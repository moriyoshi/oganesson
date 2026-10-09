// Package native exposes Go functions to an app's server-side JavaScript as
// og:<name> modules (import { hashPassword } from "og:hash").
//
// A module is registered, before the app serves, from an init function: og
// build generates that for a project's Go packages (a Go module in the
// project's og/ directory, one package per native module), and an ejected
// module (og build -eject) can register its own:
//
//	native.Register(native.Module{Name: "hash", Funcs: []native.Func{
//		{Name: "hashPassword", Fn: hash.HashPassword},
//		{Name: "verify", Fn: hash.Verify, Async: true},
//	}})
//
// Values cross as encoding/json converts them (structs by their json tags),
// except that a []byte is a Uint8Array, a time.Time a Date, and a top-level
// int64/uint64 a bigint (so values beyond 2^53 keep full precision; nested in
// a struct or slice they stay JSON numbers), arguments and results alike. A
// function whose first parameter is a
// context.Context gets the request's context (cancelled when the request's
// engine ends). A synchronous function runs on the event loop and returns
// its result; an asynchronous one runs on a goroutine and returns a
// Promise. A non-nil error as the last result throws (or rejects) an Error
// with its message; so does a panic.
package native

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// Func is one exported function: its JavaScript name, the Go function, and
// whether it is asynchronous (returns a Promise and runs off the event
// loop).
type Func struct {
	Name  string
	Fn    any
	Async bool
}

// Module is a native module: og:<Name> exports its Funcs.
type Module struct {
	Name  string
	Funcs []Func
}

// reserved are og:* names og itself uses (its modules, and the virtual
// modules of its builds).
var reserved = map[string]bool{
	"kv": true, "sharp": true, "better-sqlite3": true, "node-globals": true, "cache-handler": true,
	"entry": true, "middleware": true, "manifests": true, "actions": true, "client-registry": true,
	"webpack-runtime": true, "client-entry": true, "pages-client-entry": true, "rolldown-real": true,
	"acorn": true, "remapping": true, "sourcemap-codec": true, "config-file": true, "server-program-glue": true,
}

var (
	mu       sync.RWMutex
	registry = map[string]Module{}
)

var (
	contextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType   = reflect.TypeOf((*error)(nil)).Elem()
	bytesType   = reflect.TypeOf([]byte(nil))
	timeType    = reflect.TypeOf(time.Time{})
	int64Type   = reflect.TypeOf(int64(0))
	uint64Type  = reflect.TypeOf(uint64(0))
)

// Register adds a module. It panics on a reserved or duplicate name, a
// value that is not a function, a variadic function, or more than two
// results (or two whose last is not an error).
func Register(m Module) {
	if m.Name == "" || reserved[m.Name] || strings.ContainsAny(m.Name, ":/ ") {
		panic(fmt.Sprintf("native: invalid module name %q", m.Name))
	}
	seen := map[string]bool{}
	for _, f := range m.Funcs {
		t := reflect.TypeOf(f.Fn)
		switch {
		case f.Name == "" || seen[f.Name]:
			panic(fmt.Sprintf("native: og:%s: empty or duplicate function name %q", m.Name, f.Name))
		case t == nil || t.Kind() != reflect.Func:
			panic(fmt.Sprintf("native: og:%s.%s is not a function", m.Name, f.Name))
		case t.IsVariadic():
			panic(fmt.Sprintf("native: og:%s.%s is variadic", m.Name, f.Name))
		case t.NumOut() > 2 || t.NumOut() == 2 && t.Out(1) != errorType:
			panic(fmt.Sprintf("native: og:%s.%s must return at most a value and an error", m.Name, f.Name))
		}
		seen[f.Name] = true
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[m.Name]; dup {
		panic(fmt.Sprintf("native: og:%s registered twice", m.Name))
	}
	registry[m.Name] = m
}

// Names lists the registered modules.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func lookup(name string) (Module, bool) {
	mu.RLock()
	defer mu.RUnlock()
	m, ok := registry[name]
	return m, ok
}

// IsReserved reports an og:* name og provides itself (not a native module).
func IsReserved(name string) bool { return reserved[name] }

