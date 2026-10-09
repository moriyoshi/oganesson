// Package wasm provides the WebAssembly JS API to build-time JavaScript
// (Goja), backed by wazero. It exists so that tools shipping WebAssembly
// builds of native addons run unmodified: napi-rs wasm32-wasi packages
// (e.g. Tailwind v4's oxide) and napi-wasm packages (e.g. lightningcss-wasm),
// whose JS side is plain Node-API glue over an instance.
//
// Beyond the standard API, Symbol.for("og.wasm") offers native imports
// the JS glue would otherwise build on Node's WASI and worker threads:
//
//	wasi(options)        an import namespace served by wazero's WASI
//	                     preview1 (the host filesystem, read-only)
//	native("emnapi")     emnapi's runtime helpers
//	native("wasi-threads")  {"thread-spawn"}: runs threads as goroutines,
//	                     each a fresh instance sharing the memory
//
// JS imports may only be called from the main thread: wasm threads that
// call one trap.
package wasm

import (
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/grafana/sobek"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed wasm.js
var shimSource string

// Options configure Install.
type Options struct {
	Env    map[string]string // WASI environment defaults
	Stderr io.Writer         // WASI stdout/stderr
	// Restricted withholds the Oganesson extensions (WASI with host file
	// access, native imports), leaving the standard API: for the production
	// runtime, where request code must not reach the host.
	Restricted bool
}

// Runtime is the WebAssembly support installed on one Goja runtime.
type Runtime struct {
	vm     *sobek.Runtime
	r      wazero.Runtime // created on first use (see wz)
	rOnce  sync.Once
	cfg    wazero.RuntimeConfig
	ctx    context.Context
	cancel context.CancelFunc
	opts   Options
	handle *sobek.Symbol
	start  time.Time

	seq      atomic.Int64
	threads  sync.WaitGroup // running wasm threads
	memories map[api.Memory]*memory
	wasiOnce sync.Once
	wasiErr  error

	// refs holds the JS values passed into wasm as externref (handle 0 is
	// null); nextRef is the next handle.
	refs    map[uint64]sobek.Value
	nextRef uint64

	// pending holds JS exceptions thrown from imports while wasm runs, one
	// slot per nested call from JS into wasm.
	pending []*sobek.Exception

	errCompile, errLink, errRuntime sobek.Value
}

var cache = func() wazero.CompilationCache {
	if dir, err := os.UserCacheDir(); err == nil {
		if c, err := wazero.NewCompilationCacheWithDir(filepath.Join(dir, "oganesson", "wazero")); err == nil {
			return c
		}
	}
	return wazero.NewCompilationCache()
}()

// Install defines globalThis.WebAssembly on vm. Call Close to release the
// runtime.
func Install(vm *sobek.Runtime, opts Options) (*Runtime, error) {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	ctx, cancel := context.WithCancel(context.Background())
	cfg := wazero.NewRuntimeConfig().
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesThreads).
		WithCompilationCache(cache).
		WithCloseOnContextDone(true)
	rt := &Runtime{
		vm: vm, cfg: cfg, ctx: ctx, cancel: cancel, opts: opts,
		handle: sobek.NewSymbol("og.wasm.handle"), memories: map[api.Memory]*memory{}, start: time.Now(),
	}
	native := vm.NewObject()
	set := func(name string, fn func(sobek.FunctionCall) sobek.Value) { _ = native.Set(name, fn) }
	set("setErrors", func(c sobek.FunctionCall) sobek.Value {
		rt.errCompile, rt.errLink, rt.errRuntime = c.Argument(0), c.Argument(1), c.Argument(2)
		return sobek.Undefined()
	})
	set("compile", rt.jsCompile)
	set("moduleImports", rt.jsModuleImports)
	set("moduleExports", rt.jsModuleExports)
	set("instantiate", rt.jsInstantiate)
	set("newMemory", rt.jsNewMemory)
	set("memoryBuffer", func(c sobek.FunctionCall) sobek.Value { return rt.memoryOf(c.Argument(0)).buffer() })
	set("memoryGrow", rt.jsMemoryGrow)
	set("tableGet", rt.jsTableGet)
	set("tableSet", rt.jsTableSet)
	set("tableGrow", rt.jsTableGrow)
	set("tableLength", func(c sobek.FunctionCall) sobek.Value { return vm.ToValue(rt.tableOf(c.Argument(0)).length()) })
	set("globalGet", func(c sobek.FunctionCall) sobek.Value { return rt.globalOf(c.Argument(0)).get(vm) })
	set("globalSet", func(c sobek.FunctionCall) sobek.Value {
		rt.globalOf(c.Argument(0)).set(rt, c.Argument(1))
		return sobek.Undefined()
	})
	if !opts.Restricted {
		set("wasi", func(sobek.FunctionCall) sobek.Value { return rt.marker(&wasiMarker{}) })
		set("native", rt.jsNative)
	}
	fn, err := vm.RunScript("og-wasm.js", shimSource)
	if err != nil {
		return nil, err
	}
	install, _ := sobek.AssertFunction(fn)
	if _, err := install(sobek.Undefined(), native, rt.handle); err != nil {
		return nil, err
	}
	return rt, nil
}

