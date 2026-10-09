package wasm

import (
	"bytes"
	"io"
	"testing"

	"github.com/grafana/sobek"
)

// testModule hand-assembles:
//
//	(import "env" "log" (func (param i32)))
//	(func $add (param i32 i32) (result i32) local.get 0 local.get 1 i32.add)
//	(func $callLog (param i32) local.get 0 i32.const 2 i32.mul call 0)
//	(func $grow (result i32) i32.const 1 memory.grow)
//	(table 2 funcref) (elem (i32.const 0) $add $callLog)
//	(memory 1 10) (data (i32.const 0) "hi")
//	exports: add, callLog, grow, memory, table
func testModule() []byte {
	var out bytes.Buffer
	out.WriteString("\x00asm\x01\x00\x00\x00")
	sec := func(id byte, body ...byte) { writeSection(&out, id, body) }
	sec(secType, 3,
		0x60, 2, 0x7f, 0x7f, 1, 0x7f, // 0: (i32 i32) -> i32
		0x60, 1, 0x7f, 0, // 1: (i32) -> ()
		0x60, 0, 1, 0x7f) // 2: () -> i32
	sec(secImport, 1, 3, 'e', 'n', 'v', 3, 'l', 'o', 'g', kindFunc, 1)
	sec(secFunction, 3, 0, 1, 2)
	sec(4, 1, 0x70, 0, 2)       // table: funcref, min 2
	sec(secMemory, 1, 1, 1, 10) // memory: min 1 max 10
	var exp bytes.Buffer
	writeU32(&exp, 5)
	for _, e := range []struct {
		name string
		kind byte
		idx  uint32
	}{{"add", kindFunc, 1}, {"callLog", kindFunc, 2}, {"grow", kindFunc, 3}, {"memory", kindMemory, 0}, {"table", kindTable, 0}} {
		writeName(&exp, e.name)
		exp.WriteByte(e.kind)
		writeU32(&exp, e.idx)
	}
	writeSection(&out, secExport, exp.Bytes())
	sec(secElement, 1, 0, 0x41, 0, 0x0b, 2, 1, 2)
	sec(10, 3,
		7, 0, 0x20, 0, 0x20, 1, 0x6a, 0x0b,
		9, 0, 0x20, 0, 0x41, 2, 0x6c, 0x10, 0, 0x0b,
		6, 0, 0x41, 1, 0x40, 0, 0x0b)
	sec(11, 1, 0, 0x41, 0, 0x0b, 2, 'h', 'i')
	return out.Bytes()
}

