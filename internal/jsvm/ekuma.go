//go:build ekuma

package jsvm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hajimehoshi/ekuma"
	_ "github.com/hajimehoshi/ekuma/intl"
)

const UsesEkuma = true

type Program = ekuma.UnboundScript
type CompilerSyntaxError = ekuma.SyntaxError

func Compile(name, source string, strict bool) (*Program, error) {
	if strict {
		source = `"use strict"; ` + source
	}
	return ekuma.ParseUnboundScript(name, source)
}

func MustCompile(name, source string, strict bool) *Program {
	p, err := Compile(name, source, strict)
	if err != nil {
		panic(err)
	}
	return p
}

func CompileCommonJS(name, source string, _ func(string) ([]byte, error)) (*Program, error) {
	return Compile(name, source, false)
}

// The helpers capture intrinsics before application scripts can replace them.
var helpers = MustCompile("og:jsvm", `(() => {
	const {apply, construct, get: getProperty, set: setProperty, deleteProperty} = Reflect, {defineProperty, keys} = Object;
	const {isArray} = Array, {isView} = ArrayBuffer, {getTime} = Date.prototype;
	const {get: bufferDetached} = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, "detached");
	const {get: bufferLength} = Object.getOwnPropertyDescriptor(ArrayBuffer.prototype, "byteLength");
	const U8 = Uint8Array, Box = Object, Str = String, Err = Error, Num = Number;
	const opaque = new WeakMap(), {get, set} = WeakMap.prototype;
	return [
		apply, construct, keys, isArray, Box, Str, Num,
		(v) => { try { return apply(getTime, v, []); } catch { return undefined; } },
		(v) => new U8(v),
		(v) => new U8(v.buffer, v.byteOffset, v.byteLength),
		isView,
		(v, k, value, writable, configurable, enumerable) =>
			defineProperty(v, k, {value, writable, configurable, enumerable}),
		(fn) => function (...args) { return apply(fn, this, args); },
		() => new Err().stack,
		(v) => apply(get, opaque, [v]),
		(v, id) => { apply(set, opaque, [v, id]); },
		(v) => { try { apply(bufferLength, v, []); return true; } catch { return false; } },
		(v, k) => k in v, getProperty, setProperty, deleteProperty,
		(v) => apply(bufferDetached, v, []),
		(n) => new U8(n),
	];
})()`, true)

const (
	apply = iota
	construct
	keys
	isArray
	box
	str
	num
	dateTime
	byteView
	viewBytes
	isView
	define
	constructorWrapper
	stack
	opaqueGet
	opaqueSet
	isBuffer
	has
	getProperty
	setProperty
	deleteProperty
	bufferDetached
	makeBytes
	numHelpers
)

// Runtime owns the VM and the private JS operations that adapt og's host
// bindings to Ekuma's explicit value and callback API.
type Runtime struct {
	vm      ekuma.VM
	helpers [numHelpers]ekuma.Value
	ctx     context.Context
	unbound context.Context
	cancel  context.CancelCauseFunc

	opaque    map[int]any
	nextID    int
	finalizer ekuma.Value
	symbols   map[*Symbol]ekuma.Value
}

func New() *Runtime {
	vm := &Runtime{opaque: make(map[int]any), symbols: make(map[*Symbol]ekuma.Value)}
	vm.unbound, vm.cancel = context.WithCancelCause(context.Background())
	v, err := vm.vm.BindScript(helpers).Evaluate(context.Background())
	if err != nil {
		panic(err)
	}
	for i := range vm.helpers {
		vm.helpers[i], err = v.Index(i)
		if err != nil {
			panic(err)
		}
	}
	ctor, err := vm.vm.Global().Get("FinalizationRegistry")
	if err != nil {
		panic(err)
	}
	vm.finalizer, err = ctor.New(context.Background(), vm.vm.FuncOf("release", func(_ ekuma.Value, args []ekuma.Value) (any, error) {
		delete(vm.opaque, args[0].Int())
		return ekuma.Undefined, nil
	}))
	if err != nil {
		panic(err)
	}
	return vm
}

func (vm *Runtime) context() context.Context {
	if vm.ctx != nil {
		return vm.ctx
	}
	return vm.unbound
}

func WithContext(vm *Runtime, ctx context.Context, reason error, f func() error) error {
	prev := vm.ctx
	ctx, cancel := context.WithCancelCause(ctx)
	vm.ctx = ctx
	defer func() { vm.ctx = prev; cancel(nil) }()
	err := f()
	if err != nil && ctx.Err() != nil && errors.Is(err, context.Cause(ctx)) {
		return reason
	}
	return err
}

// Interrupt supports the watchdog used by tests. Production calls supply a
// context directly via WithContext.
func (vm *Runtime) Interrupt(reason any) {
	vm.cancel(fmt.Errorf("%v", reason))
}

func (vm *Runtime) call(i int, args ...any) ekuma.Value {
	v, err := vm.helpers[i].Invoke(vm.context(), args...)
	if err != nil {
		panic(vm.wrapError(err))
	}
	return v
}