// wz returns the wazero runtime, creating it on first use: an engine that
// never touches WebAssembly (most request VMs) pays nothing for it.
func (rt *Runtime) wz() wazero.Runtime {
	rt.rOnce.Do(func() { rt.r = wazero.NewRuntimeWithConfig(rt.ctx, rt.cfg) })
	return rt.r
}

// Close stops wasm threads (cancelling their context makes them exit at
// their next check) and releases every module. wazero must not close
// modules under running threads; threads that do not stop in time (e.g.
// parked in memory.atomic.wait) keep the runtime alive instead.
func (rt *Runtime) Close() {
	rt.cancel()
	done := make(chan struct{})
	go func() {
		rt.threads.Wait()
		close(done)
	}()
	deadline := time.After(threadStopTimeout)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			if rt.r != nil {
				_ = rt.r.Close(context.Background())
			}
			return
		case <-deadline:
			closeTimeouts.Add(1)
			return
		case <-tick.C:
			// Threads parked in memory.atomic.wait never see the
			// cancellation; wake them so they reach a check and exit.
			for mem := range rt.memories {
				wakeWaiters(mem)
			}
		}
	}
}

// closeTimeouts counts Close calls that gave up waiting for threads.
var closeTimeouts atomic.Int64

// CloseTimeouts reports how many Close calls gave up waiting for wasm
// threads to stop (and so kept their runtime alive).
func CloseTimeouts() int64 { return closeTimeouts.Load() }

// wakeWaiters notifies every address threads wait on in mem. wazero has
// no API for this: the waiters live in an unexported sync.Map keyed by
// address on its memory instance (wazero v1.12), read here by reflection.
// If that changes, Close falls back to its timeout.
func wakeWaiters(mem api.Memory) {
	n, ok := mem.(interface {
		Notify(offset, count uint32) uint32
	})
	if !ok {
		return
	}
	v := reflect.ValueOf(mem)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return
	}
	f := v.Elem().FieldByName("waiters")
	if !f.IsValid() || f.Type() != reflect.TypeFor[sync.Map]() {
		return
	}
	waiters := (*sync.Map)(unsafe.Pointer(f.UnsafeAddr()))
	waiters.Range(func(k, _ any) bool {
		if offset, ok := k.(uint32); ok {
			n.Notify(offset, math.MaxUint32)
		}
		return true
	})
}

const threadStopTimeout = 2 * time.Second

// --- handles ------------------------------------------------------------------

func (rt *Runtime) throw(ctor sobek.Value, msg string) {
	if c, ok := sobek.AssertConstructor(ctor); ok {
		if obj, err := c(nil, rt.vm.ToValue(msg)); err == nil {
			panic(obj)
		}
	}
	panic(rt.vm.NewGoError(errors.New(msg)))
}

func (rt *Runtime) handleOf(v sobek.Value) any {
	if obj, ok := v.(*sobek.Object); ok {
		if h := obj.GetSymbol(rt.handle); h != nil {
			return h.Export()
		}
	}
	return nil
}

// marker is an object carrying a native value under the handle symbol.
func (rt *Runtime) marker(v any) sobek.Value {
	obj := rt.vm.NewObject()
	_ = obj.SetSymbol(rt.handle, rt.vm.ToValue(v))
	return obj
}

func bytesOf(v sobek.Value) ([]byte, bool) {
	switch b := v.Export().(type) {
	case sobek.ArrayBuffer:
		return b.Bytes(), true
	case []byte:
		return b, true
	}
	return nil, false
}

// --- Module -------------------------------------------------------------------

type module struct {
	compiled wazero.CompiledModule
	info     *moduleInfo
}

func (rt *Runtime) jsCompile(c sobek.FunctionCall) sobek.Value {
	b, ok := bytesOf(c.Argument(0))
	if !ok {
		panic(rt.vm.NewTypeError("WebAssembly.Module: argument must be a buffer source"))
	}
	info, err := parseModule(b)
	if err != nil {
		rt.throw(rt.errCompile, err.Error())
	}
	patched, err := withSlotExports(b, info)
	if err != nil {
		rt.throw(rt.errCompile, err.Error())
	}
	if patched, err = withTableAccessors(patched, info); err != nil {
		rt.throw(rt.errCompile, err.Error())
	}
	compiled, err := rt.wz().CompileModule(rt.ctx, patched)
	if err != nil {
		rt.throw(rt.errCompile, err.Error())
	}
	return rt.vm.ToValue(&module{compiled: compiled, info: info})
}

var kindNames = map[byte]string{kindFunc: "function", kindTable: "table", kindMemory: "memory", kindGlobal: "global"}

