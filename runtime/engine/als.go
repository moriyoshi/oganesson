package engine

import "github.com/grafana/sobek"

// frame is one immutable link in the async context chain. Each
// AsyncLocalStorage.run pushes a frame; lookups walk toward the root.
type frame struct {
	parent *frame
	key    *sobek.Object
	value  sobek.Value
}

func (f *frame) lookup(key *sobek.Object) (sobek.Value, bool) {
	for ; f != nil; f = f.parent {
		if f.key == key {
			return f.value, true
		}
	}
	return nil, false
}

// asyncContext implements sobek.AsyncContextTracker so that the active frame
// survives await and Promise reactions. The loop restores frames for timers
// and Go async completions.
type asyncContext struct {
	current *frame
}

func (c *asyncContext) Grab() interface{}     { return c.current }
func (c *asyncContext) Resumed(o interface{}) { c.current, _ = o.(*frame) }
func (c *asyncContext) Exited()               { c.current = nil }

func (c *asyncContext) runIn(f *frame, fn func() error) error {
	prev := c.current
	c.current = f
	defer func() { c.current = prev }()
	return fn()
}

// installAsyncLocalStorage defines the AsyncLocalStorage class (globally and
// for node:async_hooks) with run, getStore, exit, enterWith, disable, and the
// static bind and snapshot helpers.
func installAsyncLocalStorage(vm *sobek.Runtime, c *asyncContext) *sobek.Object {
	var ctor *sobek.Object
	ctor = vm.ToValue(func(call sobek.ConstructorCall) *sobek.Object {
		self := call.This
		set := func(name string, fn func(sobek.FunctionCall) sobek.Value) {
			_ = self.DefineDataProperty(name, vm.ToValue(fn), sobek.FLAG_TRUE, sobek.FLAG_FALSE, sobek.FLAG_FALSE)
		}
		runWith := func(f *frame, fnv sobek.Value, args []sobek.Value) sobek.Value {
			fn, ok := sobek.AssertFunction(fnv)
			if !ok {
				panic(vm.NewTypeError("callback is not a function"))
			}
			var out sobek.Value
			err := c.runIn(f, func() error {
				v, err := fn(sobek.Undefined(), args...)
				out = v
				return err
			})
			if err != nil {
				panic(err)
			}
			return out
		}
		set("run", func(fc sobek.FunctionCall) sobek.Value {
			f := &frame{parent: c.current, key: self, value: fc.Argument(0)}
			return runWith(f, fc.Argument(1), restArgs(fc, 2))
		})
		set("exit", func(fc sobek.FunctionCall) sobek.Value {
			f := &frame{parent: c.current, key: self, value: sobek.Undefined()}
			return runWith(f, fc.Argument(0), restArgs(fc, 1))
		})
		set("getStore", func(sobek.FunctionCall) sobek.Value {
			if v, ok := c.current.lookup(self); ok {
				return v
			}
			return sobek.Undefined()
		})
		set("enterWith", func(fc sobek.FunctionCall) sobek.Value {
			c.current = &frame{parent: c.current, key: self, value: fc.Argument(0)}
			return sobek.Undefined()
		})
		set("disable", func(sobek.FunctionCall) sobek.Value {
			c.current = &frame{parent: c.current, key: self, value: sobek.Undefined()}
			return sobek.Undefined()
		})
		return nil
	}).ToObject(vm)

	bindTo := func(f *frame, fnv sobek.Value) func(sobek.FunctionCall) sobek.Value {
		fn, ok := sobek.AssertFunction(fnv)
		if !ok {
			panic(vm.NewTypeError("callback is not a function"))
		}
		return func(fc sobek.FunctionCall) sobek.Value {
			var out sobek.Value
			err := c.runIn(f, func() error {
				v, err := fn(fc.This, fc.Arguments...)
				out = v
				return err
			})
			if err != nil {
				panic(err)
			}
			return out
		}
	}
	_ = ctor.Set("bind", func(fc sobek.FunctionCall) sobek.Value {
		return vm.ToValue(bindTo(c.current, fc.Argument(0)))
	})
	_ = ctor.Set("snapshot", func(sobek.FunctionCall) sobek.Value {
		f := c.current
		return vm.ToValue(func(fc sobek.FunctionCall) sobek.Value {
			return bindTo(f, fc.Argument(0))(sobek.FunctionCall{This: fc.This, Arguments: restArgs(fc, 1)})
		})
	})
	return ctor
}

func restArgs(fc sobek.FunctionCall, from int) []sobek.Value {
	if len(fc.Arguments) <= from {
		return nil
	}
	return fc.Arguments[from:]
}

// installContextThen defines globalThis.__ogContextThen(fn): fn wrapped to
// run in the async context current now (gojatarget.ContextualThenables
// calls it from then getters: the engine reads then while resolving, in
// the context Node runs the thenable job in, and calls it later without
// one).
func installContextThen(vm *sobek.Runtime, c *asyncContext) {
	contextThen := func(fc sobek.FunctionCall) sobek.Value {
		fnv := fc.Argument(0)
		fn, ok := sobek.AssertFunction(fnv)
		if !ok {
			return fnv
		}
		f := c.current
		w := vm.ToValue(func(call sobek.FunctionCall) sobek.Value {
			var out sobek.Value
			err := c.runIn(f, func() error {
				v, err := fn(call.This, call.Arguments...)
				out = v
				return err
			})
			if err != nil {
				panic(err)
			}
			return out
		}).ToObject(vm)
		_ = w.DefineDataProperty("name", vm.ToValue("then"), sobek.FLAG_FALSE, sobek.FLAG_TRUE, sobek.FLAG_FALSE)
		_ = w.DefineDataProperty("length", fnv.ToObject(vm).Get("length"), sobek.FLAG_FALSE, sobek.FLAG_TRUE, sobek.FLAG_FALSE)
		return w
	}
	_ = vm.GlobalObject().DefineDataProperty("__ogContextThen", vm.ToValue(contextThen), sobek.FLAG_TRUE, sobek.FLAG_FALSE, sobek.FLAG_TRUE)
}
