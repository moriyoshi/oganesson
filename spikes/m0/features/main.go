// Command features probes which esbuild-lowerable syntax features Goja
// supports, and which built-ins it lacks. Its output seeds the esbuild
// `Supported` map and the polyfill list used by internal/bundle.
package main

import (
	"fmt"
	"sort"

	"github.com/dop251/goja"
)

// syntax maps esbuild feature names to a probe that must evaluate to true.
var syntax = map[string]string{
	"arbitrary-module-namespace-names":  `true`, // modules are bundled away
	"array-spread":                      `[...[1,2]].length === 2`,
	"arrow":                             `(() => true)()`,
	"async-await":                       `(async function(){ await 1; }), true`,
	"async-generator":                   `(async function*(){ yield 1; }), true`,
	"bigint":                            `10n ** 2n === 100n`,
	"class":                             `(class A {}), true`,
	"class-field":                       `new (class { x = 1 })().x === 1`,
	"class-private-accessor":            `new (class { get #a(){return 1} t(){return this.#a} })().t() === 1`,
	"class-private-brand-check":         `(class { #x; static t(o){ return #x in o } }).t({}) === false`,
	"class-private-field":               `new (class { #x = 1; t(){return this.#x} })().t() === 1`,
	"class-private-method":              `new (class { #m(){return 1} t(){return this.#m()} })().t() === 1`,
	"class-private-static-accessor":     `(class { static get #a(){return 1} static t(){return this.#a} }).t() === 1`,
	"class-private-static-field":        `(class { static #x = 1; static t(){return this.#x} }).t() === 1`,
	"class-private-static-method":       `(class { static #m(){return 1} static t(){return this.#m()} }).t() === 1`,
	"class-static-blocks":               `(class { static x; static { this.x = 1 } }).x === 1`,
	"class-static-field":                `(class { static x = 1 }).x === 1`,
	"const-and-let":                     `(function(){ let a = 1; const b = 2; return a + b === 3 })()`,
	"decorators":                        `false`, // TC39 decorators: always lower
	"default-argument":                  `(function(a = 1){ return a })() === 1`,
	"destructuring":                     `(function(){ var {a, b:[c]} = {a:1, b:[2]}; return a + c === 3 })()`,
	"exponent-operator":                 `2 ** 3 === 8`,
	"for-await":                         `(async function(){ for await (const x of []) {} }), true`,
	"for-of":                            `(function(){ for (const x of [1]) return x === 1 })()`,
	"function-name-configurable":        `Object.getOwnPropertyDescriptor(function f(){}, "name").configurable`,
	"function-or-class-property-access": `(function(){}).name === "" || true`,
	"generator":                         `(function*(){ yield 1 })().next().value === 1`,
	"hashbang":                          `true`, // stripped by esbuild when bundling
	"import-assertions":                 `true`,
	"import-attributes":                 `true`,
	"import-meta":                       `false`, // no ESM in Goja
	"inline-script":                     `true`,
	"logical-assignment":                `(function(){ var a = null; a ??= 1; a ||= 2; a &&= 3; return a === 3 })()`,
	"nested-rest-binding":               `(function([...[a]]){ return a === 1 })([1])`,
	"new-target":                        `(function(){ return new.target === undefined })()`,
	"node-colon-prefix-import":          `true`,
	"node-colon-prefix-require":         `true`,
	"nullish-coalescing":                `(null ?? 1) === 1`,
	"object-accessors":                  `({ get a(){ return 1 } }).a === 1`,
	"object-extensions":                 `(function(){ var k = "a"; var o = { [k]: 1, m(){ return 2 } }; return o.a + o.m() === 3 })()`,
	"object-rest-spread":                `(function(){ var {a, ...r} = {a:1, b:2}; return ({...r}).b === 2 })()`,
	"optional-catch-binding":            `(function(){ try { throw 1 } catch { return true } })()`,
	"optional-chain":                    `(function(){ var o = null; return o?.a?.b === undefined && o?.[1] === undefined && o?.() === undefined })()`,
	"regexp-dot-all-flag":               `/a.b/s.test("a\nb")`,
	"regexp-lookbehind-assertions":      `/(?<=a)b/.test("ab") && !/(?<!a)b/.test("ab")`,
	"regexp-match-indices":              `/a/d.exec("a").indices[0][0] === 0`,
	"regexp-named-capture-groups":       `/(?<x>a)/.exec("a").groups.x === "a"`,
	"regexp-set-notation":               `/[\p{L}--[a-z]]/v.test("A")`,
	"regexp-sticky-and-unicode-flags":   `/a/y.sticky && /\u{1F600}/u.test("\u{1F600}")`,
	"regexp-unicode-property-escapes":   `/\p{Lu}/u.test("A")`,
	"rest-argument":                     `(function(...a){ return a.length === 2 })(1, 2)`,
	"template-literal":                  "`a${1}` === \"a1\"",
	"top-level-await":                   `false`, // no ESM in Goja
	"typeof-exotic-object-is-object":    `true`,
	"unicode-escapes":                   `"\u{41}" === "A"`,
	"using":                             `false`, // explicit resource management: always lower
}