func (rt *Runtime) jsModuleImports(c sobek.FunctionCall) sobek.Value {
	m := c.Argument(0).Export().(*module)
	out := make([]any, 0, len(m.info.imports))
	for _, im := range m.info.imports {
		e := map[string]any{"module": im.module, "name": im.name, "kind": kindNames[im.kind]}
		if im.kind == kindMemory { // non-standard: what a matching Memory needs
			e["minimum"], e["shared"] = im.mem.min, im.mem.shared
			if im.mem.hasMax {
				e["maximum"] = im.mem.max
			}
		}
		out = append(out, e)
	}
	return rt.vm.ToValue(out)
}

func (rt *Runtime) jsModuleExports(c sobek.FunctionCall) sobek.Value {
	m := c.Argument(0).Export().(*module)
	var out []any
	for name, fn := range m.compiled.ExportedFunctions() {
		_ = fn
		if !isSlotExport(name) {
			out = append(out, map[string]any{"name": name, "kind": "function"})
		}
	}
	for name := range m.compiled.ExportedMemories() {
		out = append(out, map[string]any{"name": name, "kind": "memory"})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].(map[string]any)["name"].(string) < out[j].(map[string]any)["name"].(string)
	})
	return rt.vm.ToValue(out)
}

// isSlotExport reports the exports Oganesson adds (slot functions, table
// accessors), which the instance's exports do not show.
func isSlotExport(name string) bool {
	return strings.HasPrefix(name, slotExportPrefix) || strings.HasPrefix(name, "__og_tbl_")
}

// --- Instance -----------------------------------------------------------------

type instance struct {
	rt       *Runtime
	module   *module
	mod      api.Module
	config   wazero.ModuleConfig
	resolved map[string]api.Module
	slots    map[uint32]sobek.Value
}

func (in *instance) resolve(name string) api.Module { return in.resolved[name] }

type threadKey struct{}

func isThread(ctx context.Context) bool { return ctx.Value(threadKey{}) != nil }

// jsInstantiate(moduleHandle, importObject, wrapMemory, wrapTable, wrapGlobal) -> exports
func (rt *Runtime) jsInstantiate(c sobek.FunctionCall) sobek.Value {
	m, _ := c.Argument(0).Export().(*module)
	if m == nil {
		panic(rt.vm.NewTypeError("WebAssembly.Instance: argument must be a WebAssembly.Module"))
	}
	importObj, _ := c.Argument(1).(*sobek.Object)
	wrapMemory, _ := sobek.AssertFunction(c.Argument(2))
	wrapTable, _ := sobek.AssertFunction(c.Argument(3))
	wrapGlobal, _ := sobek.AssertFunction(c.Argument(4))

	in := &instance{rt: rt, module: m, resolved: map[string]api.Module{}, slots: map[uint32]sobek.Value{}}
	// Group imports by module name, in order.
	var names []string
	groups := map[string][]importEntry{}
	for _, im := range m.info.imports {
		if _, ok := groups[im.module]; !ok {
			names = append(names, im.module)
		}
		groups[im.module] = append(groups[im.module], im)
	}
	for _, name := range names {
		var ns *sobek.Object
		if importObj != nil {
			ns, _ = importObj.Get(name).(*sobek.Object)
		}
		if ns == nil {
			rt.throw(rt.errLink, fmt.Sprintf("WebAssembly.Instance: import namespace %q is missing", name))
		}
		if _, ok := rt.handleOf(ns).(*wasiMarker); ok {
			if name != wasi_snapshot_preview1.ModuleName {
				rt.throw(rt.errLink, fmt.Sprintf("WebAssembly.Instance: WASI preview1 must be imported as %q, not %q", wasi_snapshot_preview1.ModuleName, name))
			}
			// A host module: resolved by its registered name (the import
			// resolver only takes wasm module instances).
			if err := rt.instantiateWASI(); err != nil {
				rt.throw(rt.errLink, err.Error())
			}
			continue
		}
		glue, err := rt.glue(in, name, ns, groups[name])
		if err != nil {
			rt.throw(rt.errLink, err.Error())
		}
		in.resolved[name] = glue
	}

	cfg := wazero.NewModuleConfig().
		WithName(fmt.Sprintf("og.instance.%d", rt.seq.Add(1))).
		WithStartFunctions().
		WithFSConfig(wazero.NewFSConfig().WithReadOnlyDirMount("/", "/")).
		WithSysWalltime().WithSysNanotime().WithSysNanosleep().
		WithRandSource(rand.Reader).
		WithStdout(rt.opts.Stderr).WithStderr(rt.opts.Stderr)
	keys := make([]string, 0, len(rt.opts.Env))
	for k := range rt.opts.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cfg = cfg.WithEnv(k, rt.opts.Env[k])
	}
	in.config = cfg
	rt.pending = append(rt.pending, nil)
	mod, err := rt.wz().InstantiateModule(experimental.WithImportResolver(rt.ctx, in.resolve), m.compiled, cfg)
	rt.finishCall(err)
	in.mod = mod

	exports := rt.vm.NewObject()
	for name := range m.compiled.ExportedFunctions() {
		if !isSlotExport(name) {
			_ = exports.Set(name, rt.wrapFunc(mod.ExportedFunction(name), name))
		}
	}
	for name := range m.compiled.ExportedMemories() {
		_ = exports.Set(name, rt.memoryObject(mod.ExportedMemory(name), mod.Name(), name, wrapMemory))
	}
	for name, exported := range m.info.exports {
		switch exported {
		case kindTable:
			ti := m.info.exportIdx[name]
			var elem byte = refFunc
			if int(ti) < len(m.info.tables) {
				elem = m.info.tables[ti]
			}
			v, _ := wrapTable(sobek.Undefined(), rt.vm.ToValue(&table{in: in, idx: ti, elem: elem}))
			_ = exports.Set(name, v)
		case kindGlobal:
			if g := mod.ExportedGlobal(name); g != nil && wrapGlobal != nil {
				v, _ := wrapGlobal(sobek.Undefined(), rt.vm.ToValue(&global{g: g}))
				_ = exports.Set(name, v)
			}
		}
	}
	return exports
}