func (vm *Runtime) RunProgram(p *Program) (Value, error) {
	v, err := vm.vm.BindScript(p).Evaluate(vm.context())
	return vm.wrap(v), vm.wrapError(err)
}

func (vm *Runtime) RunScript(name, source string) (Value, error) {
	p, err := Compile(name, source, false)
	if err != nil {
		return nil, err
	}
	return vm.RunProgram(p)
}

func (vm *Runtime) RunString(source string) (Value, error) { return vm.RunScript("<eval>", source) }
func (vm *Runtime) GlobalObject() *Object                  { return vm.object(vm.vm.Global()) }
func (vm *Runtime) Get(name string) Value                  { return vm.GlobalObject().Get(name) }
func (vm *Runtime) Set(name string, value any) error       { return vm.GlobalObject().Set(name, value) }
func (vm *Runtime) NewObject() *Object                     { return vm.object(vm.vm.ValueOf(map[string]any{})) }
func (vm *Runtime) NewArray(args ...any) *Object {
	values := make([]any, len(args))
	for i, value := range args {
		values[i] = vm.toJS(value)
	}
	return vm.object(vm.vm.ValueOf(values))
}

func (vm *Runtime) New(constructor Value, args ...Value) (*Object, error) {
	c, ok := AssertConstructor(constructor)
	if !ok {
		return nil, fmt.Errorf("JavaScript value is not a constructor")
	}
	return c(nil, args...)
}

func (vm *Runtime) newError(name, message string) *Object {
	v, err := vm.New(vm.Get(name), vm.ToValue(message))
	if err != nil {
		panic(err)
	}
	return v
}

func (vm *Runtime) NewGoError(err error) *Object        { return vm.newError("Error", err.Error()) }
func (vm *Runtime) NewTypeError(message string) *Object { return vm.newError("TypeError", message) }

type Exception struct {
	err *ekuma.ScriptError
	vm  *Runtime
}

func (e *Exception) Error() string  { return e.err.Error() }
func (e *Exception) String() string { return e.err.Error() }
func (e *Exception) Value() Value   { return e.vm.wrap(e.err.Value) }
func (e *Exception) Unwrap() error  { return e.err }

func (vm *Runtime) wrapError(err error) error {
	var ex *ekuma.ScriptError
	if errors.As(err, &ex) {
		return &Exception{err: ex, vm: vm}
	}
	return err
}

type StackFrame string

func (f StackFrame) SrcName() string { return string(f) }

var sourceFrame = regexp.MustCompile(`^(?:.* \(|\s*at )(.+):[0-9]+:[0-9]+\)?$`)

func (vm *Runtime) CaptureCallStack(depth int, dst []StackFrame) []StackFrame {
	// The first source frame is the private stack helper. The next is the
	// application frame that called the host callback.
	frames := strings.Split(vm.call(stack).String(), "\n")
	dst = append(dst, "")
	if len(frames) < 3 {
		return dst
	}
	for _, frame := range frames[2:] {
		if len(dst) >= depth {
			break
		}
		if m := sourceFrame.FindStringSubmatch(frame); m != nil {
			dst = append(dst, StackFrame(m[1]))
		}
	}
	return dst
}

type Promise struct {
	vm  *Runtime
	raw ekuma.Value
}

const (
	PromiseStatePending  = ekuma.PromisePending
	PromiseStateRejected = ekuma.PromiseRejected
)

func (p *Promise) State() ekuma.PromiseState { state, _ := p.raw.PromiseState(); return state }
func (p *Promise) Result() Value             { _, result := p.raw.PromiseState(); return p.vm.wrap(result) }

func AsPromise(v Value) (*Promise, bool) {
	if o, ok := v.(*Object); ok && o.raw.IsPromise() {
		return &Promise{vm: o.vm, raw: o.raw}, true
	}
	return nil, false
}

func (vm *Runtime) NewPromise() (*Promise, func(any) error, func(any) error) {
	p, r := vm.vm.NewPromise()
	return &Promise{vm: vm, raw: p},
		func(v any) error { return vm.wrapError(r.Resolve(vm.context(), vm.toJS(v))) },
		func(v any) error { return vm.wrapError(r.Reject(vm.context(), vm.toJS(v))) }
}

type PromiseRejectionOperation int

const (
	PromiseRejectionReject PromiseRejectionOperation = iota
	PromiseRejectionHandle
)

func (vm *Runtime) SetPromiseRejectionTracker(f func(*Promise, PromiseRejectionOperation)) {
	vm.vm.SetUnhandledRejection(func(p, _ ekuma.Value) {
		f(&Promise{vm: vm, raw: p}, PromiseRejectionReject)
	})
}

func (vm *Runtime) ContinuationData() any                   { return vm.vm.ContinuationData() }
func (vm *Runtime) WithContinuationData(data any, f func()) { vm.vm.WithContinuationData(data, f) }

func SameObject(a, b *Object) bool { return a.raw.Equal(b.raw) }

func IsInterrupted(error) bool { return false }
