//go:build ekuma

package jsvm

import (
	"fmt"
	"math"
	"math/big"
	"reflect"
	"time"
	"unicode"

	"github.com/hajimehoshi/ekuma"
)

type Value interface {
	String() string
	ToBoolean() bool
	ToFloat() float64
	ToInteger() int64
	ToObject(*Runtime) *Object
	Export() any
	rawValue(*Runtime) ekuma.Value
}

type value struct {
	vm  *Runtime
	raw ekuma.Value
}

type Object struct{ value }
type Symbol struct {
	value
	name string
}

func NewSymbol(name string) *Symbol { return &Symbol{name: name} }

func (vm *Runtime) wrap(v ekuma.Value) Value {
	switch v.Type() {
	case ekuma.TypeObject, ekuma.TypeFunction:
		return vm.object(v)
	case ekuma.TypeSymbol:
		return &Symbol{value: value{vm, v}}
	default:
		return value{vm, v}
	}
}

func (vm *Runtime) object(v ekuma.Value) *Object { return &Object{value{vm, v}} }
func (v value) rawValue(*Runtime) ekuma.Value    { return v.raw }

func (s *Symbol) rawValue(vm *Runtime) ekuma.Value {
	if s.vm != nil {
		return s.raw
	}
	if v, ok := vm.symbols[s]; ok {
		return v
	}
	fn := vm.Get("Symbol")
	call, _ := AssertFunction(fn)
	v, err := call(Undefined(), vm.ToValue(s.name))
	if err != nil {
		panic(err)
	}
	raw := v.rawValue(vm)
	vm.symbols[s] = raw
	return raw
}

func raw(vm *Runtime, v Value) ekuma.Value {
	if v == nil {
		return ekuma.Undefined
	}
	return v.rawValue(vm)
}

func isSymbol(v Value) bool { _, ok := v.(*Symbol); return ok }

func Null() Value              { return value{raw: ekuma.Null} }
func Undefined() Value         { return value{raw: ekuma.Undefined} }
func IsNull(v Value) bool      { return v != nil && !isSymbol(v) && v.rawValue(nil).IsNull() }
func IsUndefined(v Value) bool { return v != nil && !isSymbol(v) && v.rawValue(nil).IsUndefined() }

func (v value) String() string {
	switch v.raw.Type() {
	case ekuma.TypeNull:
		return "null"
	case ekuma.TypeUndefined:
		return "undefined"
	case ekuma.TypeString:
		return v.raw.String()
	default:
		return v.vm.call(str, v.raw).String()
	}
}

func (v value) ToBoolean() bool { return v.raw.Truthy() }

func (v value) ToFloat() float64 {
	switch v.raw.Type() {
	case ekuma.TypeNumber:
		return v.raw.Float()
	case ekuma.TypeUndefined:
		return math.NaN()
	case ekuma.TypeNull:
		return 0
	default:
		return v.vm.call(num, v.raw).Float()
	}
}

func (v value) ToInteger() int64 {
	n := v.ToFloat()
	switch {
	case math.IsNaN(n):
		return 0
	case n >= math.MaxInt64:
		return math.MaxInt64
	case n <= math.MinInt64:
		return math.MinInt64
	default:
		return int64(n)
	}
}

func (v value) ToObject(vm *Runtime) *Object {
	if v.raw.IsUndefined() || v.raw.IsNull() {
		panic(vm.NewTypeError("cannot convert null or undefined to object"))
	}
	return vm.object(vm.call(box, v.raw))
}

func (o *Object) ToObject(*Runtime) *Object   { return o }
func (o *Object) Get(name string) Value       { return o.get(o.vm.vm.ValueOf(name)) }
func (o *Object) GetSymbol(key *Symbol) Value { return o.get(key.rawValue(o.vm)) }

func (o *Object) get(key ekuma.Value) Value {
	v := o.vm.call(getProperty, o.raw, key)
	// The Go host distinguishes an absent property from a JS undefined value.
	if v.IsUndefined() && !o.vm.call(has, o.raw, key).Bool() {
		return nil
	}
	return o.vm.wrap(v)
}

func (o *Object) Set(name string, v any) error {
	return o.set(name, v)
}

func (o *Object) SetSymbol(key *Symbol, v any) error {
	return o.set(key.rawValue(o.vm), v)
}

func (o *Object) set(key, v any) error {
	ok, err := o.vm.helpers[setProperty].Invoke(o.vm.context(), o.raw, key, o.vm.toJS(v))
	if err != nil {
		return o.vm.wrapError(err)
	}
	if !ok.Bool() {
		return fmt.Errorf("cannot set JavaScript property %v", key)
	}
	return nil
}

