package next

import (
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
)

func TestTransformInlineActions(t *testing.T) {
	b := &builder{buildID: "b", actions: map[string]*actionModule{}}
	src := `"use strict";
import { db } from "./db";
const top = 1;
export default async function Page({ user }: { user: any }) {
  let n = 2;
  async function save(form: FormData) {
    "use server";
    await db.save(user.name, user.profile.age, user.greet(), { n }, top, form);
  }
  const plain = async () => { "use server"; return top; };
  return [save, plain];
}
export async function exported() { "use server"; return top; }
`
	got, err := b.transformInlineActions("/app/page.tsx", src, "", false)
	if err != nil {
		t.Fatal(err)
	}
	m := b.actions["/app/page.tsx"]
	id0, id1, id2 := m.IDs["$$RSC_SERVER_ACTION_0"], m.IDs["$$RSC_SERVER_ACTION_1"], m.IDs["$$RSC_SERVER_ACTION_2"]
	if len(m.IDs) != 3 || id0 == "" || id1 == "" || id2 == "" {
		t.Fatalf("actions %v", m.IDs)
	}
	for _, want := range []string{
		// hoisted after the prologue, with decrypted bound arguments: user
		// (also read through user.greet(), a method call), n (shorthand)
		"\"use strict\";\nexport const $$RSC_SERVER_ACTION_0 = async function save($$ACTION_CLOSURE_BOUND, form: FormData) {var [$$ACTION_ARG_0, $$ACTION_ARG_1] = await __og_dab(\"" + id0 + "\", $$ACTION_CLOSURE_BOUND);\n" +
			"    \"use server\";\n    await db.save($$ACTION_ARG_0.name, $$ACTION_ARG_0.profile.age, $$ACTION_ARG_0.greet(), { n: $$ACTION_ARG_1 }, top, form);\n  };\n" +
			"__og_rsr($$RSC_SERVER_ACTION_0, \"" + id0 + "\", null);\n",
		// no closure: no bound arguments
		"export const $$RSC_SERVER_ACTION_1 = async function plain() { \"use server\"; return top; };\n__og_rsr($$RSC_SERVER_ACTION_1, \"" + id1 + "\", null);\n",
		// the original places
		"var save = $$RSC_SERVER_ACTION_0.bind(null, __og_eab(\"" + id0 + "\", user, n));",
		"const plain = ($$RSC_SERVER_ACTION_1);",
		"export var exported = $$RSC_SERVER_ACTION_2;",
		`import { encryptActionBoundArgs as __og_eab, decryptActionBoundArgs as __og_dab } from "next/dist/server/app-render/encryption";`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks\n%s\n--- output:\n%s", want, got)
		}
	}
	if res := api.Transform(got, api.TransformOptions{Loader: api.LoaderTSX}); len(res.Errors) > 0 {
		t.Errorf("output does not parse: %s\n%s", res.Errors[0].Text, got)
	}

	// Paths are narrowed: user.name and user.profile.greeting, not user.
	got, err = b.transformInlineActions("/app/narrow.tsx", `export default function P({ user }: any) {
  async function a() { "use server"; return user.profile.greeting + user.name + user.profile.greeting.length; }
  return a;
}`, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `__og_eab("`+b.actions["/app/narrow.tsx"].IDs["$$RSC_SERVER_ACTION_0"]+`", user.profile.greeting, user.name))`) ||
		!strings.Contains(got, "return $$ACTION_ARG_0 + $$ACTION_ARG_1 + $$ACTION_ARG_0.length;") {
		t.Errorf("narrowed captures:\n%s", got)
	}

	// A nested action: the inner is hoisted too and, inside the outer's
	// hoisted body, replaced by its binding. It closes over the outer's own
	// parameter (form), which stays named in the hoisted outer, so no
	// shared-closure threading is needed.
	got, err = b.transformInlineActions("/app/nested.tsx", `export default function P() {
  async function outer(form: FormData) {
    "use server";
    async function inner(x: string) {
      "use server";
      return form.get(x);
    }
    return inner;
  }
  return outer;
}`, "", false)
	if err != nil {
		t.Fatal(err)
	}
	nid1 := b.actions["/app/nested.tsx"].IDs["$$RSC_SERVER_ACTION_1"]
	for _, want := range []string{
		// the inner, hoisted with its decrypted bound argument (form)
		`export const $$RSC_SERVER_ACTION_1 = async function inner($$ACTION_CLOSURE_BOUND, x: string) {var [$$ACTION_ARG_0] = await __og_dab("` + nid1 + `", $$ACTION_CLOSURE_BOUND);`,
		"return $$ACTION_ARG_0.get(x);",
		// inside the outer's hoisted body, the inner becomes its binding
		`var inner = $$RSC_SERVER_ACTION_1.bind(null, __og_eab("` + nid1 + `", form));`,
		// the outer, which closes over nothing, is hoisted and placed in the module
		"export const $$RSC_SERVER_ACTION_0 = async function outer(form: FormData) {",
		"var outer = $$RSC_SERVER_ACTION_0;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("nested output lacks\n%s\n--- output:\n%s", want, got)
		}
	}
	if ids := b.actions["/app/nested.tsx"].IDs; len(ids) != 2 {
		t.Errorf("nested: actions %v, want 2", ids)
	}
	if res := api.Transform(got, api.TransformOptions{Loader: api.LoaderTSX}); len(res.Errors) > 0 {
		t.Errorf("nested output does not parse: %s\n%s", res.Errors[0].Text, got)
	}

	for _, c := range []struct{ src, err string }{
		{`export function P() { function a() { "use server"; } return a; }`, "must be async functions"},
		{`export function P() { const o = { async m() { "use server"; } }; return o; }`, "methods cannot be Server Actions"},
		{`export function P() { async function a() { "use server"; return this; } return a; }`, "cannot use this"},
		{`export default function P({ user }: any) { async function outer() { "use server"; async function inner() { "use server"; return user.name; } return inner; } return outer; }`, "closes over the same value"},
	} {
		if _, err := b.transformInlineActions("/app/bad.tsx", c.src, "", false); err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: err %v, want %q", c.src, err, c.err)
		}
	}
}

