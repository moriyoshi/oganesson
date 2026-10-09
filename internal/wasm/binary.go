package wasm

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// A small reader and writer for the WebAssembly binary format: just enough
// to (1) find which function each table slot holds, so Table.get can call
// it through an export, and (2) generate the glue modules that wire
// JS-provided imports and memories into a guest.

const (
	secCustom   = 0
	secType     = 1
	secImport   = 2
	secFunction = 3
	secTable    = 4
	secMemory   = 5
	secExport   = 7
	secElement  = 9
	secCode     = 10

	kindFunc   = 0
	kindTable  = 1
	kindMemory = 2
	kindGlobal = 3
)

type funcType struct{ params, results []byte }

func (t funcType) key() string { return string(t.params) + "|" + string(t.results) }

type limits struct {
	min, max uint32
	hasMax   bool
	shared   bool
}

type importEntry struct {
	module, name string
	kind         byte
	typeIdx      uint32 // kindFunc
	mem          limits // kindMemory
}

// moduleInfo is what Oganesson needs to know about a guest module.
type moduleInfo struct {
	types     []funcType
	imports   []importEntry
	funcTypes []uint32          // type index of every function (imports first)
	exports   map[string]byte   // export name -> kind
	exportIdx map[string]uint32 // export name -> index in its kind's space
	slots     map[uint32]uint32 // table 0 slot -> function index
	tables    []byte            // element type of every table (imports first)
	imported  int               // imported functions
}

type reader struct {
	b   []byte
	pos int
	err error
}

func (r *reader) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf("wasm: "+format, args...)
	}
}

func (r *reader) byte() byte {
	if r.pos >= len(r.b) {
		r.fail("unexpected end")
		return 0
	}
	c := r.b[r.pos]
	r.pos++
	return c
}

func (r *reader) u32() uint32 {
	var v uint32
	for shift := uint(0); shift < 35; shift += 7 {
		c := r.byte()
		v |= uint32(c&0x7f) << shift
		if c&0x80 == 0 {
			return v
		}
	}
	r.fail("bad LEB128")
	return 0
}

func (r *reader) s64() int64 {
	var v int64
	var shift uint
	for {
		c := r.byte()
		v |= int64(c&0x7f) << shift
		shift += 7
		if c&0x80 == 0 {
			if shift < 64 && c&0x40 != 0 {
				v |= -1 << shift
			}
			return v
		}
		if shift >= 70 || r.err != nil {
			r.fail("bad LEB128")
			return 0
		}
	}
}

func (r *reader) bytes(n uint32) []byte {
	if r.pos+int(n) > len(r.b) {
		r.fail("unexpected end")
		return nil
	}
	s := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return s
}

func (r *reader) name() string { return string(r.bytes(r.u32())) }

func (r *reader) limits() limits {
	flags := r.byte()
	l := limits{min: r.u32()}
	if flags&1 != 0 {
		l.max, l.hasMax = r.u32(), true
	}
	l.shared = flags&2 != 0
	if flags&^3 != 0 {
		r.fail("unsupported limits flags %#x", flags)
	}
	return l
}

// constExpr reads a constant expression and returns its value when it is
// an i32.const or ref.func (ok is false for anything else).
func (r *reader) constExpr() (v int64, ok bool) {
	ok = true
	for {
		switch op := r.byte(); op {
		case 0x0b: // end
			return v, ok
		case 0x41, 0x42: // i32.const, i64.const
			v = r.s64()
		case 0xd2: // ref.func
			v = int64(r.u32())
		case 0xd0: // ref.null
			r.byte()
			ok = false
		case 0x23: // global.get
			r.u32()
			ok = false
		default:
			r.fail("unsupported constant expression opcode %#x", op)
			return 0, false
		}
		if r.err != nil {
			return 0, false
		}
	}
}

type section struct {
	id   byte
	body []byte
}

func splitSections(b []byte) ([]section, error) {
	if len(b) < 8 || !bytes.Equal(b[:4], []byte("\x00asm")) {
		return nil, errors.New("wasm: not a WebAssembly module")
	}
	if binary.LittleEndian.Uint32(b[4:8]) != 1 {
		return nil, errors.New("wasm: unsupported binary version")
	}
	r := &reader{b: b, pos: 8}
	var out []section
	for r.pos < len(b) && r.err == nil {
		id := r.byte()
		body := r.bytes(r.u32())
		out = append(out, section{id, body})
	}
	return out, r.err
}