func (o *Object) Delete(name string) error {
	ok, err := o.vm.helpers[deleteProperty].Invoke(o.vm.context(), o.raw, name)
	if err != nil {
		return o.vm.wrapError(err)
	}
	if !ok.Bool() {
		return fmt.Errorf("cannot delete JavaScript property %s", name)
	}
	return nil
}

func (o *Object) Keys() []string {
	a := o.vm.call(keys, o.raw)
	n, err := a.Get("length")
	if err != nil {
		panic(o.vm.wrapError(err))
	}
	out := make([]string, n.Int())
	for i := range out {
		v, err := a.Index(i)
		if err != nil {
			panic(o.vm.wrapError(err))
		}
		out[i] = v.String()
	}
	return out
}

type Flag bool

const (
	FLAG_FALSE Flag = false
	FLAG_TRUE  Flag = true
)

func (o *Object) DefineDataProperty(name string, v Value, writable, configurable, enumerable Flag) error {
	_, err := o.vm.helpers[define].Invoke(o.vm.context(), o.raw, name, raw(o.vm, v), bool(writable), bool(configurable), bool(enumerable))
	return o.vm.wrapError(err)
}

type ArrayBuffer struct{ value }

func (vm *Runtime) NewArrayBuffer(b []byte) ArrayBuffer {
	// Web Streams may transfer these buffers. Allocate ordinary JS storage
	// and reserve host-only detachment for live WebAssembly memory.
	view := vm.call(makeBytes, len(b))
	ekuma.CopyBytesToJS(view, b)
	return ArrayBuffer{value{vm, vm.call(getProperty, view, "buffer")}}
}

func WrapArrayBuffer(vm *Runtime, b []byte) ArrayBuffer {
	return ArrayBuffer{value{vm, vm.vm.NewArrayBuffer(b)}}
}

func (a ArrayBuffer) Bytes() []byte {
	if a.Detached() {
		return nil
	}
	return copyBytes(a.vm.call(byteView, a.raw))
}

func (a ArrayBuffer) Detached() bool {
	return a.vm.call(bufferDetached, a.raw).Bool()
}

func (a ArrayBuffer) Detach() bool {
	if a.Detached() {
		return false
	}
	a.vm.vm.DetachArrayBuffer(a.raw)
	return true
}

func copyBytes(v ekuma.Value) []byte {
	n, err := v.Get("byteLength")
	if err != nil {
		panic(err)
	}
	b := make([]byte, n.Int())
	ekuma.CopyBytesToGo(b, v)
	return b
}

func (v value) Export() any { return v.export(nil) }

func (v value) export(path []ekuma.Value) any {
	switch v.raw.Type() {
	case ekuma.TypeUndefined, ekuma.TypeNull:
		return nil
	case ekuma.TypeBoolean:
		return v.raw.Bool()
	case ekuma.TypeString:
		return v.raw.String()
	case ekuma.TypeNumber:
		n := v.raw.Float()
		if n >= math.MinInt64 && n < math.MaxInt64 && n == math.Trunc(n) && !(n == 0 && math.Signbit(n)) {
			return int64(n)
		}
		return n
	case ekuma.TypeBigInt:
		return v.raw.BigInt()
	case ekuma.TypeFunction, ekuma.TypeSymbol:
		return v.vm.wrap(v.raw)
	}
	vm := v.vm
	if p, ok := AsPromise(vm.wrap(v.raw)); ok {
		return p
	}
	if id := vm.call(opaqueGet, v.raw); !id.IsUndefined() {
		return vm.opaque[id.Int()]
	}
	if vm.call(isView, v.raw).Bool() {
		return copyBytes(vm.call(viewBytes, v.raw))
	}
	if vm.call(isBuffer, v.raw).Bool() {
		return ArrayBuffer{v}
	}
	if timeValue := vm.call(dateTime, v.raw); !timeValue.IsUndefined() {
		ms := timeValue.Float()
		if !math.IsNaN(ms) {
			return time.UnixMilli(int64(ms))
		}
		return nil
	}
	for _, parent := range path {
		if parent.Equal(v.raw) {
			return nil
		}
	}
	path = append(path, v.raw)
	o := vm.object(v.raw)
	if vm.call(isArray, v.raw).Bool() {
		a := make([]any, int(o.Get("length").ToInteger()))
		for i := range a {
			item := vm.call(getProperty, v.raw, i)
			a[i] = (value{vm, item}).export(path)
		}
		return a
	}
	obj := make(map[string]any)
	for _, k := range o.Keys() {
		item := vm.call(getProperty, v.raw, k)
		obj[k] = (value{vm, item}).export(path)
	}
	return obj
}

func (vm *Runtime) ToValue(v any) Value { return vm.wrap(vm.vm.ValueOf(vm.toJS(v))) }

