package engine

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/grafana/sobek"
)

// Loop is a single-threaded event loop that owns one sobek.Runtime. All JS
// runs on the goroutine that called Run; other goroutines hand work to it via
// Async completions.
//
// The loop exits when nothing can make progress: no referenced timers, no
// outstanding async operations, and an empty job queue.
type Loop struct {
	vm      *sobek.Runtime
	actx    *asyncContext
	mu      sync.Mutex
	queue   []func() error // jobs posted from any goroutine
	wake    chan struct{}  // signaled (non-blocking) when queue grows
	timers  timerHeap
	nextID  int64
	byID    map[int64]*timer
	pending int // outstanding Async operations
	refs    int // referenced (not unref'd) armed timers
	// weak counts live handles that do not keep the loop running but may
	// still post work (unreferenced workers): Engine.Await waits for them
	// before declaring a pending promise unsettleable.
	weak  int
	start time.Time

	// uncaught receives errors thrown by timer callbacks and async
	// completions. Returning a non-nil error aborts the loop.
	uncaught func(error) error
	// rejected holds promises rejected without a handler, in order, when
	// rejections are tracked (Options.UnhandledRejections).
	rejected []*sobek.Promise
	tracking bool
	// uncaughtSym is Symbol.for("og.uncaught") (claim), once looked up.
	uncaughtSym *sobek.Symbol
}

// trackRejections records unhandled rejections for call to report.
func (l *Loop) trackRejections(vm *sobek.Runtime) {
	l.tracking = true
	vm.SetPromiseRejectionTracker(func(p *sobek.Promise, op sobek.PromiseRejectionOperation) {
		switch op {
		case sobek.PromiseRejectionReject:
			l.rejected = append(l.rejected, p)
		case sobek.PromiseRejectionHandle:
			for i, q := range l.rejected {
				if q == p {
					l.rejected = append(l.rejected[:i], l.rejected[i+1:]...)
					break
				}
			}
		}
	})
}

// reportRejections hands the rejections still unhandled after a job to
// uncaught.
func (l *Loop) reportRejections() error {
	for len(l.rejected) > 0 {
		p := l.rejected[0]
		l.rejected = l.rejected[1:]
		reason := p.Result()
		var err error
		if ex, ok := reason.Export().(error); ok {
			err = ex
		} else {
			err = fmt.Errorf("Uncaught (in promise) %s", reason.String())
		}
		if obj, ok := reason.(*sobek.Object); ok {
			if stack := obj.Get("stack"); stack != nil && !sobek.IsUndefined(stack) {
				err = fmt.Errorf("%s", stack.String())
			}
		}
		if handled, herr := l.claim(reason, p); herr != nil {
			err = herr
		} else if handled {
			continue
		}
		if err := l.uncaught(err); err != nil {
			return err
		}
	}
	return nil
}

// claim offers an uncaught exception (p nil) or unhandled rejection to the
// realm's handler, globalThis[Symbol.for("og.uncaught")](value, promise),
// which reports whether it handled it: the Node shims deliver it to
// process's "uncaughtException" / "unhandledRejection" listeners, as Node
// does. An error thrown by the handler takes the original's place.
func (l *Loop) claim(v sobek.Value, p *sobek.Promise) (handled bool, err error) {
	if l.uncaughtSym == nil {
		s, rerr := l.vm.RunString(`Symbol.for("og.uncaught")`)
		if rerr != nil {
			return false, nil
		}
		l.uncaughtSym, _ = s.(*sobek.Symbol)
		if l.uncaughtSym == nil {
			return false, nil
		}
	}
	fn, ok := sobek.AssertFunction(l.vm.GlobalObject().GetSymbol(l.uncaughtSym))
	if !ok {
		return false, nil
	}
	promise := sobek.Undefined()
	if p != nil {
		promise = l.vm.ToValue(p)
	}
	r, err := fn(sobek.Undefined(), v, promise)
	if err != nil {
		return false, err
	}
	return r.ToBoolean(), nil
}

type timer struct {
	id       int64
	when     time.Time
	interval time.Duration // 0 for one-shot
	fn       func() error
	ctx      *frame
	ref      bool
	index    int // position in the heap, -1 when not armed
}