// parseModule reads the parts of a module Oganesson needs.
func parseModule(b []byte) (*moduleInfo, error) {
	secs, err := splitSections(b)
	if err != nil {
		return nil, err
	}
	m := &moduleInfo{exports: map[string]byte{}, exportIdx: map[string]uint32{}, slots: map[uint32]uint32{}}
	for _, s := range secs {
		r := &reader{b: s.body}
		switch s.id {
		case secType:
			for n := r.u32(); n > 0 && r.err == nil; n-- {
				if form := r.byte(); form != 0x60 {
					r.fail("unsupported type form %#x", form)
					break
				}
				var t funcType
				t.params = append([]byte(nil), r.bytes(r.u32())...)
				t.results = append([]byte(nil), r.bytes(r.u32())...)
				m.types = append(m.types, t)
			}
		case secImport:
			for n := r.u32(); n > 0 && r.err == nil; n-- {
				e := importEntry{module: r.name(), name: r.name(), kind: r.byte()}
				switch e.kind {
				case kindFunc:
					e.typeIdx = r.u32()
					m.funcTypes = append(m.funcTypes, e.typeIdx)
					m.imported++
				case kindTable:
					m.tables = append(m.tables, r.byte())
					r.limits()
				case kindMemory:
					e.mem = r.limits()
				case kindGlobal:
					r.byte()
					r.byte()
				default:
					r.fail("unknown import kind %d", e.kind)
				}
				m.imports = append(m.imports, e)
			}
		case secFunction:
			for n := r.u32(); n > 0 && r.err == nil; n-- {
				m.funcTypes = append(m.funcTypes, r.u32())
			}
		case secTable:
			for n := r.u32(); n > 0 && r.err == nil; n-- {
				m.tables = append(m.tables, r.byte())
				r.limits()
			}
		case secExport:
			for n := r.u32(); n > 0 && r.err == nil; n-- {
				name := r.name()
				m.exports[name] = r.byte()
				m.exportIdx[name] = r.u32()
			}
		case secElement:
			parseElements(r, m)
		}
		if r.err != nil {
			return nil, r.err
		}
	}
	return m, nil
}

// parseElements records the function in each slot of table 0 that active
// element segments with constant offsets fill.
func parseElements(r *reader, m *moduleInfo) {
	for n := r.u32(); n > 0 && r.err == nil; n-- {
		flags := r.u32()
		table := uint32(0)
		active := flags&1 == 0
		if flags&2 != 0 && active {
			table = r.u32()
		}
		var offset int64
		offsetOK := false
		if active {
			offset, offsetOK = r.constExpr()
		}
		if flags&2 != 0 || !active {
			r.byte() // elemkind or reftype
		}
		count := r.u32()
		for i := uint32(0); i < count && r.err == nil; i++ {
			var fn int64
			ok := true
			if flags&4 != 0 {
				fn, ok = r.constExpr()
			} else {
				fn = int64(r.u32())
			}
			if active && offsetOK && ok && table == 0 {
				m.slots[uint32(offset)+i] = uint32(fn)
			}
		}
	}
}

// slotExportPrefix names the exports Oganesson adds for the functions in
// table slots.
const slotExportPrefix = "__og_fn_"

func slotExport(fn uint32) string { return fmt.Sprintf("%s%d", slotExportPrefix, fn) }