// finishCall pops the pending-exception slot of a call into wasm and turns
// a failure into the JS exception that caused it or a RuntimeError.
func (rt *Runtime) finishCall(err error) {
	exc := rt.pending[len(rt.pending)-1]
	rt.pending = rt.pending[:len(rt.pending)-1]
	rt.refreshMemories()
	if exc != nil {
		panic(exc)
	}
	if err != nil {
		rt.throw(rt.errRuntime, err.Error())
	}
}

// glue instantiates the module that serves one import namespace: JS
// functions through a host module, memories from their providers.
func (rt *Runtime) glue(in *instance, name string, ns *sobek.Object, entries []importEntry) (api.Module, error) {
	id := rt.seq.Add(1)
	hostName := fmt.Sprintf("og.host.%d", id)
	host := rt.wz().NewHostModuleBuilder(hostName)
	var items []glueImport
	for _, im := range entries {
		v := ns.Get(im.name)
		switch im.kind {
		case kindFunc:
			t := in.module.info.types[im.typeIdx]
			fn, err := rt.bindImport(in, name+"."+im.name, v, t)
			if err != nil {
				return nil, err
			}
			host.NewFunctionBuilder().WithGoModuleFunction(fn, valueTypes(t.params), valueTypes(t.results)).Export(im.name)
			items = append(items, glueImport{fromModule: hostName, fromName: im.name, exportAs: im.name, kind: kindFunc, typ: t})
		case kindMemory:
			mem, _ := rt.handleOf(v).(*memory)
			if mem == nil {
				return nil, fmt.Errorf("WebAssembly.Instance: import %s.%s must be a WebAssembly.Memory", name, im.name)
			}
			items = append(items, glueImport{fromModule: mem.module, fromName: mem.export, exportAs: im.name, kind: kindMemory, mem: im.mem})
		default:
			return nil, fmt.Errorf("WebAssembly.Instance: import %s.%s: %s imports are not supported", name, im.name, kindNames[im.kind])
		}
	}
	if _, err := host.Instantiate(rt.ctx); err != nil {
		return nil, err
	}
	compiled, err := rt.wz().CompileModule(rt.ctx, glueModule(items))
	if err != nil {
		return nil, err
	}
	return rt.wz().InstantiateModule(rt.ctx, compiled, wazero.NewModuleConfig().WithName(fmt.Sprintf("og.glue.%d", id)).WithStartFunctions())
}

func valueTypes(b []byte) []api.ValueType {
	out := make([]api.ValueType, len(b))
	for i, t := range b {
		out[i] = api.ValueType(t)
	}
	return out
}

// bindImport turns an import value into a host function.
func (rt *Runtime) bindImport(in *instance, what string, v sobek.Value, t funcType) (api.GoModuleFunction, error) {
	switch h := rt.handleOf(v).(type) {
	case *nativeFunc:
		return rt.nativeImpl(in, h.name)
	case *exportedFunc:
		fn := h.fn
		return api.GoModuleFunc(func(ctx context.Context, _ api.Module, stack []uint64) {
			res, err := fn.Call(ctx, stack[:len(t.params)]...)
			if err != nil {
				panic(err)
			}
			copy(stack, res)
		}), nil
	}
	call, ok := sobek.AssertFunction(v)
	if !ok {
		return nil, fmt.Errorf("WebAssembly.Instance: import %s must be callable", what)
	}
	return api.GoModuleFunc(func(ctx context.Context, _ api.Module, stack []uint64) {
		if isThread(ctx) {
			panic(fmt.Errorf("wasm: JS import %s called from a wasm thread", what))
		}
		rt.refreshMemories()
		args := make([]sobek.Value, len(t.params))
		for i, p := range t.params {
			args[i] = rt.fromWasm(p, stack[i])
		}
		res, err := call(sobek.Undefined(), args...)
		if err != nil {
			// Unwind the wasm stack; the caller rethrows the JS exception.
			var exc *sobek.Exception
			if errors.As(err, &exc) && len(rt.pending) > 0 {
				rt.pending[len(rt.pending)-1] = exc
			}
			panic(err)
		}
		switch len(t.results) {
		case 0:
		case 1:
			stack[0] = rt.toWasm(t.results[0], res)
		default:
			arr, _ := res.(*sobek.Object)
			for i, r := range t.results {
				if arr != nil {
					stack[i] = rt.toWasm(r, arr.Get(fmt.Sprint(i)))
				}
			}
		}
	}), nil
}