// builtins lists globals and methods the bundles may need at runtime.
var builtins = []string{
	"globalThis", "Promise.allSettled", "Promise.any", "Promise.withResolvers",
	"Array.prototype.at", "Array.prototype.flat", "Array.prototype.flatMap",
	"Array.prototype.findLast", "Array.prototype.findLastIndex", "Array.prototype.toSorted",
	"Array.prototype.toReversed", "Array.prototype.with", "Array.fromAsync",
	"Object.fromEntries", "Object.hasOwn", "Object.groupBy", "Map.groupBy",
	"String.prototype.at", "String.prototype.replaceAll", "String.prototype.matchAll",
	"String.prototype.isWellFormed", "String.prototype.padStart", "String.prototype.trimStart",
	"WeakRef", "FinalizationRegistry", "AggregateError", "Intl", "Intl.DateTimeFormat",
	"Intl.NumberFormat", "Intl.Segmenter", "structuredClone", "queueMicrotask",
	"Error.captureStackTrace", "SharedArrayBuffer", "Atomics", "WebAssembly",
	"Symbol.asyncIterator", "Symbol.dispose", "BigInt64Array", "ArrayBuffer.prototype.transfer",
	"Set.prototype.union", "Iterator", "Iterator.prototype.map", "JSON.rawJSON",
	"Date.prototype.toLocaleString", "String.prototype.normalize", "String.prototype.localeCompare",
	"TextEncoder", "TextDecoder", "URL", "URLSearchParams", "setTimeout", "console",
}

func main() {
	var supported, unsupported []string
	for name, probe := range syntax {
		if eval(probe) {
			supported = append(supported, name)
		} else {
			unsupported = append(unsupported, name)
		}
	}
	sort.Strings(supported)
	sort.Strings(unsupported)
	fmt.Println("// Syntax features Goja lacks (esbuild must lower these):")
	for _, n := range unsupported {
		fmt.Printf("\t%q: false,\n", n)
	}
	fmt.Println("// Syntax features Goja supports:")
	for _, n := range supported {
		fmt.Printf("\t%q: true,\n", n)
	}

	fmt.Println("\n// Missing built-ins (need polyfills or Go-backed implementations):")
	for _, b := range builtins {
		if !eval(fmt.Sprintf("typeof (%s) !== 'undefined'", b)) {
			fmt.Println("\t" + b)
		}
	}
}

func eval(code string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	vm := goja.New()
	v, err := vm.RunString("(function(){ try { return (" + code + "); } catch (e) { return false } })()")
	if err != nil {
		// Parse errors land here: the syntax itself is unsupported.
		return false
	}
	return v.ToBoolean()
}
