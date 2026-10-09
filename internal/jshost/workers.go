package jshost

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// Worker threads for build-time JS: node:worker_threads' Worker (runtime/
// nodeshim's worker_threads.js) runs its file in a separate engine on its
// own goroutine, prepared like the host's, as Node runs a worker in its own
// isolate. Messages cross as strings (worker_threads.js serializes them).
// A running, referenced worker keeps the parent's loop alive, as in Node;
// the worker lives until terminated or until it closes its port.
//
// Tooling uses workers for parallel work (@rollup/plugin-terser's pool,
// which workbox-build runs for vite-plugin-pwa).

type workerHost struct {
	h      *Host
	parent *engine.Engine
	mu     sync.Mutex
	next   int
	all    map[int]*worker
}

type worker struct {
	id     int
	cancel context.CancelFunc
	// Parent side (parent loop goroutine only).
	keep      func(func(*sobek.Runtime) error) // holds the parent loop while referenced
	callbacks *sobek.Object                    // {message, error, exit, online}
	exited    bool
	weak      bool // unreferenced: counted in the parent loop's weak handles
	// Worker side.
	we *engine.Engine
}

// installWorkers exposes the worker host to e's JS as Symbol.for("og.workers").
func (h *Host) installWorkers(e *engine.Engine) error {
	wh := &workerHost{h: h, parent: e, all: map[int]*worker{}}
	vm := e.VM
	o := vm.NewObject()
	_ = o.Set("spawn", func(file, data string, callbacks *sobek.Object, env string) int {
		return wh.spawn(file, data, callbacks, env)
	})
	_ = o.Set("post", func(id int, msg string) {
		if w := wh.get(id); w != nil && w.we != nil {
			w.we.Loop.Post(func(vm *sobek.Runtime) error { return deliver(vm, msg) })
		}
	})
	_ = o.Set("terminate", func(id int) {
		if w := wh.get(id); w != nil {
			w.cancel()
		}
	})
	_ = o.Set("ref", func(id int, ref bool) {
		w := wh.get(id)
		if w == nil || w.exited {
			return
		}
		switch {
		case ref && w.keep == nil:
			w.keep = e.Loop.Async()
			if w.weak {
				w.weak = false
				e.Loop.Weak(-1)
			}
		case !ref && w.keep != nil:
			k := w.keep
			w.keep = nil
			k(func(*sobek.Runtime) error { return nil })
			w.weak = true
			e.Loop.Weak(1)
		}
	})
	e.OnClose(func() {
		wh.mu.Lock()
		defer wh.mu.Unlock()
		for _, w := range wh.all {
			w.cancel()
		}
	})
	return defineSymbol(vm, "og.workers", o)
}

func (wh *workerHost) get(id int) *worker {
	wh.mu.Lock()
	defer wh.mu.Unlock()
	return wh.all[id]
}

// deliver hands a message to the worker's parentPort (worker_threads.js
// sets the context's onMessage).
func deliver(vm *sobek.Runtime, msg string) error {
	v, err := vm.RunString(`globalThis[Symbol.for("og.workerContext")]`)
	if err != nil || sobek.IsUndefined(v) {
		return err
	}
	if fn, ok := sobek.AssertFunction(v.ToObject(vm).Get("onMessage")); ok {
		_, err = fn(sobek.Undefined(), vm.ToValue(msg))
	}
	return err
}