// exportedFunc marks a JS wrapper of a wasm export (passed as an import
// elsewhere, it is linked directly).
type exportedFunc struct{ fn api.Function }

func (rt *Runtime) wrapFunc(fn api.Function, name string) sobek.Value {
	def := fn.Definition()
	params, results := def.ParamTypes(), def.ResultTypes()
	f := rt.vm.ToValue(func(c sobek.FunctionCall) sobek.Value {
		args := make([]uint64, len(params))
		for i, p := range params {
			args[i] = rt.toWasm(byte(p), c.Argument(i))
		}
		rt.pending = append(rt.pending, nil)
		res, err := fn.Call(rt.ctx, args...)
		rt.finishCall(err)
		switch len(results) {
		case 0:
			return sobek.Undefined()
		case 1:
			return rt.fromWasm(byte(results[0]), res[0])
		}
		out := make([]any, len(results))
		for i, r := range results {
			out[i] = rt.fromWasm(byte(r), res[i])
		}
		return rt.vm.ToValue(out)
	}).(*sobek.Object)
	_ = f.DefineDataProperty("name", rt.vm.ToValue(name), sobek.FLAG_FALSE, sobek.FLAG_FALSE, sobek.FLAG_TRUE)
	_ = f.DefineDataProperty("length", rt.vm.ToValue(len(params)), sobek.FLAG_FALSE, sobek.FLAG_FALSE, sobek.FLAG_TRUE)
	_ = f.SetSymbol(rt.handle, rt.vm.ToValue(&exportedFunc{fn: fn}))
	return f
}

func (rt *Runtime) toWasm(t byte, v sobek.Value) uint64 {
	switch api.ValueType(t) {
	case api.ValueTypeI32:
		return uint64(uint32(int32(v.ToInteger())))
	case api.ValueTypeI64:
		switch x := v.Export().(type) {
		case *big.Int:
			return uint64(x.Int64())
		default:
			return uint64(v.ToInteger())
		}
	case api.ValueTypeF32:
		return api.EncodeF32(float32(v.ToFloat()))
	case api.ValueTypeF64:
		return api.EncodeF64(v.ToFloat())
	case api.ValueTypeExternref:
		return rt.externref(v)
	}
	panic(rt.vm.NewTypeError(fmt.Sprintf("WebAssembly: unsupported value type %#x", t)))
}

func (rt *Runtime) fromWasm(t byte, x uint64) sobek.Value {
	switch api.ValueType(t) {
	case api.ValueTypeI32:
		return rt.vm.ToValue(int32(uint32(x)))
	case api.ValueTypeI64:
		return rt.vm.ToValue(big.NewInt(int64(x)))
	case api.ValueTypeF32:
		return rt.vm.ToValue(float64(api.DecodeF32(x)))
	case api.ValueTypeF64:
		return rt.vm.ToValue(api.DecodeF64(x))
	case api.ValueTypeExternref:
		if x == 0 {
			return sobek.Null()
		}
		if v, ok := rt.refs[x]; ok {
			return v
		}
		return sobek.Undefined()
	}
	return sobek.Undefined()
}

// externref is the handle of a JS value passed into wasm as externref:
// null is the null reference; anything else (undefined included) gets a
// handle that maps back to the same value. Handles are kept for the
// runtime's life (no finalization).
func (rt *Runtime) externref(v sobek.Value) uint64 {
	if v == nil || sobek.IsNull(v) {
		return 0
	}
	if rt.refs == nil {
		rt.refs = map[uint64]sobek.Value{}
	}
	rt.nextRef++
	rt.refs[rt.nextRef] = v
	return rt.nextRef
}

// --- Memory -------------------------------------------------------------------

type memory struct {
	rt             *Runtime
	mem            api.Memory
	module, export string // where wasm modules import it from
	obj            *sobek.Object
	ab             *sobek.ArrayBuffer
	abLen          uint32
}

