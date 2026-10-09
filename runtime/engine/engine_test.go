package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/grafana/sobek"
)

// newTestEngine exposes the host object as globalThis.host for tests.
func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e := New(Options{})
	if err := e.VM.Set("host", e.Host); err != nil {
		t.Fatal(err)
	}
	if _, err := e.VM.RunString(`var log = []; var ALS = host.AsyncLocalStorage;`); err != nil {
		t.Fatal(err)
	}
	return e
}

func runJS(t *testing.T, e *Engine, src string) {
	t.Helper()
	err := e.Run(context.Background(), func(vm *sobek.Runtime) error {
		_, err := vm.RunString(src)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func joinedLog(t *testing.T, e *Engine) string {
	t.Helper()
	v, err := e.VM.RunString(`log.join(",")`)
	if err != nil {
		t.Fatal(err)
	}
	return v.String()
}

func TestTimerOrdering(t *testing.T) {
	e := newTestEngine(t)
	runJS(t, e, `
		host.timers.set(() => log.push("b"), 20);
		host.timers.set(() => log.push("a"), 5);
		host.timers.set(() => log.push("a2"), 5);
		Promise.resolve().then(() => log.push("micro"));
		log.push("sync");
	`)
	if got := joinedLog(t, e); got != "sync,micro,a,a2,b" {
		t.Fatalf("order = %q", got)
	}
}

func TestIntervalAndClear(t *testing.T) {
	e := newTestEngine(t)
	runJS(t, e, `
		let n = 0;
		const id = host.timers.set(() => { log.push(++n); if (n === 3) host.timers.clear(id); }, 1, true);
	`)
	if got := joinedLog(t, e); got != "1,2,3" {
		t.Fatalf("log = %q", got)
	}
}

func TestUnrefTimerDoesNotKeepLoopAlive(t *testing.T) {
	e := newTestEngine(t)
	start := time.Now()
	runJS(t, e, `
		const id = host.timers.set(() => log.push("never"), 10000);
		host.timers.ref(id, false);
	`)
	if time.Since(start) > time.Second {
		t.Fatal("unref'd timer kept the loop alive")
	}
	if got := joinedLog(t, e); got != "" {
		t.Fatalf("log = %q", got)
	}
}

func TestAsyncLocalStoragePropagation(t *testing.T) {
	e := newTestEngine(t)
	// A Go async op started inside run() must complete in the same context.
	_ = e.Host.Set("goAsync", func(fc sobek.FunctionCall) sobek.Value {
		cb, _ := sobek.AssertFunction(fc.Argument(0))
		complete := e.Loop.Async()
		go func() {
			time.Sleep(2 * time.Millisecond)
			complete(func(*sobek.Runtime) error { _, err := cb(sobek.Undefined()); return err })
		}()
		return sobek.Undefined()
	})
	runJS(t, e, `
		const als = new ALS();
		const other = new ALS();
		const get = () => String(als.getStore()) + "/" + String(other.getStore());
		als.run("r1", async () => {
			log.push("sync:" + get());
			await null;
			log.push("await:" + get());
			other.run("o", () => {
				host.timers.set(() => log.push("timer:" + get()), 1);
			});
			host.goAsync(() => log.push("go:" + get()));
		});
		als.run("r2", () => {
			Promise.resolve().then(() => log.push("then:" + get()));
		});
		log.push("outside:" + get());
	`)
	got := joinedLog(t, e)
	want := []string{"sync:r1/undefined", "outside:undefined/undefined", "await:r1/undefined", "then:r2/undefined", "timer:r1/o", "go:r1/undefined"}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
}

func TestAwait(t *testing.T) {
	e := newTestEngine(t)
	v, err := e.VM.RunString(`new Promise((r) => host.timers.set(() => r(42), 1))`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Await(context.Background(), v)
	if err != nil || res.ToInteger() != 42 {
		t.Fatalf("Await = %v, %v", res, err)
	}

	v, _ = e.VM.RunString(`Promise.reject(new Error("boom"))`)
	if _, err := e.Await(context.Background(), v); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("rejection error = %v", err)
	}

	v, _ = e.VM.RunString(`new Promise(() => {})`)
	if _, err := e.Await(context.Background(), v); !errors.Is(err, ErrStalled) {
		t.Fatalf("stalled error = %v", err)
	}
}

func TestInterruptOnContextCancel(t *testing.T) {
	e := newTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := e.Run(ctx, func(vm *sobek.Runtime) error {
		_, err := vm.RunString(`host.timers.set(() => { for (;;) {} }, 0)`)
		return err
	})
	if !errors.Is(err, ErrInterrupted) {
		t.Fatalf("err = %v", err)
	}
}

func TestUncaughtHandlerCanContinue(t *testing.T) {
	var seen []string
	e := New(Options{Uncaught: func(err error) error { seen = append(seen, err.Error()); return nil }})
	_ = e.VM.Set("host", e.Host)
	runJS(t, e, `
		var log = [];
		host.timers.set(() => { throw new Error("first") }, 1);
		host.timers.set(() => log.push("still running"), 2);
	`)
	if got := joinedLog(t, e); got != "still running" || len(seen) != 1 {
		t.Fatalf("log = %q, uncaught = %v", got, seen)
	}
}

func TestRunReturnsInitialErrorDespiteLenientHandler(t *testing.T) {
	e := New(Options{Uncaught: func(error) error { return nil }})
	err := e.Run(context.Background(), func(vm *sobek.Runtime) error {
		_, err := vm.RunString(`throw new Error("top-level")`)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "top-level") {
		t.Fatalf("err = %v", err)
	}
}

// Unbounded recursion ends the job with Node's RangeError message instead
// of overflowing the goroutine stack, which kills the process. (Goja's
// error cannot be caught by the code, unlike Node's.)
func TestCallStackLimit(t *testing.T) {
	e := newTestEngine(t)
	runJS(t, e, `
		function deep(n) { return n === 0 ? 0 : deep(n - 1) + 1; }
		log.push(deep(5000));
	`)
	if got := joinedLog(t, e); got != "5000" {
		t.Fatalf("got %s", got)
	}
	err := e.Run(context.Background(), func(vm *sobek.Runtime) error {
		_, err := vm.RunString(`function down(n) { return down(n + 1) + 1; } down(0);`)
		return err
	})
	if err == nil || !strings.HasPrefix(err.Error(), "RangeError: Maximum call stack size exceeded") {
		t.Fatalf("err %v", err)
	}
}