// Plugin resolves imports of the named native modules (og:<name>) to
// modules that read the registered functions when they load, so a bundle
// needs no list of the functions: named imports are read as properties.
// Only these names: og's own modules use og:* too.
func Plugin(names []string) api.Plugin {
	return api.Plugin{Name: "og-native", Setup: func(b api.PluginBuild) {
		if len(names) == 0 {
			return
		}
		alts := make([]string, len(names))
		for i, n := range names {
			alts[i] = regexp.QuoteMeta(n)
		}
		b.OnResolve(api.OnResolveOptions{Filter: `^og:(` + strings.Join(alts, "|") + `)$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: strings.TrimPrefix(a.Path, "og:"), Namespace: "og-native"}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "og-native"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			src := fmt.Sprintf("module.exports = globalThis[Symbol.for(\"og.native\")](%q);\n", a.Path)
			return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
		})
	}}
}

// Install gives e's JavaScript the registered modules:
// globalThis[Symbol.for("og.native")](name) is og:<name>'s exports (made
// once per engine). Contexts passed to the functions end with the engine.
func Install(e *engine.Engine) error {
	vm := e.VM
	ctx, cancel := context.WithCancel(context.Background())
	e.OnClose(cancel)
	made := map[string]*sobek.Object{}
	load := func(name string) *sobek.Object {
		if o, ok := made[name]; ok {
			return o
		}
		m, ok := lookup(name)
		if !ok {
			panic(vm.NewGoError(fmt.Errorf("og: no native module og:%s in this binary (a Go package og/%s in the project, built with og build -o; or native.Register in an ejected module)", name, name)))
		}
		o := vm.NewObject()
		for _, f := range m.Funcs {
			_ = o.Set(f.Name, wrap(e, ctx, m.Name, f))
		}
		made[name] = o
		return o
	}
	install, err := vm.RunString(`(function (f) { Object.defineProperty(globalThis, Symbol.for("og.native"), { value: f }); })`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), vm.ToValue(load))
	return err
}

// wrap makes f callable from JavaScript.
func wrap(e *engine.Engine, ctx context.Context, module string, f Func) func(sobek.FunctionCall) sobek.Value {
	vm := e.VM
	fv := reflect.ValueOf(f.Fn)
	t := fv.Type()
	return func(call sobek.FunctionCall) sobek.Value {
		args := make([]reflect.Value, t.NumIn())
		j := 0
		for i := 0; i < t.NumIn(); i++ {
			in := t.In(i)
			if i == 0 && in == contextType {
				args[i] = reflect.ValueOf(ctx)
				continue
			}
			v, err := toGo(vm, call.Argument(j), in)
			j++
			if err != nil {
				panic(vm.NewTypeError("og:%s.%s: argument %d: %v", module, f.Name, j, err))
			}
			args[i] = v
		}
		if !f.Async {
			out, err := invoke(fv, args)
			if err != nil {
				panic(jsError(vm, err))
			}
			return toJS(vm, out)
		}
		promise, resolve, reject := vm.NewPromise()
		done := e.Loop.Async()
		go func() {
			out, err := invoke(fv, args)
			done(func(vm *sobek.Runtime) error {
				if err != nil {
					return reject(jsError(vm, err))
				}
				return resolve(toJS(vm, out))
			})
		}()
		return vm.ToValue(promise)
	}
}

// jsError is err as a JavaScript Error with its message.
func jsError(vm *sobek.Runtime, err error) *sobek.Object {
	e, cerr := vm.New(vm.Get("Error"), vm.ToValue(err.Error()))
	if cerr != nil {
		return vm.NewGoError(err)
	}
	return e
}

// invoke calls fv, turning a returned error or a panic into err; out is the
// value result, if any.
func invoke(fv reflect.Value, args []reflect.Value) (out reflect.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	res := fv.Call(args)
	if n := len(res); n > 0 && fv.Type().Out(n-1) == errorType {
		if e, _ := res[n-1].Interface().(error); e != nil {
			return reflect.Value{}, e
		}
		res = res[:n-1]
	}
	if len(res) == 1 {
		return res[0], nil
	}
	return reflect.Value{}, nil
}

var errNotBytes = errors.New("expected a Uint8Array, an ArrayBuffer or a string")