// withSlotExports returns the module with an export added for every
// function referenced from table 0, so Table.get can call them.
func withSlotExports(b []byte, m *moduleInfo) ([]byte, error) {
	secs, err := splitSections(b)
	if err != nil {
		return nil, err
	}
	fns := map[uint32]bool{}
	for _, fn := range m.slots {
		fns[fn] = true
	}
	if len(fns) == 0 {
		return b, nil
	}
	sorted := make([]uint32, 0, len(fns))
	for fn := range fns {
		sorted = append(sorted, fn)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var out bytes.Buffer
	out.Write(b[:8])
	wroteExports := false
	writeExports := func(old []byte) {
		var body bytes.Buffer
		count := uint32(0)
		var entries []byte
		if old != nil {
			r := &reader{b: old}
			count = r.u32()
			entries = old[r.pos:]
		}
		writeU32(&body, count+uint32(len(sorted)))
		body.Write(entries)
		for _, fn := range sorted {
			writeName(&body, slotExport(fn))
			body.WriteByte(kindFunc)
			writeU32(&body, fn)
		}
		writeSection(&out, secExport, body.Bytes())
		wroteExports = true
	}
	for _, s := range secs {
		switch {
		case s.id == secExport:
			writeExports(s.body)
			continue
		case !wroteExports && s.id >= 8 && s.id <= 12: // start, element, code, data, datacount follow exports
			writeExports(nil)
		}
		writeSection(&out, s.id, s.body)
	}
	if !wroteExports {
		writeExports(nil)
	}
	return out.Bytes(), nil
}

// Reference types of table elements.
const (
	refFunc   = 0x70
	refExtern = 0x6f
)

// tableAccessor names the export Oganesson adds to reach table ti: op is
// size, grow, get or set.
func tableAccessor(op string, ti uint32) string { return fmt.Sprintf("__og_tbl_%s_%d", op, ti) }

// withTableAccessors returns the module with functions added and exported
// for every exported table: size and grow (with ref.null), and for
// externref tables get and set. wazero gives no access to a module's
// tables, and WebAssembly.Table's methods (wasm-bindgen grows and fills its
// externref table at start) need it.
func withTableAccessors(b []byte, m *moduleInfo) ([]byte, error) {
	type added struct {
		name string
		typ  funcType
		code []byte
	}
	var fns []added
	var tables []uint32
	for name, kind := range m.exports {
		if kind == kindTable {
			tables = append(tables, m.exportIdx[name])
		}
	}
	if len(tables) == 0 {
		return b, nil
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i] < tables[j] })
	seen := map[uint32]bool{}
	for _, ti := range tables {
		if seen[ti] || int(ti) >= len(m.tables) {
			continue
		}
		seen[ti] = true
		et := m.tables[ti]
		var idx bytes.Buffer
		writeU32(&idx, ti)
		t := idx.Bytes()
		cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
		fns = append(fns,
			added{tableAccessor("size", ti), funcType{nil, []byte{0x7f}}, cat([]byte{0xfc, 0x10}, t)},
			added{tableAccessor("grow", ti), funcType{[]byte{0x7f}, []byte{0x7f}}, cat([]byte{0xd0, et, 0x20, 0x00, 0xfc, 0x0f}, t)},
		)
		if et == refExtern {
			fns = append(fns,
				added{tableAccessor("get", ti), funcType{[]byte{0x7f}, []byte{et}}, cat([]byte{0x20, 0x00, 0x25}, t)},
				added{tableAccessor("set", ti), funcType{[]byte{0x7f, et}, nil}, cat([]byte{0x20, 0x00, 0x20, 0x01, 0x26}, t)},
			)
		}
	}
	secs, err := splitSections(b)
	if err != nil {
		return nil, err
	}
	have := map[byte]bool{}
	for _, s := range secs {
		have[s.id] = true
	}
	if !have[secType] || !have[secFunction] || !have[secCode] || !have[secExport] {
		return b, nil
	}
	firstFn := uint32(len(m.funcTypes))
	firstType := uint32(len(m.types))
	// appendEntries rewrites a vector section with more entries.
	appendEntries := func(old []byte, extra [][]byte) []byte {
		r := &reader{b: old}
		n := r.u32()
		var body bytes.Buffer
		writeU32(&body, n+uint32(len(extra)))
		body.Write(old[r.pos:])
		for _, e := range extra {
			body.Write(e)
		}
		return body.Bytes()
	}
	var out bytes.Buffer
	out.Write(b[:8])
	for _, s := range secs {
		body := s.body
		var extra [][]byte
		for i, f := range fns {
			var e bytes.Buffer
			switch s.id {
			case secType:
				e.WriteByte(0x60)
				writeU32(&e, uint32(len(f.typ.params)))
				e.Write(f.typ.params)
				writeU32(&e, uint32(len(f.typ.results)))
				e.Write(f.typ.results)
			case secFunction:
				writeU32(&e, firstType+uint32(i))
			case secCode:
				var fb bytes.Buffer
				fb.WriteByte(0) // no locals
				fb.Write(f.code)
				fb.WriteByte(0x0b)
				writeU32(&e, uint32(fb.Len()))
				e.Write(fb.Bytes())
			case secExport:
				writeName(&e, f.name)
				e.WriteByte(kindFunc)
				writeU32(&e, firstFn+uint32(i))
			default:
				continue
			}
			extra = append(extra, e.Bytes())
		}
		if len(extra) > 0 {
			body = appendEntries(body, extra)
		}
		writeSection(&out, s.id, body)
	}
	return out.Bytes(), nil
}