func newLoop(vm *sobek.Runtime, actx *asyncContext) *Loop {
	return &Loop{
		vm: vm, actx: actx, start: time.Now(),
		wake:     make(chan struct{}, 1),
		byID:     map[int64]*timer{},
		uncaught: func(err error) error { return err },
	}
}

// Async starts an operation whose completion will run on the loop. It must be
// called on the loop goroutine. The returned function must be called exactly
// once, from any goroutine; its argument runs on the loop in the async
// context that was current when Async was called.
func (l *Loop) Async() (complete func(func(*sobek.Runtime) error)) {
	l.pending++
	ctx := l.actx.current
	var once sync.Once
	return func(fn func(*sobek.Runtime) error) {
		once.Do(func() {
			l.post(func() error {
				l.pending--
				return l.actx.runIn(ctx, func() error { return fn(l.vm) })
			})
		})
	}
}

// Post schedules fn on the loop from any goroutine. Unlike Async it does not
// keep the loop alive: if the loop is idle, fn runs only if the loop is
// driven again (Run, Await or Drain).
func (l *Loop) Post(fn func(*sobek.Runtime) error) {
	l.post(func() error { return fn(l.vm) })
}

// Bind captures the async context current on the loop goroutine. The
// returned function posts fn to run in that context, from any goroutine;
// like Post, it does not keep the loop alive (an I/O handle's events: its
// data arrives in the context that opened it, as Node's async resources).
func (l *Loop) Bind() func(func(*sobek.Runtime) error) {
	ctx := l.actx.current
	return func(fn func(*sobek.Runtime) error) {
		l.post(func() error { return l.actx.runIn(ctx, func() error { return fn(l.vm) }) })
	}
}

func (l *Loop) post(job func() error) {
	l.mu.Lock()
	l.queue = append(l.queue, job)
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *Loop) popJob() func() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) == 0 {
		return nil
	}
	job := l.queue[0]
	l.queue[0] = nil
	l.queue = l.queue[1:]
	return job
}

// SetTimer schedules fn after d (repeating if interval) in the current async
// context. It must be called on the loop goroutine.
func (l *Loop) SetTimer(d time.Duration, repeat bool, fn func() error) int64 {
	if d < 0 {
		d = 0
	}
	l.nextID++
	t := &timer{id: l.nextID, when: time.Now().Add(d), fn: fn, ctx: l.actx.current, ref: true}
	if repeat {
		t.interval = max(d, time.Millisecond)
	}
	l.byID[t.id] = t
	heap.Push(&l.timers, t)
	l.refs++
	return t.id
}

// ClearTimer cancels a timer; unknown ids are ignored.
func (l *Loop) ClearTimer(id int64) {
	t, ok := l.byID[id]
	if !ok {
		return
	}
	delete(l.byID, id)
	if t.index >= 0 {
		heap.Remove(&l.timers, t.index)
		if t.ref {
			l.refs--
		}
	}
}

// RefTimer marks whether a timer keeps the loop alive (Node's ref/unref).
func (l *Loop) RefTimer(id int64, ref bool) {
	t, ok := l.byID[id]
	if !ok || t.ref == ref {
		return
	}
	t.ref = ref
	if t.index >= 0 {
		if ref {
			l.refs++
		} else {
			l.refs--
		}
	}
}

// Weak adjusts the count of weak handles (loop goroutine only).
func (l *Loop) Weak(delta int) { l.weak += delta }

// ErrInterrupted is returned when ctx ends before the loop drains.
var ErrInterrupted = errors.New("engine: interrupted")

// ErrStalled is returned by Engine.Await when the loop goes idle while the
// awaited promise is still pending: nothing can ever settle it.
var ErrStalled = errors.New("engine: promise can never settle (event loop is idle)")

// errIdle signals that step found nothing left to do.
var errIdle = errors.New("engine: idle")

// run drives the loop until it is idle or ctx is done.
func (l *Loop) run(ctx context.Context) error {
	for {
		switch err := l.step(ctx); err {
		case nil:
		case errIdle:
			return nil
		default:
			return err
		}
	}
}

// step performs one unit of work: a queued job or a due timer, waiting for
// one if necessary. It returns errIdle when nothing can make progress.
func (l *Loop) step(ctx context.Context) error { return l.stepAlive(ctx, false) }

