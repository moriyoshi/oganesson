package kv

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"math/big"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// ModuleName is the import specifier of the store in JS.
const ModuleName = "og:kv"

//go:embed js/kv.js
var moduleSource string

// Types are TypeScript declarations for ModuleName.
//
//go:embed kv.d.ts
var Types string

// Plugin resolves ModuleName for esbuild. Server builds only: the module
// throws when no host store is installed.
func Plugin() api.Plugin {
	return api.Plugin{
		Name: "og-kv",
		Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: "^og:kv$"}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "kv", Namespace: "og-kv"}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "og-kv"}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
				src := moduleSource
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
			})
		},
	}
}

// Install exposes store to JS in e (as globalThis[Symbol.for("og.kv")]).
func Install(e *engine.Engine, store *Store) error {
	vm := e.VM
	h := &binding{vm: vm, store: store}
	o := vm.NewObject()
	_ = o.Set("get", h.get)
	_ = o.Set("list", h.list)
	_ = o.Set("commit", h.commit)
	install, err := vm.RunString(`(function (o) { Object.defineProperty(globalThis, Symbol.for("og.kv"), { value: o }); })`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), o)
	return err
}

type binding struct {
	vm    *sobek.Runtime
	store *Store
}

func (h *binding) throw(err error) {
	panic(h.vm.NewTypeError(err.Error()))
}

func (h *binding) keyFromJS(v sobek.Value) []KeyPart {
	arr, ok := v.Export().([]any)
	if !ok {
		h.throw(fmt.Errorf("kv: key must be an array"))
	}
	out := make([]KeyPart, len(arr))
	for i, p := range arr {
		switch x := p.(type) {
		case string, bool, *big.Int:
			out[i] = x
		case int64:
			out[i] = float64(x)
		case float64:
			out[i] = x
		case []byte:
			out[i] = append([]byte(nil), x...)
		default:
			h.throw(fmt.Errorf("kv: invalid key part of type %T", p))
		}
	}
	return out
}

func (h *binding) keyToJS(key []KeyPart) sobek.Value {
	out := make([]any, len(key))
	for i, p := range key {
		if b, ok := p.([]byte); ok {
			out[i] = h.vm.NewArrayBuffer(append([]byte(nil), b...))
		} else {
			out[i] = p
		}
	}
	return h.vm.ToValue(out)
}

func (h *binding) valueFromJS(v sobek.Value) Value {
	o := v.ToObject(h.vm)
	if u := o.Get("u64"); u != nil && !sobek.IsUndefined(u) {
		n, ok := u.Export().(*big.Int)
		if !ok || n.Sign() < 0 || n.BitLen() > 64 {
			h.throw(fmt.Errorf("kv: invalid KvU64"))
		}
		return Value{U64: n.Uint64(), IsU64: true}
	}
	data, ok := o.Get("data").Export().([]byte)
	if !ok {
		h.throw(fmt.Errorf("kv: invalid value"))
	}
	return Value{Data: append([]byte(nil), data...)}
}

func (h *binding) entryToJS(e Entry) sobek.Value {
	r := h.vm.NewObject()
	_ = r.Set("key", h.keyToJS(e.Key))
	val := h.vm.NewObject()
	if e.Value.IsU64 {
		_ = val.Set("u64", new(big.Int).SetUint64(e.Value.U64))
	} else {
		_ = val.Set("data", h.vm.NewArrayBuffer(append([]byte(nil), e.Value.Data...)))
	}
	_ = r.Set("value", val)
	_ = r.Set("versionstamp", e.Versionstamp)
	return r
}

func (h *binding) get(key sobek.Value) sobek.Value {
	e, ok, err := h.store.Get(h.keyFromJS(key))
	if err != nil {
		h.throw(err)
	}
	if !ok {
		return sobek.Null()
	}
	return h.entryToJS(e)
}

func (h *binding) list(selV, optsV sobek.Value) sobek.Value {
	sel := selV.ToObject(h.vm)
	var s Selector
	if v := sel.Get("prefix"); v != nil && !sobek.IsUndefined(v) {
		s.Prefix = h.keyFromJS(v)
		if s.Prefix == nil {
			s.Prefix = []KeyPart{}
		}
	}
	if v := sel.Get("start"); v != nil && !sobek.IsUndefined(v) {
		s.Start = h.keyFromJS(v)
	}
	if v := sel.Get("end"); v != nil && !sobek.IsUndefined(v) {
		s.End = h.keyFromJS(v)
	}
	o := optsV.ToObject(h.vm)
	opts := ListOptions{Limit: int(o.Get("limit").ToInteger()), Reverse: o.Get("reverse").ToBoolean()}
	if c := o.Get("cursor"); c != nil && !sobek.IsUndefined(c) && c.String() != "" {
		b, err := base64.RawURLEncoding.DecodeString(c.String())
		if err != nil {
			h.throw(fmt.Errorf("kv: invalid cursor"))
		}
		opts.Cursor = b
	}
	entries, encoded, err := h.store.List(s, opts)
	if err != nil {
		h.throw(err)
	}
	es := make([]any, len(entries))
	cursors := make([]any, len(entries))
	for i, e := range entries {
		es[i] = h.entryToJS(e)
		cursors[i] = base64.RawURLEncoding.EncodeToString(encoded[i])
	}
	r := h.vm.NewObject()
	_ = r.Set("entries", es)
	_ = r.Set("cursors", cursors)
	return r
}

var mutationKinds = map[string]MutationKind{"set": MutSet, "delete": MutDelete, "sum": MutSum, "min": MutMin, "max": MutMax}

func (h *binding) commit(checksV, mutsV sobek.Value) sobek.Value {
	var checks []Check
	for _, c := range h.objects(checksV) {
		checks = append(checks, Check{Key: h.keyFromJS(c.Get("key")), Versionstamp: c.Get("versionstamp").String()})
	}
	var muts []Mutation
	for _, m := range h.objects(mutsV) {
		kind, ok := mutationKinds[m.Get("type").String()]
		if !ok {
			h.throw(fmt.Errorf("kv: unknown mutation %s", m.Get("type")))
		}
		mut := Mutation{Kind: kind, Key: h.keyFromJS(m.Get("key"))}
		if kind != MutDelete {
			mut.Value = h.valueFromJS(m.Get("value"))
		}
		if ms := m.Get("expireIn"); ms != nil && !sobek.IsUndefined(ms) {
			mut.ExpireIn = time.Duration(ms.ToFloat() * float64(time.Millisecond))
		}
		muts = append(muts, mut)
	}
	ok, stamp, err := h.store.Commit(checks, muts)
	if err != nil {
		h.throw(err)
	}
	r := h.vm.NewObject()
	_ = r.Set("ok", ok)
	if ok {
		_ = r.Set("versionstamp", stamp)
	}
	return r
}

func (h *binding) objects(v sobek.Value) []*sobek.Object {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return nil
	}
	arr := v.ToObject(h.vm)
	n := int(arr.Get("length").ToInteger())
	out := make([]*sobek.Object, n)
	for i := 0; i < n; i++ {
		out[i] = arr.Get(fmt.Sprint(i)).ToObject(h.vm)
	}
	return out
}
