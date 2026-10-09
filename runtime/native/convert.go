package native

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/grafana/sobek"
)

var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// bigInt returns the JS BigInt for the decimal string s. int64 and uint64
// convert to a bigint, not a number, so values beyond 2^53 keep full precision.
func bigInt(vm *sobek.Runtime, s string) sobek.Value {
	fn, ok := sobek.AssertFunction(vm.Get("BigInt"))
	if !ok {
		return vm.ToValue(s)
	}
	v, err := fn(sobek.Undefined(), vm.ToValue(s))
	if err != nil {
		panic(vm.NewGoError(err))
	}
	return v
}

// bigintMarker wraps an int64/uint64 value JSON.stringify would otherwise
// choke on (a JS bigint) so toGo can carry it through the JSON round-trip
// without losing precision: the replacer emits it as this distinctive string,
// which bigintMarkerRe then unquotes into a bare JSON number for json.Unmarshal
// to decode into the int64/uint64 field exactly.
const (
	bigintMarkerPrefix = "oGbIgInT:"
	bigintMarkerSuffix = ":oGbIgInT"
)

var bigintMarkerRe = regexp.MustCompile(`"` + bigintMarkerPrefix + `(-?\d+)` + bigintMarkerSuffix + `"`)

// toGo converts a JavaScript argument to t: []byte from a Uint8Array (any
// view), an ArrayBuffer or a string; time.Time from a Date (or what Date
// parses); a top-level int64/uint64 from a bigint; anything else through JSON,
// as encoding/json decodes it (a nested bigint survives the JSON round-trip as
// a bare number, so a struct/slice/map field keeps full 64-bit precision).
// undefined gives the zero value.
func toGo(vm *sobek.Runtime, v sobek.Value, t reflect.Type) (reflect.Value, error) {
	if v == nil || sobek.IsUndefined(v) {
		return reflect.Zero(t), nil
	}
	switch t {
	case bytesType:
		if sobek.IsNull(v) {
			return reflect.Zero(t), nil
		}
		switch x := v.Export().(type) {
		case []byte:
			return reflect.ValueOf(append([]byte(nil), x...)), nil
		case sobek.ArrayBuffer:
			return reflect.ValueOf(append([]byte(nil), x.Bytes()...)), nil
		case string:
			return reflect.ValueOf([]byte(x)), nil
		}
		var b []byte
		if err := vm.ExportTo(v, &b); err == nil {
			return reflect.ValueOf(b), nil
		}
		return reflect.Value{}, errNotBytes
	case timeType:
		if tm, ok := v.Export().(time.Time); ok {
			return reflect.ValueOf(tm), nil
		}
		d, err := vm.New(vm.Get("Date"), v)
		if err != nil {
			return reflect.Value{}, err
		}
		tm, ok := d.Export().(time.Time)
		if !ok {
			return reflect.Value{}, fmt.Errorf("not a date: %s", v)
		}
		return reflect.ValueOf(tm), nil
	case int64Type, uint64Type:
		// A top-level int64/uint64 is a JS bigint (full 64-bit precision). A
		// plain number also works — fall through to the JSON path below.
		if bi, ok := v.Export().(*big.Int); ok {
			if t == uint64Type {
				return reflect.ValueOf(bi.Uint64()), nil
			}
			return reflect.ValueOf(bi.Int64()), nil
		}
	}
	// JSON.stringify would throw on a nested bigint ("Do not know how to
	// serialize a BigInt"); the replacer turns each into a marker string that
	// json.Unmarshal then reads back as a bare number (see bigintMarker).
	stringify, _ := sobek.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	s, err := stringify(sobek.Undefined(), v, vm.ToValue(markBigInts(vm)))
	if err != nil {
		return reflect.Value{}, err
	}
	p := reflect.New(t)
	if sobek.IsUndefined(s) {
		return p.Elem(), nil
	}
	text := bigintMarkerRe.ReplaceAllString(s.String(), "$1")
	if err := json.Unmarshal([]byte(text), p.Interface()); err != nil {
		return reflect.Value{}, err
	}
	return p.Elem(), nil
}

// markBigInts is a JSON.stringify replacer that renders each bigint as a
// bigintMarker string, so stringify does not throw and toGo can restore the
// value with full precision.
func markBigInts(vm *sobek.Runtime) func(sobek.FunctionCall) sobek.Value {
	return func(call sobek.FunctionCall) sobek.Value {
		val := call.Argument(1)
		if bi, ok := val.Export().(*big.Int); ok {
			return vm.ToValue(bigintMarkerPrefix + bi.String() + bigintMarkerSuffix)
		}
		return val
	}
}

// toJS converts a result to its JS value. At the top level []byte is a
// Uint8Array and time.Time a Date; everything else mirrors encoding/json's
// shapes, except an int64/uint64 at any depth is a bigint (so a nested id keeps
// full 64-bit precision) rather than a number. Nested []byte and time.Time keep
// their JSON forms (a base64 string, an RFC 3339 string), as the generated .d.ts
// declares.
func toJS(vm *sobek.Runtime, v reflect.Value) sobek.Value {
	if !v.IsValid() {
		return sobek.Undefined()
	}
	switch v.Type() {
	case bytesType:
		if v.IsNil() {
			return sobek.Null()
		}
		b := append([]byte(nil), v.Bytes()...)
		u, err := vm.New(vm.Get("Uint8Array"), vm.ToValue(vm.NewArrayBuffer(b)))
		if err != nil {
			panic(err)
		}
		return u
	case timeType:
		d, err := vm.New(vm.Get("Date"), vm.ToValue(float64(v.Interface().(time.Time).UnixNano())/1e6))
		if err != nil {
			panic(err)
		}
		return d
	}
	return nestedJS(vm, v)
}