// spawn starts a worker for file (on the parent loop goroutine). env, if
// not "", is the worker's process.env as JSON (the Worker's env option).
func (wh *workerHost) spawn(file, data string, callbacks *sobek.Object, env string) int {
	wh.mu.Lock()
	wh.next++
	id := wh.next
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{id: id, cancel: cancel, callbacks: callbacks}
	wh.all[id] = w
	wh.mu.Unlock()
	w.keep = wh.parent.Loop.Async()
	// Parent-side callbacks, on the parent loop.
	call := func(name string, args ...any) func(*sobek.Runtime) error {
		return func(vm *sobek.Runtime) error {
			fn, ok := sobek.AssertFunction(w.callbacks.Get(name))
			if !ok {
				return nil
			}
			vals := make([]sobek.Value, len(args))
			for i, a := range args {
				vals[i] = vm.ToValue(a)
			}
			_, err := fn(sobek.Undefined(), vals...)
			return err
		}
	}
	toParent := func(fn func(*sobek.Runtime) error) { wh.parent.Loop.Post(fn) }

	// An uncaught error (or unhandled rejection) ends the worker with code
	// 1 after its error event, as in Node.
	var failed atomic.Bool
	we := engine.New(engine.Options{UnhandledRejections: true, Uncaught: func(err error) error {
		toParent(call("error", err.Error()))
		failed.Store(true)
		cancel()
		return nil
	}})
	w.we = we
	go func() {
		defer we.Close()
		code, exitCode := 0, -1
		err := func() error {
			wctx := we.VM.NewObject()
			_ = wctx.Set("id", id)
			_ = wctx.Set("workerData", data)
			_ = wctx.Set("post", func(msg string) { toParent(call("message", msg)) })
			_ = wctx.Set("close", func() { cancel() })
			var held func(func(*sobek.Runtime) error) // worker loop goroutine only
			_ = wctx.Set("hold", func(on bool) {
				switch {
				case on && held == nil:
					held = we.Loop.Async()
				case !on && held != nil:
					k := held
					held = nil
					k(func(*sobek.Runtime) error { return nil })
				}
			})
			// process.exit in a worker ends the worker with that code.
			_ = wctx.Set("exit", func(c int) {
				exitCode = c
				cancel()
			})
			if err := defineSymbol(we.VM, "og.workerContext", wctx); err != nil {
				return err
			}
			if err := wh.h.prepare(we); err != nil {
				return err
			}
			// The worker's engine is set up as the parent's (the Vite build
			// host's Rolldown facade), and its file bundled as the parent's
			// code is, with the host's plugins and shared modules.
			if wh.h.setup != nil {
				if err := wh.h.setup(we); err != nil {
					return err
				}
			}
			// ES module workers may await at their top level.
			wb := *wh.h
			wb.TopLevelAwait = true
			if wb.WorkerShared != nil {
				wb.Shared = wb.WorkerShared
			}
			// The file is the worker's entry, evaluated whatever its
			// package's "sideEffects" says (Node ignores it; esbuild drops
			// a bare import of a side-effect-free package's file: Qwik
			// City's workers ran nothing). A used namespace keeps it.
			code, err := wb.Bundle("", fmt.Sprintf("import * as __og_worker from %s;\nglobalThis[Symbol.for(\"og.workerModule\")] = __og_worker;\n", jsonString(file)), "")
			if err != nil {
				return err
			}
			if _, err := we.VM.RunString(workerProcess); err != nil {
				return err
			}
			if env != "" {
				setEnv, err := we.VM.RunString(`(s) => { const e = process.env; for (const k of Object.keys(e)) delete e[k]; Object.assign(e, JSON.parse(s)); }`)
				if err != nil {
					return err
				}
				fn, _ := sobek.AssertFunction(setEnv)
				if _, err := fn(sobek.Undefined(), we.VM.ToValue(env)); err != nil {
					return err
				}
			}
			return we.Run(ctx, func(vm *sobek.Runtime) error {
				// As in Node, the worker ends when its loop is idle; its
				// parentPort holds the loop while it has "message" listeners
				// and is not unref'd (hold, from worker_threads.js).
				// terminate / close end it by canceling ctx.
				toParent(call("online"))
				// A failure after a top-level await is an unhandled
				// rejection: the worker's error (UnhandledRejections).
				_, err := vm.RunScript("og-worker.js", code)
				return err
			})
		}()
		if err != nil && ctx.Err() == nil {
			toParent(call("error", err.Error()))
			code = 1
		}
		if failed.Load() {
			code = 1
		}
		if exitCode >= 0 {
			code = exitCode
		}
		cancel()
		exit := func(vm *sobek.Runtime) error {
			w.exited = true
			if w.weak {
				w.weak = false
				wh.parent.Loop.Weak(-1)
			}
			wh.mu.Lock()
			delete(wh.all, id)
			wh.mu.Unlock()
			return call("exit", code)(vm)
		}
		// The exit event releases the parent's hold (or is posted when
		// the worker was unreferenced).
		toParent(func(vm *sobek.Runtime) error {
			if w.keep != nil {
				k := w.keep
				w.keep = nil
				k(exit)
				return nil
			}
			return exit(vm)
		})
	}()
	return id
}

// workerProcess makes a worker's process.exit end the worker (Node stops
// the thread; the parent sees an exit event with the code).
const workerProcess = `process.exit = (code) => globalThis[Symbol.for("og.workerContext")].exit(code === undefined ? Number(process.exitCode ?? 0) : Number(code));`

// defineSymbol sets globalThis[Symbol.for(name)] to v.
func defineSymbol(vm *sobek.Runtime, name string, v sobek.Value) error {
	install, err := vm.RunString(`(n, v) => Object.defineProperty(globalThis, Symbol.for(n), { value: v, configurable: true })`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), vm.ToValue(name), v)
	return err
}