func (vm *Runtime) toJS(v any) any {
	switch v := v.(type) {
	case nil, bool, string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, uintptr, float32, float64, *big.Int:
		return v
	case *Object:
		if v == nil {
			return nil
		}
		return v.raw
	case Value:
		return v.rawValue(vm)
	case *Promise:
		return v.raw
	case ArrayBuffer:
		return v.raw
	case time.Time:
		d, err := vm.New(vm.Get("Date"), vm.ToValue(v.UnixMilli()))
		if err != nil {
			panic(err)
		}
		return d.raw
	case error:
		return vm.NewGoError(v).raw
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Func:
		return vm.hostFunc(r)
	case reflect.Array, reflect.Slice:
		a := make([]any, r.Len())
		for i := range a {
			a[i] = vm.toJS(r.Index(i).Interface())
		}
		return a
	case reflect.Map:
		obj := make(map[string]any, r.Len())
		for i := r.MapRange(); i.Next(); {
			obj[i.Key().String()] = vm.toJS(i.Value().Interface())
		}
		return obj
	case reflect.Pointer:
		if r.IsNil() {
			return nil
		}
		// Private host handles are passed back to Go by the wasm wrappers.
		return vm.nativeHandle(v)
	case reflect.Struct:
		obj := make(map[string]any)
		for i := range r.NumField() {
			field := r.Type().Field(i)
			if field.IsExported() {
				obj[uncap(field.Name)] = vm.toJS(r.Field(i).Interface())
			}
		}
		return obj
	case reflect.String:
		return r.String()
	case reflect.Bool:
		return r.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return r.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return r.Uint()
	case reflect.Float32, reflect.Float64:
		return r.Float()
	default:
		panic(fmt.Sprintf("jsvm: cannot convert %T to JavaScript", v))
	}
}

func uncap(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

func (vm *Runtime) nativeHandle(v any) ekuma.Value {
	o := vm.NewObject()
	vm.nextID++
	id := vm.nextID
	vm.call(opaqueSet, o.raw, id)
	if _, err := vm.finalizer.Call(vm.context(), "register", o.raw, id); err != nil {
		panic(vm.wrapError(err))
	}
	vm.opaque[id] = v
	return o.raw
}

var (
	valueType       = reflect.TypeFor[Value]()
	arrayBufferType = reflect.TypeFor[ArrayBuffer]()
	objectType      = reflect.TypeFor[*Object]()
	errorType       = reflect.TypeFor[error]()
	callableType    = reflect.TypeFor[Callable]()
)

func (vm *Runtime) ExportTo(v Value, dest any) error {
	return vm.exportTo(v, reflect.ValueOf(dest).Elem())
}

func (vm *Runtime) exportTo(v Value, dest reflect.Value) error {
	switch dest.Type() {
	case callableType:
		if IsNull(v) || IsUndefined(v) {
			return nil
		}
		f, ok := AssertFunction(v)
		if !ok {
			return fmt.Errorf("expected function")
		}
		dest.Set(reflect.ValueOf(f))
		return nil
	case valueType:
		dest.Set(reflect.ValueOf(v))
		return nil
	case objectType:
		if !IsNull(v) && !IsUndefined(v) {
			dest.Set(reflect.ValueOf(v.ToObject(vm)))
		}
		return nil
	case arrayBufferType:
		if ab, ok := v.Export().(ArrayBuffer); ok {
			dest.Set(reflect.ValueOf(ab))
			return nil
		}
		return fmt.Errorf("expected ArrayBuffer")
	}
	switch dest.Kind() {
	case reflect.String:
		dest.SetString(v.String())
	case reflect.Bool:
		dest.SetBool(v.ToBoolean())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		dest.SetInt(v.ToInteger())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		dest.SetUint(uint64(v.ToInteger()))
	case reflect.Float32, reflect.Float64:
		dest.SetFloat(v.ToFloat())
	case reflect.Slice:
		if IsNull(v) || IsUndefined(v) {
			return nil
		}
		if dest.Type().Elem().Kind() == reflect.Uint8 {
			switch x := v.Export().(type) {
			case []byte:
				dest.SetBytes(x)
				return nil
			case ArrayBuffer:
				dest.SetBytes(x.Bytes())
				return nil
			}
		}
		o := v.ToObject(vm)
		n := o.Get("length")
		if n == nil {
			return fmt.Errorf("expected array")
		}
		out := reflect.MakeSlice(dest.Type(), int(n.ToInteger()), int(n.ToInteger()))
		for i := range out.Len() {
			item := vm.call(getProperty, o.raw, i)
			if err := vm.exportTo(vm.wrap(item), out.Index(i)); err != nil {
				return err
			}
		}
		dest.Set(out)
	case reflect.Map:
		if IsNull(v) || IsUndefined(v) {
			return nil
		}
		if dest.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("expected a map with string keys")
		}
		o := v.ToObject(vm)
		m := reflect.MakeMap(dest.Type())
		for _, key := range o.Keys() {
			elem := reflect.New(dest.Type().Elem()).Elem()
			if err := vm.exportTo(vm.wrap(vm.call(getProperty, o.raw, key)), elem); err != nil {
				return err
			}
			m.SetMapIndex(reflect.ValueOf(key).Convert(dest.Type().Key()), elem)
		}
		dest.Set(m)
	case reflect.Interface:
		if x := v.Export(); x != nil {
			dest.Set(reflect.ValueOf(x))
		}
	default:
		return fmt.Errorf("jsvm: cannot export JavaScript value to %s", dest.Type())
	}
	return nil
}