// jsNewMemory(initial, maximum, shared, obj) -> handle
func (rt *Runtime) jsNewMemory(c sobek.FunctionCall) sobek.Value {
	l := limits{min: uint32(c.Argument(0).ToInteger()), shared: c.Argument(2).ToBoolean()}
	if mx := c.Argument(1); !sobek.IsUndefined(mx) {
		l.max, l.hasMax = uint32(mx.ToInteger()), true
	}
	if l.shared && !l.hasMax {
		panic(rt.vm.NewTypeError("WebAssembly.Memory: shared memory requires a maximum"))
	}
	name := fmt.Sprintf("og.memory.%d", rt.seq.Add(1))
	compiled, err := rt.wz().CompileModule(rt.ctx, memoryModule(l))
	if err != nil {
		panic(rt.vm.NewTypeError("WebAssembly.Memory: " + err.Error()))
	}
	mod, err := rt.wz().InstantiateModule(rt.ctx, compiled, wazero.NewModuleConfig().WithName(name).WithStartFunctions())
	if err != nil {
		panic(rt.vm.NewGoError(err))
	}
	m := &memory{rt: rt, mem: mod.ExportedMemory("memory"), module: name, export: "memory", obj: c.Argument(3).(*sobek.Object)}
	rt.memories[m.mem] = m
	return rt.vm.ToValue(m)
}

// memoryObject returns the JS object for an exported memory (the same
// object for the same memory, e.g. a re-exported import).
func (rt *Runtime) memoryObject(mem api.Memory, module, export string, wrap sobek.Callable) sobek.Value {
	if m, ok := rt.memories[mem]; ok {
		return m.obj
	}
	m := &memory{rt: rt, mem: mem, module: module, export: export}
	rt.memories[mem] = m
	v, err := wrap(sobek.Undefined(), rt.vm.ToValue(m))
	if err != nil {
		panic(err)
	}
	m.obj = v.(*sobek.Object)
	return v
}

func (rt *Runtime) memoryOf(v sobek.Value) *memory {
	m, ok := v.Export().(*memory)
	if !ok {
		panic(rt.vm.NewTypeError("not a WebAssembly.Memory"))
	}
	return m
}

func (m *memory) buffer() sobek.Value {
	size := m.mem.Size()
	if m.ab != nil && m.abLen == size {
		return m.rt.vm.ToValue(*m.ab)
	}
	m.detach()
	view, _ := m.mem.Read(0, size)
	ab := m.rt.vm.NewArrayBuffer(view)
	m.ab, m.abLen = &ab, size
	return m.rt.vm.ToValue(ab)
}

func (m *memory) detach() {
	if m.ab != nil {
		m.ab.Detach()
		m.ab = nil
	}
}

func (rt *Runtime) jsMemoryGrow(c sobek.FunctionCall) sobek.Value {
	m := rt.memoryOf(c.Argument(0))
	prev, ok := m.mem.Grow(uint32(c.Argument(1).ToInteger()))
	if !ok {
		panic(rt.vm.NewGoError(errors.New("WebAssembly.Memory.grow: failed to grow memory")))
	}
	m.detach()
	return rt.vm.ToValue(prev)
}

// refreshMemories detaches the ArrayBuffers of memories that grew, as
// browsers do; JS glue re-reads memory.buffer when it finds its views
// detached (byteLength 0).
func (rt *Runtime) refreshMemories() {
	for _, m := range rt.memories {
		if m.ab != nil && m.mem.Size() != m.abLen {
			m.detach()
		}
	}
}

// --- Table --------------------------------------------------------------------

// global is an exported WebAssembly.Global.
type global struct{ g api.Global }

func (rt *Runtime) globalOf(v sobek.Value) *global {
	g, ok := v.Export().(*global)
	if !ok {
		panic(rt.vm.NewTypeError("not a WebAssembly.Global"))
	}
	return g
}

// get converts the value by type: i32 and f32/f64 as numbers, i64 as a
// BigInt (the JS API's ToJSValue).
func (g *global) get(vm *sobek.Runtime) sobek.Value {
	v := g.g.Get()
	switch g.g.Type() {
	case api.ValueTypeI32:
		return vm.ToValue(int32(uint32(v)))
	case api.ValueTypeI64:
		return vm.ToValue(new(big.Int).SetInt64(int64(v)))
	case api.ValueTypeF32:
		return vm.ToValue(float64(math.Float32frombits(uint32(v))))
	case api.ValueTypeF64:
		return vm.ToValue(math.Float64frombits(v))
	}
	return sobek.Undefined()
}

func (g *global) set(rt *Runtime, v sobek.Value) {
	mg, ok := g.g.(api.MutableGlobal)
	if !ok {
		panic(rt.vm.NewTypeError("WebAssembly.Global: can't set the value of an immutable global"))
	}
	switch g.g.Type() {
	case api.ValueTypeI32:
		mg.Set(uint64(uint32(int32(v.ToInteger()))))
	case api.ValueTypeI64:
		b, ok := v.Export().(*big.Int)
		if !ok {
			panic(rt.vm.NewTypeError("WebAssembly.Global: an i64 value must be a BigInt"))
		}
		mg.Set(uint64(b.Int64()))
	case api.ValueTypeF32:
		mg.Set(uint64(math.Float32bits(float32(v.ToFloat()))))
	case api.ValueTypeF64:
		mg.Set(math.Float64bits(v.ToFloat()))
	}
}

// table is an exported table: its index and element type (funcref or
// externref).
type table struct {
	in   *instance
	idx  uint32
	elem byte
}