// TestInlineActionsUseServerModule: in a "use server" module, a top-level
// function carrying a redundant "use server" directive is one of the module's
// own actions (addActions/serverActionSource register it under its export id),
// so transformInlineActions with moduleUseServer must NOT also hoist it to an
// inline $$RSC_SERVER_ACTION — that double-registration (two ids for one
// function) is the latent bug this guards. Its inline "use cache" functions
// are still hoisted. Without the flag (a non-"use server" module) the same
// top-level function IS an inline action and is hoisted.
func TestInlineActionsUseServerModule(t *testing.T) {
	src := `"use server";
export async function bar(form: FormData) { "use server"; return form.get("x"); }
export async function cached() { "use cache"; return 1; }
`
	b := &builder{buildID: "b", actions: map[string]*actionModule{}}
	got, err := b.transformInlineActions("/app/actions.ts", src, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "$$RSC_SERVER_ACTION") {
		t.Errorf("use-server module: bar was hoisted as an inline action\n%s", got)
	}
	if !strings.Contains(got, `export async function bar(form: FormData) { "use server"; return form.get("x"); }`) {
		t.Errorf("use-server module: bar should be left unchanged for addActions\n%s", got)
	}
	if !strings.Contains(got, "$$RSC_SERVER_CACHE_0") {
		t.Errorf("use-server module: the inline \"use cache\" function should still be hoisted\n%s", got)
	}
	if ids := b.actions["/app/actions.ts"].IDs; len(ids) != 1 || ids["$$RSC_SERVER_CACHE_0"] == "" {
		t.Errorf("use-server module: hoisted actions %v, want only $$RSC_SERVER_CACHE_0", ids)
	}
	if res := api.Transform(got, api.TransformOptions{Loader: api.LoaderTS}); len(res.Errors) > 0 {
		t.Errorf("output does not parse: %s\n%s", res.Errors[0].Text, got)
	}

	// Contrast: without the flag (the pre-fix behavior, and how a
	// non-"use server" module is treated) bar IS an inline action and is
	// hoisted — the double-handling the flag prevents.
	b2 := &builder{buildID: "b", actions: map[string]*actionModule{}}
	got2, err := b2.transformInlineActions("/app/actions.ts", src, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got2, "export var bar = $$RSC_SERVER_ACTION_0;") {
		t.Errorf("without the flag, bar should be hoisted (double-handling repro)\n%s", got2)
	}
}