// nestedJS converts a Go value to JS preserving int64/uint64 precision as a
// bigint at any depth. []byte, time.Time and other json.Marshaler types keep
// their encoding/json forms (via jsonToJS); structs with embedded fields and
// maps with non-string keys defer wholesale to encoding/json, so json's own
// promotion and key rules still apply (nested int64 precision is not preserved
// inside those rarer shapes).
func nestedJS(vm *sobek.Runtime, v reflect.Value) sobek.Value {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return sobek.Null()
		}
		v = v.Elem()
	}
	t := v.Type()
	if t == timeType || t.Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(jsonMarshalerType) {
		return jsonToJS(vm, v)
	}
	switch v.Kind() {
	case reflect.Bool:
		return vm.ToValue(v.Bool())
	case reflect.String:
		return vm.ToValue(v.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32:
		return vm.ToValue(float64(v.Int()))
	case reflect.Int64:
		return bigInt(vm, strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uintptr:
		return vm.ToValue(float64(v.Uint()))
	case reflect.Uint64:
		return bigInt(vm, strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		return vm.ToValue(v.Float())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return jsonToJS(vm, v) // []byte -> base64 string (or null), as json does when nested
		}
		if v.IsNil() {
			return sobek.Null()
		}
		return seqToJS(vm, v)
	case reflect.Array:
		return seqToJS(vm, v)
	case reflect.Map:
		if v.IsNil() {
			return sobek.Null()
		}
		if v.Type().Key().Kind() != reflect.String {
			return jsonToJS(vm, v)
		}
		return mapToJS(vm, v)
	case reflect.Struct:
		return structToJS(vm, v)
	}
	return jsonToJS(vm, v)
}

// seqToJS converts a slice or array to a JS array.
func seqToJS(vm *sobek.Runtime, v reflect.Value) sobek.Value {
	items := make([]interface{}, v.Len())
	for i := range items {
		items[i] = nestedJS(vm, v.Index(i))
	}
	return vm.NewArray(items...)
}

// mapToJS converts a string-keyed map to a JS object, keys sorted as
// encoding/json sorts them.
func mapToJS(vm *sobek.Runtime, v reflect.Value) sobek.Value {
	keys := v.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	o := vm.NewObject()
	for _, k := range keys {
		_ = o.Set(k.String(), nestedJS(vm, v.MapIndex(k)))
	}
	return o
}

// structToJS converts a struct to a JS object following encoding/json's field
// rules (json tag name, omitempty, "-" skip, unexported skip), promoting the
// fields of a plain embedded struct. An embedding shape json treats specially
// (a tagged or non-struct anonymous field, or a json.Marshaler), or a field
// name that appears at more than one depth (json's shadowing), defers the whole
// struct to encoding/json so its own rules hold.
func structToJS(vm *sobek.Runtime, v reflect.Value) sobek.Value {
	fields, ok := flattenFields(v)
	if !ok {
		return jsonToJS(vm, v)
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if seen[f.name] {
			return jsonToJS(vm, v) // a name at two depths: let json shadow it
		}
		seen[f.name] = true
	}
	o := vm.NewObject()
	for _, f := range fields {
		_ = o.Set(f.name, nestedJS(vm, f.val))
	}
	return o
}

type outField struct {
	name string
	val  reflect.Value
}

// flattenFields collects a struct's JS output fields, promoting the fields of a
// plain embedded struct (encoding/json's promotion). ok is false for an
// embedding shape json treats specially: a tagged or non-struct anonymous field,
// or one whose type is a json.Marshaler.
func flattenFields(v reflect.Value) ([]outField, bool) {
	t := v.Type()
	var out []outField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			ft, fv := f.Type, v.Field(i)
			if ft.Kind() == reflect.Pointer {
				if fv.IsNil() {
					continue // a nil embedded pointer contributes no fields
				}
				ft, fv = ft.Elem(), fv.Elem()
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			named := tag != "" && strings.Split(tag, ",")[0] != ""
			if ft.Kind() != reflect.Struct || named ||
				ft.Implements(jsonMarshalerType) || reflect.PointerTo(ft).Implements(jsonMarshalerType) {
				return nil, false
			}
			sub, ok := flattenFields(fv)
			if !ok {
				return nil, false
			}
			out = append(out, sub...)
			continue
		}
		if f.PkgPath != "" {
			continue // unexported
		}
		name, omitempty, skip := jsonField(f)
		if skip {
			continue
		}
		fv := v.Field(i)
		if omitempty && isEmptyValue(fv) {
			continue
		}
		out = append(out, outField{name, fv})
	}
	return out, true
}

// jsonField reads encoding/json's view of a struct field: its name (the tag's,
// else the field's), whether it is omitempty, and whether it is skipped ("-").
func jsonField(f reflect.StructField) (name string, omitempty, skip bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	name = f.Name
	if tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] != "" {
			name = parts[0]
		}
		for _, o := range parts[1:] {
			if o == "omitempty" {
				omitempty = true
			}
		}
	}
	return name, omitempty, false
}

// isEmptyValue mirrors encoding/json's omitempty emptiness test.
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}

// jsonToJS converts a value exactly as encoding/json does, through a JSON
// round-trip (its nested forms: base64 for []byte, RFC 3339 for time.Time).
func jsonToJS(vm *sobek.Runtime, v reflect.Value) sobek.Value {
	b, err := json.Marshal(v.Interface())
	if err != nil {
		panic(vm.NewGoError(err))
	}
	parse, _ := sobek.AssertFunction(vm.Get("JSON").ToObject(vm).Get("parse"))
	out, err := parse(sobek.Undefined(), vm.ToValue(string(b)))
	if err != nil {
		panic(err)
	}
	return out
}