// accessor is the function withTableAccessors added for op on this table,
// or nil.
func (t *table) accessor(op string) api.Function {
	return t.in.mod.ExportedFunction(tableAccessor(op, t.idx))
}

func (rt *Runtime) tableOf(v sobek.Value) *table {
	t, ok := v.Export().(*table)
	if !ok {
		panic(rt.vm.NewTypeError("not a WebAssembly.Table"))
	}
	return t
}

func (t *table) length() uint32 {
	if f := t.accessor("size"); f != nil {
		if res, err := f.Call(t.in.rt.ctx); err == nil && len(res) == 1 {
			return uint32(res[0])
		}
	}
	var n uint32
	for slot := range t.in.module.info.slots {
		if slot+1 > n {
			n = slot + 1
		}
	}
	return n
}

func (rt *Runtime) jsTableGet(c sobek.FunctionCall) sobek.Value {
	t := rt.tableOf(c.Argument(0))
	slot := uint32(c.Argument(1).ToInteger())
	if t.elem == refExtern {
		return rt.fromWasm(refExtern, rt.tableCall(t, "get", uint64(slot))[0])
	}
	if v, ok := t.in.slots[slot]; ok {
		return v
	}
	fn, ok := t.in.module.info.slots[slot]
	if !ok {
		return sobek.Null()
	}
	f := t.in.mod.ExportedFunction(slotExport(fn))
	if f == nil {
		return sobek.Null()
	}
	v := rt.wrapFunc(f, fmt.Sprint(fn))
	t.in.slots[slot] = v
	return v
}

// jsTableSet(table, index, value): an externref table's slot.
func (rt *Runtime) jsTableSet(c sobek.FunctionCall) sobek.Value {
	t := rt.tableOf(c.Argument(0))
	if t.elem != refExtern {
		panic(rt.vm.NewTypeError("WebAssembly.Table.set: only externref tables can be set"))
	}
	rt.tableCall(t, "set", uint64(uint32(c.Argument(1).ToInteger())), rt.externref(c.Argument(2)))
	return sobek.Undefined()
}

// jsTableGrow(table, delta, init): grows the table; the previous length.
func (rt *Runtime) jsTableGrow(c sobek.FunctionCall) sobek.Value {
	t := rt.tableOf(c.Argument(0))
	delta := uint32(c.Argument(1).ToInteger())
	old := int32(uint32(rt.tableCall(t, "grow", uint64(delta))[0]))
	if old < 0 {
		rt.throw(rt.vm.Get("RangeError"), "WebAssembly.Table.grow(): failed to grow table")
	}
	// An externref table's new slots hold init, undefined when omitted
	// (the JS API's default value for externref; the instruction fills
	// them with null).
	if t.elem == refExtern {
		init := c.Argument(2)
		for i := uint32(0); i < delta; i++ {
			rt.tableCall(t, "set", uint64(uint32(old)+i), rt.externref(init))
		}
	}
	return rt.vm.ToValue(old)
}

// tableCall calls the accessor op of table t.
func (rt *Runtime) tableCall(t *table, op string, args ...uint64) []uint64 {
	f := t.accessor(op)
	if f == nil {
		panic(rt.vm.NewTypeError("WebAssembly.Table: " + op + " is not supported for this table"))
	}
	res, err := f.Call(rt.ctx, args...)
	if err != nil {
		panic(rt.vm.NewGoError(err))
	}
	return res
}

// --- native imports -------------------------------------------------------------

type wasiMarker struct{}

type nativeFunc struct{ name string }

var nativeSets = map[string][]string{
	"emnapi": append([]string{
		"_emnapi_async_worker", "_emnapi_get_now", "_emnapi_is_main_browser_thread",
		"_emnapi_is_main_runtime_thread", "_emnapi_runtime_keepalive_push",
		"_emnapi_runtime_keepalive_pop", "_emnapi_unwind", "_emnapi_worker_ref", "_emnapi_worker_unref",
	}, emnapiTrapped...),
	"wasi-threads": {"thread-spawn"},
}

// emnapiTrapped are emnapi's runtime hooks for the Node-API it compiles
// into a module (threadsafe functions, async work, callbacks into the
// module). og has no emnapi runtime. The first group has a meaning without
// one, for a module that starts no threads: a threadsafe function can be
// created (napi-rs creates one at registration for its custom GC), and
// nothing ever calls it from another thread. The rest only run when one is
// called or finalized, and trap.
var emnapiTrapped = []string{
	"_emnapi_env_check_gc_access", "napi_set_last_error", "_emnapi_node_emit_async_init",
	"_emnapi_node_emit_async_destroy", "_emnapi_env_ref", "_emnapi_env_unref",
	"_emnapi_ctx_increase_waiting_request_counter", "_emnapi_ctx_decrease_waiting_request_counter",

	"_emnapi_open_handle_scope", "_emnapi_close_handle_scope", "_emnapi_create_function",
	"_emnapi_node_make_callback", "_emnapi_call_finalizer", "_emnapi_callback_into_module",
	"_emnapi_async_send_js", "_emnapi_set_immediate",
}

