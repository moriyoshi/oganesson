// Package engine owns one Goja runtime and its event loop. Oganesson creates a
// fresh Engine per request, which keeps requests fully isolated.
//
// The engine exposes Go primitives to JS through a host object (passed to the
// prelude, never left on globalThis). Web APIs on top of it live in
// runtime/webapi.
package engine

import (
	"context"
	"time"

	"github.com/grafana/sobek"
)

// Options configure a new Engine.
type Options struct {
	// Uncaught receives errors thrown from timer callbacks and async
	// completions. Returning nil keeps the loop running (browser semantics);
	// returning an error aborts Run with it. Defaults to aborting.
	Uncaught func(error) error
	// UnhandledRejections reports a promise rejected without a handler
	// once the job that rejected it (and its microtasks) ran, to Uncaught,
	// as Node does by default (a worker's error event).
	UnhandledRejections bool
}

// Engine is a Goja runtime plus event loop. It is not safe for concurrent use.
type Engine struct {
	VM   *sobek.Runtime
	Loop *Loop
	// Host is the private object that carries Go primitives to the prelude.
	Host *sobek.Object
	actx *asyncContext

	closers []func()
}

// OnClose registers fn to run when the engine is closed, e.g. to release
// response bodies the script never consumed.
func (e *Engine) OnClose(fn func()) { e.closers = append(e.closers, fn) }

// Close releases resources held on behalf of JS. The engine must not be used
// afterwards.
func (e *Engine) Close() {
	for i := len(e.closers) - 1; i >= 0; i-- {
		e.closers[i]()
	}
	e.closers = nil
}

// New creates an engine with timers and AsyncLocalStorage primitives
// registered on Host.
// maxCallStackSize bounds JS call depth per engine.
const maxCallStackSize = 15000

func New(opts Options) *Engine {
	vm := sobek.New()
	// Unbounded recursion throws a RangeError, as in Node (V8 allows about
	// 11k frames of a simple function), instead of growing the goroutine
	// stack until Go aborts the process.
	vm.SetMaxCallStackSize(maxCallStackSize)
	vm.SetFieldNameMapper(sobek.UncapFieldNameMapper())
	actx := &asyncContext{}
	vm.SetAsyncContextTracker(actx)
	e := &Engine{VM: vm, Loop: newLoop(vm, actx), Host: vm.NewObject(), actx: actx}
	if opts.Uncaught != nil {
		e.Loop.uncaught = opts.Uncaught
	}
	if opts.UnhandledRejections {
		e.Loop.trackRejections(vm)
	}
	e.installTimers()
	_ = e.Host.Set("AsyncLocalStorage", installAsyncLocalStorage(vm, actx))
	installContextThen(vm, actx)
	return e
}

// Run calls fn on the loop goroutine (the caller's), then drives the event
// loop until it is idle or ctx ends.
func (e *Engine) Run(ctx context.Context, fn func(vm *sobek.Runtime) error) error {
	// Errors from fn itself belong to the caller, not the uncaught handler.
	var fnErr error
	if err := e.Loop.call(func() error { fnErr = fn(e.VM); return nil }); err != nil {
		return err
	}
	if fnErr != nil {
		return stackOverflow(fnErr)
	}
	return e.Loop.run(ctx)
}

// Drain runs the event loop until it is idle or ctx ends. It is used after
// a response completes, so background work (e.g. waitUntil) can finish.
func (e *Engine) Drain(ctx context.Context) error { return e.Loop.run(ctx) }

// Await settles a promise by driving the loop. It returns the fulfilled
// value, or the rejection reason as a *sobek.Exception-compatible error.
func (e *Engine) Await(ctx context.Context, v sobek.Value) (sobek.Value, error) {
	p, ok := v.Export().(*sobek.Promise)
	if !ok {
		return v, nil
	}
	for p.State() == sobek.PromiseStatePending {
		switch err := e.Loop.step(ctx); err {
		case nil:
		case errIdle:
			// A weak handle (an unreferenced worker, a socket) may yet post
			// what the promise waits for; timers keep firing meanwhile.
			if e.Loop.weak > 0 {
				if err := e.Loop.stepAlive(ctx, true); err != nil {
					return nil, err
				}
				continue
			}
			return nil, ErrStalled
		default:
			return nil, err
		}
	}
	if p.State() == sobek.PromiseStateRejected {
		return nil, &RejectionError{Value: p.Result()}
	}
	return p.Result(), nil
}

// RejectionError wraps a rejected promise's reason.
type RejectionError struct{ Value sobek.Value }

func (r *RejectionError) Error() string {
	if o, ok := r.Value.(*sobek.Object); ok {
		if st := o.Get("stack"); st != nil && !sobek.IsUndefined(st) {
			return st.String()
		}
	}
	if r.Value == nil {
		return "undefined"
	}
	return r.Value.String()
}

func (e *Engine) installTimers() {
	vm := e.VM
	timers := vm.NewObject()
	// set(fn, ms, repeat) -> id. Arguments are bound in JS.
	_ = timers.Set("set", func(fc sobek.FunctionCall) sobek.Value {
		fn, ok := sobek.AssertFunction(fc.Argument(0))
		if !ok {
			panic(vm.NewTypeError("timer callback is not a function"))
		}
		ms := fc.Argument(1).ToFloat()
		if ms != ms || ms < 0 { // NaN or negative
			ms = 0
		}
		id := e.Loop.SetTimer(time.Duration(ms*float64(time.Millisecond)), fc.Argument(2).ToBoolean(), func() error {
			_, err := fn(sobek.Undefined())
			return err
		})
		return vm.ToValue(id)
	})
	_ = timers.Set("clear", func(id int64) { e.Loop.ClearTimer(id) })
	_ = timers.Set("ref", func(id int64, ref bool) { e.Loop.RefTimer(id, ref) })
	_ = timers.Set("now", func() float64 {
		return float64(time.Since(e.Loop.start).Nanoseconds()) / 1e6
	})
	_ = e.Host.Set("timers", timers)
}