func newVM(t *testing.T) *sobek.Runtime {
	t.Helper()
	vm := sobek.New()
	rt, err := Install(vm, Options{Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	_ = vm.Set("wasmBytes", vm.NewArrayBuffer(testModule()))
	return vm
}

func TestWebAssembly(t *testing.T) {
	vm := newVM(t)
	v, err := vm.RunString(`
		const logged = [];
		const mod = new WebAssembly.Module(wasmBytes);
		const { exports } = new WebAssembly.Instance(mod, { env: { log: (x) => logged.push(x) } });
		const out = {};
		out.add = exports.add(2, 3);
		exports.callLog(21);
		out.logged = logged.join(",");
		out.table = exports.table.get(0)(40, 2) + ":" + typeof exports.table.get(1) + ":" + exports.table.get(5);
		const before = exports.memory.buffer;
		out.data = String.fromCharCode(...new Uint8Array(before, 0, 2));
		out.sameBuffer = before === exports.memory.buffer;
		out.grow = exports.grow();
		out.detached = before.byteLength;
		out.grown = exports.memory.buffer.byteLength;
		out.imports = WebAssembly.Module.imports(mod).map((i) => i.module + "." + i.name + ":" + i.kind).join();
		out.exports = WebAssembly.Module.exports(mod).map((e) => e.name).join();
		JSON.stringify(out);
	`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"add":5,"logged":"42","table":"42:function:null","data":"hi","sameBuffer":true,"grow":1,"detached":0,"grown":131072,"imports":"env.log:function","exports":"add,callLog,grow,memory"}`
	if got := v.String(); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// A JS exception thrown by an import unwinds the wasm stack and reaches
// the JS caller unchanged; missing imports are LinkErrors.
func TestWebAssemblyErrors(t *testing.T) {
	vm := newVM(t)
	v, err := vm.RunString(`
		const mod = new WebAssembly.Module(wasmBytes);
		class Boom extends Error {}
		const { exports } = new WebAssembly.Instance(mod, { env: { log() { throw new Boom("boom"); } } });
		const out = [];
		try { exports.callLog(1); } catch (e) { out.push(e instanceof Boom ? "Boom:" + e.message : "other:" + e); }
		out.push(exports.add(1, 1)); // still usable
		try { new WebAssembly.Instance(mod, {}); } catch (e) { out.push(e instanceof WebAssembly.LinkError); }
		try { new WebAssembly.Module(new Uint8Array([0, 1, 2])); } catch (e) { out.push(e instanceof WebAssembly.CompileError); }
		out.join();
	`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), "Boom:boom,2,true,true"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestRestricted: the production runtime's mode keeps the standard API but
// withholds WASI and the native imports, and creates no wazero runtime
// until WebAssembly is used.
func TestRestricted(t *testing.T) {
	vm := sobek.New()
	rt, err := Install(vm, Options{Stderr: io.Discard, Restricted: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	if rt.r != nil {
		t.Error("wazero runtime created before use")
	}
	_ = vm.Set("wasmBytes", vm.NewArrayBuffer(testModule()))
	v, err := vm.RunString(`[typeof globalThis[Symbol.for("og.wasm")], new WebAssembly.Instance(new WebAssembly.Module(wasmBytes), { env: { log() {} } }).exports.add(2, 3)].join(",")`)
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "undefined,5" {
		t.Errorf("got %s", v)
	}
	if rt.r == nil {
		t.Error("no wazero runtime after use")
	}
}

// Exported globals are WebAssembly.Global objects: i32 / f64 as numbers,
// i64 as BigInt; mutable ones can be set (es-module-lexer reads
// __heap_base.value).
func TestExportedGlobals(t *testing.T) {
	var out bytes.Buffer
	out.WriteString("\x00asm\x01\x00\x00\x00")
	writeSection(&out, 6, []byte{3,
		0x7f, 0x00, 0x41, 0xd2, 0x09, 0x0b, // i32 const 1234
		0x7e, 0x01, 0x42, 0x7b, 0x0b, // mut i64 -5
		0x7c, 0x00, 0x44, 0, 0, 0, 0, 0, 0, 0x04, 0x40, 0x0b, // f64 2.5
	})
	var exp bytes.Buffer
	writeU32(&exp, 3)
	for i, name := range []string{"base", "counter", "ratio"} {
		writeName(&exp, name)
		exp.WriteByte(kindGlobal)
		writeU32(&exp, uint32(i))
	}
	writeSection(&out, secExport, exp.Bytes())
	vm := sobek.New()
	rt, err := Install(vm, Options{Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rt.Close)
	_ = vm.Set("bytes", vm.NewArrayBuffer(out.Bytes()))
	v, err := vm.RunString(`
		const e = new WebAssembly.Instance(new WebAssembly.Module(bytes)).exports;
		const r = [e.base.value, e.base instanceof WebAssembly.Global, +e.base, typeof e.counter.value, String(e.counter.value), e.ratio.value];
		e.counter.value = 7n;
		r.push(String(e.counter.value));
		try { e.base.value = 1; r.push("set"); } catch (err) { r.push(err instanceof TypeError); }
		r.join(",");
	`)
	if err != nil {
		t.Fatal(err)
	}
	if want := "1234,true,1234,bigint,-5,2.5,7,true"; v.String() != want {
		t.Fatalf("got %s, want %s", v, want)
	}
}

// externrefModule hand-assembles:
//
//	(import "env" "echo" (func $echo (param externref) (result externref)))
//	(func $id (param externref) (result externref) local.get 0)
//	(func $put (param i32 externref) local.get 0 local.get 1 table.set 0)
//	(func $take (param i32) (result externref) local.get 0 table.get 0)
//	(func $viaImport (param externref) (result externref) local.get 0 call $echo)
//	(table 1 externref)
//	exports: id, put, take, viaImport, tbl
var externrefModule = []byte{0, 97, 115, 109, 1, 0, 0, 0, 1, 16, 3, 96, 1, 111, 1, 111, 96, 2, 127, 111, 0, 96, 1, 127, 1, 111, 2, 12, 1, 3, 101, 110, 118, 4, 101, 99, 104, 111, 0, 0, 3, 5, 4, 0, 1, 2, 0, 4, 4, 1, 111, 0, 1, 7, 37, 5, 2, 105, 100, 0, 1, 3, 112, 117, 116, 0, 2, 4, 116, 97, 107, 101, 0, 3, 9, 118, 105, 97, 73, 109, 112, 111, 114, 116, 0, 4, 3, 116, 98, 108, 1, 0, 10, 29, 4, 4, 0, 32, 0, 11, 8, 0, 32, 0, 32, 1, 38, 0, 11, 6, 0, 32, 0, 37, 0, 11, 6, 0, 32, 0, 16, 0, 11}

// TestExternref: externref crosses JS and wasm as the same JS value (null
// as the null reference), and an exported externref table grows, sets and
// gets from JS and from wasm (wasm-bindgen grows and fills its externref
// table at start: Qwik's optimizer). The expectation is Node 24's.
func TestExternref(t *testing.T) {
	vm := newVM(t)
	_ = vm.Set("refBytes", vm.NewArrayBuffer(externrefModule))
	v, err := vm.RunString(`const echoed = [];
const { exports: x } = new WebAssembly.Instance(new WebAssembly.Module(refBytes), { env: { echo: (v) => { echoed.push(typeof v); return v; } } });
const o = { a: 1 };
const out = [];
out.push(x.id(o) === o, x.id(null) === null, x.id(undefined) === undefined, x.id("s"));
out.push(x.tbl.length, x.tbl.grow(2), x.tbl.length, String(x.tbl.get(2)));
x.tbl.set(1, o);
out.push(x.take(1) === o, x.tbl.get(1) === o);
x.put(2, "p");
out.push(x.tbl.get(2), x.tbl.get(0));
out.push(x.tbl.grow(1, "init"), x.tbl.get(3));
out.push(x.viaImport(o) === o, echoed.join("/"));
out.push(Object.keys(x).sort().join("/"));
out.join(",");
`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := v.String(), "true,true,true,s,1,1,3,undefined,true,true,p,,3,init,true,object,id/put/take/tbl/viaImport"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