// --- encoding -----------------------------------------------------------------

func writeU32(w *bytes.Buffer, v uint32) {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			c |= 0x80
		}
		w.WriteByte(c)
		if v == 0 {
			return
		}
	}
}

func writeName(w *bytes.Buffer, s string) {
	writeU32(w, uint32(len(s)))
	w.WriteString(s)
}

func writeSection(w *bytes.Buffer, id byte, body []byte) {
	w.WriteByte(id)
	writeU32(w, uint32(len(body)))
	w.Write(body)
}

func writeLimits(w *bytes.Buffer, l limits) {
	flags := byte(0)
	if l.hasMax {
		flags |= 1
	}
	if l.shared {
		flags |= 2
	}
	w.WriteByte(flags)
	writeU32(w, l.min)
	if l.hasMax {
		writeU32(w, l.max)
	}
}

// glueImport is one import a glue module takes and re-exports.
type glueImport struct {
	fromModule, fromName string
	exportAs             string
	kind                 byte
	typ                  funcType // kindFunc
	mem                  limits   // kindMemory
}

// glueModule encodes a module that imports the given items and exports
// each under its exportAs name.
func glueModule(items []glueImport) []byte {
	var types []funcType
	typeIdx := map[string]uint32{}
	for _, it := range items {
		if it.kind == kindFunc {
			if _, ok := typeIdx[it.typ.key()]; !ok {
				typeIdx[it.typ.key()] = uint32(len(types))
				types = append(types, it.typ)
			}
		}
	}
	var out bytes.Buffer
	out.Write([]byte("\x00asm\x01\x00\x00\x00"))

	var body bytes.Buffer
	writeU32(&body, uint32(len(types)))
	for _, t := range types {
		body.WriteByte(0x60)
		writeU32(&body, uint32(len(t.params)))
		body.Write(t.params)
		writeU32(&body, uint32(len(t.results)))
		body.Write(t.results)
	}
	writeSection(&out, secType, body.Bytes())

	body.Reset()
	writeU32(&body, uint32(len(items)))
	for _, it := range items {
		writeName(&body, it.fromModule)
		writeName(&body, it.fromName)
		body.WriteByte(it.kind)
		switch it.kind {
		case kindFunc:
			writeU32(&body, typeIdx[it.typ.key()])
		case kindMemory:
			writeLimits(&body, it.mem)
		}
	}
	writeSection(&out, secImport, body.Bytes())

	body.Reset()
	writeU32(&body, uint32(len(items)))
	var fn, mem uint32
	for _, it := range items {
		writeName(&body, it.exportAs)
		body.WriteByte(it.kind)
		switch it.kind {
		case kindFunc:
			writeU32(&body, fn)
			fn++
		case kindMemory:
			writeU32(&body, mem)
			mem++
		}
	}
	writeSection(&out, secExport, body.Bytes())
	return out.Bytes()
}

// memoryModule encodes a module that defines one memory and exports it as
// "memory" (the backing of a JS-created WebAssembly.Memory).
func memoryModule(l limits) []byte {
	var out, body bytes.Buffer
	out.Write([]byte("\x00asm\x01\x00\x00\x00"))
	writeU32(&body, 1)
	writeLimits(&body, l)
	writeSection(&out, secMemory, body.Bytes())
	body.Reset()
	writeU32(&body, 1)
	writeName(&body, "memory")
	body.WriteByte(kindMemory)
	writeU32(&body, 0)
	writeSection(&out, secExport, body.Bytes())
	return out.Bytes()
}