// TestTransformUseCache: "use cache" functions take the shape next-swc gives
// them (its server-actions fixtures 33, 34, 38, 39 and 46 at v16.4.0): the
// body (without the directive) as NAME_INNER, an export wrapping Next's
// cache(kind, id, boundArgsLength, INNER, args) in React's cache, ids
// whose info byte marks a cache and the arguments it uses, bound captured
// values, and a module-level directive caching its exported async
// functions only.
func TestTransformUseCache(t *testing.T) {
	b := &builder{buildID: "b", actions: map[string]*actionModule{}}
	run := func(path, src, file string) (string, map[string]string) {
		t.Helper()
		got, err := b.transformInlineActions(path, src, file, false)
		if err != nil {
			t.Fatal(err)
		}
		return got, b.actions[path].IDs
	}
	contains := func(got string, wants ...string) {
		t.Helper()
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("output lacks %s\n--- output:\n%s", w, got)
			}
		}
	}

	// 33: no arguments.
	got, ids := run("/app/33.tsx", "const v = 'world'\nasync function fn() {\n  'use cache'\n  return 'hello, ' + v\n}\nexport async function Component() {\n  const data = await fn()\n  return <div>{data}</div>\n}\n", "")
	id := ids["$$RSC_SERVER_CACHE_0"]
	if !strings.HasPrefix(id, "80") {
		t.Errorf("33: id %s, want info byte 80", id)
	}
	contains(got,
		"const $$RSC_SERVER_CACHE_0_INNER = async function fn() {\n  \n  return 'hello, ' + v\n};\n",
		`export var $$RSC_SERVER_CACHE_0 = __og_rc(function fn() { return __og_cache("default", "`+id+`", 0, $$RSC_SERVER_CACHE_0_INNER, []); });`,
		`__og_rsr($$RSC_SERVER_CACHE_0, "`+id+`", null);`,
		`Object["defineProperty"]($$RSC_SERVER_CACHE_0, "name", { value: "fn" });`,
		"var fn = $$RSC_SERVER_CACHE_0;",
		`import { cache as __og_cache } from "next/dist/build/webpack/loaders/next-flight-loader/cache-wrapper";`,
		`import { cache as __og_rc } from "react";`)

	// 46: the info byte's argument bits; a rest parameter (or more than
	// six) sets them all and the last bit; actions keep theirs.
	got, ids = run("/app/46.tsx", `async function f1(a, b) { 'use cache'; return [a, b] }
async function f2(a, b) { 'use server'; return [a, b] }
async function f3(a, b, ...rest) { 'use cache'; return [a, b, rest] }
async function f6(a, b, c, d, e, f, g) { 'use cache'; return [a, b, c, d, e, f, g] }
`, "")
	for name, want := range map[string]string{"$$RSC_SERVER_CACHE_0": "e0", "$$RSC_SERVER_CACHE_2": "ff", "$$RSC_SERVER_CACHE_3": "ff"} {
		if !strings.HasPrefix(ids[name], want) {
			t.Errorf("46: %s id %s, want info byte %s", name, ids[name], want)
		}
	}
	contains(got,
		`$$RSC_SERVER_CACHE_0_INNER, Array.prototype.slice.call(arguments, 0, 2)`,
		`$$RSC_SERVER_CACHE_2_INNER, Array.prototype.slice.call(arguments)`,
		`$$RSC_SERVER_CACHE_3_INNER, Array.prototype.slice.call(arguments, 0, 7)`,
		"export const $$RSC_SERVER_ACTION_1 = async function f2(a, b)")

	// 39: captured values are bound (encrypted), arrive as INNER's first
	// argument, and count as the first argument of the id.
	got, ids = run("/app/39.tsx", "async function Component({ foo }) {\n  const a = 123\n  async function fn() {\n    'use cache'\n    console.log(a)\n    return { foo }\n  }\n  const data = await fn()\n  return <div>{data}</div>\n}\n", "")
	id = ids["$$RSC_SERVER_CACHE_0"]
	if !strings.HasPrefix(id, "c0") {
		t.Errorf("39: id %s, want info byte c0", id)
	}
	contains(got,
		"async function fn([$$ACTION_ARG_0, $$ACTION_ARG_1]) {",
		"console.log($$ACTION_ARG_0)",
		"return { foo: $$ACTION_ARG_1 }",
		`__og_cache("default", "`+id+`", 2, $$RSC_SERVER_CACHE_0_INNER, Array.prototype.slice.call(arguments, 0, 1))`,
		`var fn = $$RSC_SERVER_CACHE_0.bind(null, __og_eab("`+id+`", a, foo));`)

	// 38: a cache kind; 34: a module-level directive caches the exported
	// async functions (through an export clause, as the default) only.
	got, _ = run("/app/38.tsx", "'use cache: x'\nexport async function foo() {\n  return 'data'\n}\n", "use cache: x")
	contains(got, `__og_cache("x", "80`, "export var foo = $$RSC_SERVER_CACHE_0;")
	got, ids = run("/app/34.tsx", `'use cache'
const foo = async () => { return 'foo' }
export { bar }
async function bar() { return 'bar' }
const qux = async function qux() { return 'qux' }
const quux = async () => { return 'quux' }
export { foo }
export default quux
`, "use cache")
	contains(got, "const foo = ($$RSC_SERVER_CACHE_0)\n", "var bar = $$RSC_SERVER_CACHE_1;", "const qux = async function qux() { return 'qux' }", "const quux = ($$RSC_SERVER_CACHE_2)\n")
	if len(ids) != 3 {
		t.Errorf("34: cached %v, want foo, bar and quux", ids)
	}
}