type FunctionCall struct {
	This      Value
	Arguments []Value
}

func (c FunctionCall) Argument(i int) Value {
	if i >= len(c.Arguments) {
		return Undefined()
	}
	return c.Arguments[i]
}

type ConstructorCall struct{ This *Object }

type Callable func(Value, ...Value) (Value, error)

func AssertFunction(v Value) (Callable, bool) {
	fn, ok := v.(*Object)
	if !ok || fn.raw.Type() != ekuma.TypeFunction {
		return nil, false
	}
	vm := fn.vm
	return func(this Value, args ...Value) (Value, error) {
		var out ekuma.Value
		var err error
		if this == nil || IsUndefined(this) {
			actual := make([]any, len(args))
			for i, a := range args {
				actual[i] = raw(vm, a)
			}
			out, err = fn.raw.Invoke(vm.context(), actual...)
		} else {
			out, err = vm.helpers[apply].Invoke(vm.context(), fn.raw, raw(vm, this), vm.NewArray(valueArgs(args)...).raw)
		}
		return vm.wrap(out), vm.wrapError(err)
	}, true
}

func AssertConstructor(v Value) (func(*Object, ...Value) (*Object, error), bool) {
	fn, ok := v.(*Object)
	if !ok || fn.raw.Type() != ekuma.TypeFunction {
		return nil, false
	}
	vm := fn.vm
	return func(target *Object, args ...Value) (*Object, error) {
		var out ekuma.Value
		var err error
		if target == nil {
			actual := make([]any, len(args))
			for i, a := range args {
				actual[i] = raw(vm, a)
			}
			out, err = fn.raw.New(vm.context(), actual...)
		} else {
			out, err = vm.helpers[construct].Invoke(vm.context(), fn.raw, vm.NewArray(valueArgs(args)...).raw, target.raw)
		}
		if err != nil {
			return nil, vm.wrapError(err)
		}
		return vm.object(out), nil
	}, true
}

func valueArgs(args []Value) []any {
	out := make([]any, len(args))
	for i, v := range args {
		out[i] = v
	}
	return out
}

func (vm *Runtime) hostFunc(fn reflect.Value) ekuma.Value {
	native := vm.vm.FuncOf("host", func(this ekuma.Value, args []ekuma.Value) (result any, err error) {
		defer func() {
			switch p := recover().(type) {
			case nil:
			case *Object:
				err = &ekuma.ScriptError{Value: p.raw}
			case *Exception:
				err = p.err
			default:
				panic(p)
			}
		}()
		wrapped := make([]Value, len(args))
		for i, a := range args {
			wrapped[i] = vm.wrap(a)
		}
		call := FunctionCall{This: vm.wrap(this), Arguments: wrapped}
		switch f := fn.Interface().(type) {
		case func(FunctionCall) Value:
			return raw(vm, f(call)), nil
		case func(ConstructorCall) *Object:
			if obj := f(ConstructorCall{This: vm.object(this)}); obj != nil {
				return obj.raw, nil
			}
			return ekuma.Undefined, nil
		}
		params := make([]reflect.Value, fn.Type().NumIn())
		for i := range params {
			params[i] = reflect.New(fn.Type().In(i)).Elem()
			if err := vm.exportTo(call.Argument(i), params[i]); err != nil {
				return nil, err
			}
		}
		out := fn.Call(params)
		if len(out) == 0 {
			return ekuma.Undefined, nil
		}
		if last := out[len(out)-1]; last.Type().Implements(errorType) {
			if err, ok := last.Interface().(error); ok && err != nil {
				return nil, err
			}
			out = out[:len(out)-1]
		}
		switch len(out) {
		case 0:
			return ekuma.Undefined, nil
		case 1:
			return vm.toJS(out[0].Interface()), nil
		default:
			panic(fmt.Sprintf("jsvm: unsupported host function %s", fn.Type()))
		}
	})
	if _, ok := fn.Interface().(func(ConstructorCall) *Object); ok {
		return vm.call(constructorWrapper, native)
	}
	return native
}

// Keep String's primitive behavior for error formatting, including Symbols.
var _ fmt.Stringer = (*Object)(nil)