func (rt *Runtime) jsNative(c sobek.FunctionCall) sobek.Value {
	set := c.Argument(0).String()
	names, ok := nativeSets[set]
	if !ok {
		panic(rt.vm.NewTypeError("og.wasm.native: unknown set " + set))
	}
	obj := rt.vm.NewObject()
	for _, n := range names {
		_ = obj.Set(n, rt.marker(&nativeFunc{name: n}))
	}
	return obj
}

func (rt *Runtime) instantiateWASI() error {
	rt.wasiOnce.Do(func() {
		_, rt.wasiErr = wasi_snapshot_preview1.Instantiate(rt.ctx, rt.wz())
	})
	return rt.wasiErr
}

func (rt *Runtime) nativeImpl(in *instance, name string) (api.GoModuleFunction, error) {
	i32 := func(v bool) uint64 {
		if v {
			return 1
		}
		return 0
	}
	switch name {
	case "_emnapi_get_now":
		return api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
			stack[0] = api.EncodeF64(float64(time.Since(rt.start).Nanoseconds()) / 1e6)
		}), nil
	case "_emnapi_is_main_browser_thread":
		return api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) { stack[0] = 0 }), nil
	case "_emnapi_is_main_runtime_thread":
		return api.GoModuleFunc(func(ctx context.Context, _ api.Module, stack []uint64) { stack[0] = i32(!isThread(ctx)) }), nil
	case "_emnapi_runtime_keepalive_push", "_emnapi_runtime_keepalive_pop", "_emnapi_unwind", "_emnapi_worker_ref", "_emnapi_worker_unref":
		return api.GoModuleFunc(func(context.Context, api.Module, []uint64) {}), nil
	// Asserts the env is not in a finalizer (og runs none), keeps the event
	// loop alive (og's build host waits for its own work), or reports to
	// Node's async_hooks binding (absent: emnapi_is_node_binding_available
	// is 0).
	case "_emnapi_env_check_gc_access", "_emnapi_env_ref", "_emnapi_env_unref",
		"_emnapi_ctx_increase_waiting_request_counter", "_emnapi_ctx_decrease_waiting_request_counter",
		"_emnapi_node_emit_async_init", "_emnapi_node_emit_async_destroy":
		return api.GoModuleFunc(func(context.Context, api.Module, []uint64) {}), nil
	// napi_set_last_error(env, status, engine_code, engine_reserved) returns
	// status (the C functions return through it).
	case "napi_set_last_error":
		return api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) { stack[0] = stack[1] }), nil
	case "_emnapi_async_worker":
		return api.GoModuleFunc(func(context.Context, api.Module, []uint64) {
			panic(errors.New("wasm: emnapi async work is not supported"))
		}), nil
	case "thread-spawn":
		return rt.threadSpawn(in), nil
	}
	if slices.Contains(emnapiTrapped, name) {
		return api.GoModuleFunc(func(context.Context, api.Module, []uint64) {
			panic(fmt.Errorf("wasm: emnapi's %s is not supported (no emnapi runtime)", name))
		}), nil
	}
	return nil, fmt.Errorf("wasm: no native import %q", name)
}

var nextTID atomic.Int32

// threadSpawn implements wasi-threads: a new instance of the same module,
// sharing its imports (and so its memory), runs wasi_thread_start on a
// goroutine.
func (rt *Runtime) threadSpawn(in *instance) api.GoModuleFunction {
	return api.GoModuleFunc(func(_ context.Context, _ api.Module, stack []uint64) {
		arg := uint32(stack[0])
		tid := nextTID.Add(1) & math.MaxInt32 // wasi-threads ids are positive
		ctx := experimental.WithImportResolver(context.WithValue(rt.ctx, threadKey{}, tid), in.resolve)
		cfg := in.config.WithName(fmt.Sprintf("og.thread.%d", tid))
		rt.threads.Add(1)
		go func() {
			defer rt.threads.Done()
			mod, err := rt.wz().InstantiateModule(ctx, in.module.compiled, cfg)
			if err != nil {
				if rt.ctx.Err() == nil {
					fmt.Fprintf(rt.opts.Stderr, "og: wasm thread %d: %v\n", tid, err)
				}
				return
			}
			defer mod.Close(context.Background())
			start := mod.ExportedFunction("wasi_thread_start")
			if start == nil {
				fmt.Fprintf(rt.opts.Stderr, "og: wasm thread %d: module lacks wasi_thread_start\n", tid)
				return
			}
			if _, err := start.Call(ctx, uint64(tid), uint64(arg)); err != nil && rt.ctx.Err() == nil {
				var exit interface{ ExitCode() uint32 }
				if !errors.As(err, &exit) {
					fmt.Fprintf(rt.opts.Stderr, "og: wasm thread %d: %v\n", tid, err)
				}
			}
		}()
		stack[0] = uint64(uint32(tid))
	})
}