// stepAlive is step; alive keeps it from going idle (something outside the
// loop's counts keeps it running: weak handles while Engine.Await waits),
// so it waits for posted work or the next timer, unreferenced ones
// included, as Node fires unref'd timers while other handles keep the
// process alive.
func (l *Loop) stepAlive(ctx context.Context, alive bool) error {
	if ctx.Err() != nil {
		return ErrInterrupted
	}
	stop := context.AfterFunc(ctx, func() { l.vm.Interrupt(ErrInterrupted) })
	defer stop()

	// Run already-queued jobs before looking at timers.
	if job := l.popJob(); job != nil {
		return l.call(job)
	}
	if !alive && l.pending == 0 && l.refs == 0 {
		return errIdle
	}
	var tm *time.Timer
	var wait <-chan time.Time
	if len(l.timers) > 0 {
		d := time.Until(l.timers[0].when)
		if d <= 0 {
			return l.fireTimer()
		}
		tm = time.NewTimer(d)
		wait = tm.C
	}
	select {
	case <-l.wake:
	case <-wait:
	case <-ctx.Done():
	}
	if tm != nil {
		tm.Stop()
	}
	if ctx.Err() != nil {
		return ErrInterrupted
	}
	return nil // the next step picks up whatever woke us
}

func (l *Loop) fireTimer() error {
	t := heap.Pop(&l.timers).(*timer)
	if t.ref {
		l.refs--
	}
	if t.interval > 0 {
		t.when = time.Now().Add(t.interval)
		heap.Push(&l.timers, t)
		if t.ref {
			l.refs++
		}
	} else {
		delete(l.byID, t.id)
	}
	return l.call(func() error { return l.actx.runIn(t.ctx, t.fn) })
}

// call runs a job. Interrupts always abort; other errors go to uncaught.
func (l *Loop) call(job func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			switch v := r.(type) {
			case *sobek.Exception:
				if handled, herr := l.claim(v.Value(), nil); herr != nil {
					err = l.uncaught(herr)
				} else if !handled {
					err = l.uncaught(v)
				}
			case *sobek.InterruptedError:
				err = ErrInterrupted
			case *sobek.StackOverflowError:
				err = l.uncaught(stackOverflow(v))
			default:
				// A Go panic from inside Goja or a host function must not take
				// the process down; surface it as an error for this engine.
				err = &PanicError{Value: r, Stack: debug.Stack()}
			}
		}
	}()
	if err := job(); err != nil {
		var ie *sobek.InterruptedError
		if errors.As(err, &ie) {
			return ErrInterrupted
		}
		var ex *sobek.Exception
		if errors.As(err, &ex) {
			if handled, herr := l.claim(ex.Value(), nil); herr != nil {
				return l.uncaught(herr)
			} else if handled {
				return l.reportIfTracking()
			}
		}
		return l.uncaught(stackOverflow(err))
	}
	return l.reportIfTracking()
}

func (l *Loop) reportIfTracking() error {
	if l.tracking {
		return l.reportRejections()
	}
	return nil
}

type timerHeap []*timer

func (h timerHeap) Len() int { return len(h) }
func (h timerHeap) Less(i, j int) bool {
	if h[i].when.Equal(h[j].when) {
		return h[i].id < h[j].id
	}
	return h[i].when.Before(h[j].when)
}
func (h timerHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *timerHeap) Push(x any) {
	t := x.(*timer)
	t.index = len(*h)
	*h = append(*h, t)
}
func (h *timerHeap) Pop() any {
	old := *h
	t := old[len(old)-1]
	old[len(old)-1] = nil
	t.index = -1
	*h = old[:len(old)-1]
	return t
}

// PanicError wraps a Go panic recovered while running JS.
type PanicError struct {
	Value any
	Stack []byte
}

func (p *PanicError) Error() string { return fmt.Sprintf("engine: panic: %v\n%s", p.Value, p.Stack) }

// stackOverflow gives Goja's stack overflow (uncatchable there, a
// RangeError in Node) Node's message.
func stackOverflow(err error) error {
	var so *sobek.StackOverflowError
	if errors.As(err, &so) {
		// The innermost frames show the recursion.
		lines := strings.Split(so.String(), "\n")
		if len(lines) > 40 {
			lines = append(lines[:40], fmt.Sprintf("\t... %d more frames", len(lines)-40))
		}
		return fmt.Errorf("RangeError: Maximum call stack size exceeded\n%s", strings.Join(lines, "\n"))
	}
	return err
}
