package engine

import "github.com/moriyoshi/oganesson/internal/jsvm"

// frame is one immutable link in the async context chain. Each
// AsyncLocalStorage.run pushes a frame; lookups walk toward the root.
type frame struct {
	parent *frame
	key    *jsvm.Object
	value  jsvm.Value
}

func (f *frame) lookup(key *jsvm.Object) (jsvm.Value, bool) {
	for ; f != nil; f = f.parent {
		if jsvm.SameObject(f.key, key) {
			return f.value, true
		}
	}
	return nil, false
}

// installAsyncLocalStorage defines the AsyncLocalStorage class (globally and
// for node:async_hooks) with run, getStore, exit, enterWith, disable, and the
// static bind and snapshot helpers.
func installAsyncLocalStorage(vm *jsvm.Runtime, c *asyncContext) *jsvm.Object {
	var ctor *jsvm.Object
	ctor = vm.ToValue(func(call jsvm.ConstructorCall) *jsvm.Object {
		self := call.This
		set := func(name string, fn func(jsvm.FunctionCall) jsvm.Value) {
			_ = self.DefineDataProperty(name, vm.ToValue(fn), jsvm.FLAG_TRUE, jsvm.FLAG_FALSE, jsvm.FLAG_FALSE)
		}
		runWith := func(f *frame, fnv jsvm.Value, args []jsvm.Value) jsvm.Value {
			fn, ok := jsvm.AssertFunction(fnv)
			if !ok {
				panic(vm.NewTypeError("callback is not a function"))
			}
			var out jsvm.Value
			err := c.runIn(f, func() error {
				v, err := fn(jsvm.Undefined(), args...)
				out = v
				return err
			})
			if err != nil {
				panic(err)
			}
			return out
		}
		set("run", func(fc jsvm.FunctionCall) jsvm.Value {
			f := &frame{parent: c.capture(), key: self, value: fc.Argument(0)}
			return runWith(f, fc.Argument(1), restArgs(fc, 2))
		})
		set("exit", func(fc jsvm.FunctionCall) jsvm.Value {
			f := &frame{parent: c.capture(), key: self, value: jsvm.Undefined()}
			return runWith(f, fc.Argument(0), restArgs(fc, 1))
		})
		set("getStore", func(jsvm.FunctionCall) jsvm.Value {
			if v, ok := c.capture().lookup(self); ok {
				return v
			}
			return jsvm.Undefined()
		})
		set("enterWith", func(fc jsvm.FunctionCall) jsvm.Value {
			c.enter(&frame{parent: c.capture(), key: self, value: fc.Argument(0)})
			return jsvm.Undefined()
		})
		set("disable", func(jsvm.FunctionCall) jsvm.Value {
			c.enter(&frame{parent: c.capture(), key: self, value: jsvm.Undefined()})
			return jsvm.Undefined()
		})
		return nil
	}).ToObject(vm)

	bindTo := func(f *frame, fnv jsvm.Value) func(jsvm.FunctionCall) jsvm.Value {
		fn, ok := jsvm.AssertFunction(fnv)
		if !ok {
			panic(vm.NewTypeError("callback is not a function"))
		}
		return func(fc jsvm.FunctionCall) jsvm.Value {
			var out jsvm.Value
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
	_ = ctor.Set("bind", func(fc jsvm.FunctionCall) jsvm.Value {
		return vm.ToValue(bindTo(c.capture(), fc.Argument(0)))
	})
	_ = ctor.Set("snapshot", func(jsvm.FunctionCall) jsvm.Value {
		f := c.capture()
		return vm.ToValue(func(fc jsvm.FunctionCall) jsvm.Value {
			return bindTo(f, fc.Argument(0))(jsvm.FunctionCall{This: fc.This, Arguments: restArgs(fc, 1)})
		})
	})
	return ctor
}

func restArgs(fc jsvm.FunctionCall, from int) []jsvm.Value {
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
func installContextThen(vm *jsvm.Runtime, c *asyncContext) {
	contextThen := func(fc jsvm.FunctionCall) jsvm.Value {
		fnv := fc.Argument(0)
		fn, ok := jsvm.AssertFunction(fnv)
		if !ok {
			return fnv
		}
		f := c.capture()
		w := vm.ToValue(func(call jsvm.FunctionCall) jsvm.Value {
			var out jsvm.Value
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
		_ = w.DefineDataProperty("name", vm.ToValue("then"), jsvm.FLAG_FALSE, jsvm.FLAG_TRUE, jsvm.FLAG_FALSE)
		_ = w.DefineDataProperty("length", fnv.ToObject(vm).Get("length"), jsvm.FLAG_FALSE, jsvm.FLAG_TRUE, jsvm.FLAG_FALSE)
		return w
	}
	_ = vm.GlobalObject().DefineDataProperty("__ogContextThen", vm.ToValue(contextThen), jsvm.FLAG_TRUE, jsvm.FLAG_FALSE, jsvm.FLAG_TRUE)
}
