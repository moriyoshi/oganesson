package gojatarget

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/microsoft/TypeScript/tsc/ogapi"
)

// Finish applies the fixups every bundle built for the engine (Sobek, which
// shares these Goja bugs) needs after esbuild: QuoteKeywordClassFields,
// HoistDefaultedArrayPatterns, HoldArrowArguments and ContextualThenables.
func Finish(code string) string {
	return LoweredAsyncIteratorPrototype(ContextualThenables(HoldArrowArguments(HoistDefaultedArrayPatterns(QuoteKeywordClassFields(code)))))
}

// defaultedArrayPatternRe is a cheap precondition of
// HoistDefaultedArrayPatterns (which parses the code): a property whose
// value is an array with an "=" inside, followed by "] =" (not "==" or
// "=>"), within one statement. Defaults holding a ";" (a function body)
// are missed, and keep the engine's parse error.
var defaultedArrayPatternRe = regexp.MustCompile(`:\s*\[[^;]*?=[^;]*?\]\s*=[^=>]`)

// HoistDefaultedArrayPatterns works around the engine's parser, which
// rejects an array pattern with element defaults as a defaulted object
// pattern property ({ args: [w = 1, h = 1] = [] }, @react-three/drei's
// RoundedBox, as esbuild prints it): ogapi.HoistDefaultedArrayPatterns
// moves such patterns to a temporary, until none is left.
func HoistDefaultedArrayPatterns(code string) string {
	if !defaultedArrayPatternRe.MatchString(code) {
		return code
	}
	n := 0
	tmp := func() string {
		for {
			name := fmt.Sprintf("__og_dp%d", n)
			n++
			if !strings.Contains(code, name) {
				return name
			}
		}
	}
	for range 8 {
		out, changed := ogapi.HoistDefaultedArrayPatterns("/og-bundle.js", code, tmp)
		if !changed {
			break
		}
		code = out
	}
	return code
}

// loweredAsyncGenProto matches the lowered form of
// Object.getPrototypeOf(async function* () {}).prototype, the first step
// of the idiom that reaches %AsyncIteratorPrototype%
// (Object.getPrototypeOf(<that>); whatwg-url's, which MongoDB's driver
// loads). Lowered, the async generator function is a plain function, so the
// expression is Function.prototype.prototype (undefined) and the outer
// call throws.
// Whitespace and comments may surround the function (@sec-ant/readable-stream
// has /* istanbul ignore next */ there).
var loweredAsyncGenProto = regexp.MustCompile(`Object\.getPrototypeOf\(\s*(?:/\*[^*]*\*+(?:[^/*][^*]*\*+)*/\s*)*function\s*\(\)\s*\{\s*return __asyncGenerator\(this, null, function\*\s*\(\)\s*\{\s*\}\);\s*\}\s*\)\.prototype`)

// LoweredAsyncIteratorPrototype makes that expression an object whose
// prototype is an async iterator prototype (its [Symbol.asyncIterator]
// returns this), as %AsyncGeneratorPrototype% is.
func LoweredAsyncIteratorPrototype(code string) string {
	if !strings.Contains(code, "return __asyncGenerator(this, null, function*") {
		return code
	}
	return loweredAsyncGenProto.ReplaceAllLiteralString(code, `Object.create({ [Symbol.asyncIterator]() { return this; } })`)
}

var thenMemberRe = regexp.MustCompile(`\bthen\s*[(:]|["']then["']\s*[(:]`)

// ContextualThenables works around the engine's thenable job, which runs a
// thenable's then outside the async context it was resolved in: `await
// thenable` and `resolve(thenable)` call then later from a job that neither
// grabs nor resumes the AsyncContextTracker context, so AsyncLocalStorage
// reads undefined inside it (Node runs it in the resolving context:
// Drizzle's query builders, Astro's render scope). The engine reads
// thenable.then synchronously while resolving, so each then method becomes
// an accessor whose getter wraps the method in the context current at the
// read (__ogContextThen, runtime/engine). A setter keeps `x.then = …`
// working. Not covered: then attached at run time, arrow-valued then, and
// then methods using super (ogapi.ThenMethods).
func ContextualThenables(code string) string {
	if !thenMemberRe.MatchString(code) {
		return code
	}
	ms := ogapi.ThenMethods("/og-bundle.js", code)
	if len(ms) == 0 {
		return code
	}
	var sb strings.Builder
	last := 0
	for _, m := range ms {
		sb.WriteString(code[last:m.Start])
		static := ""
		if m.Static {
			static = "static "
		}
		sep := ", "
		if m.InClass {
			sep = " "
		}
		sb.WriteString(static + "get then() { return (globalThis.__ogContextThen || ((f) => f))(" + m.Fn + "); }" + sep +
			static + `set then(v) { Object.defineProperty(this, "then", { value: v, writable: true, enumerable: true, configurable: true }); }`)
		last = m.End
	}
	sb.WriteString(code[last:])
	return sb.String()
}

// HoldArrowArguments works around a Goja compiler bug: a function whose
// arguments object is read only by nested arrow functions (`function () {
// return p.then(() => f.apply(this, arguments)) }`, vitefu's) loses it,
// panicking with an index out of range or reading another value. A
// reference in the function itself (void arguments;) keeps it.
func HoldArrowArguments(code string) string {
	if !strings.Contains(code, "arguments") || !strings.Contains(code, "=>") {
		return code
	}
	at := ogapi.ArrowArgumentsHolders("/og-bundle.js", code)
	if len(at) == 0 {
		return code
	}
	sort.Ints(at)
	var sb strings.Builder
	last := 0
	for _, i := range at {
		sb.WriteString(code[last:i])
		sb.WriteString(" void arguments;")
		last = i
	}
	sb.WriteString(code[last:])
	return sb.String()
}

// keywordFields are field names Goja's parser rejects before "=" (class C
// { set = 1 } is a SyntaxError there; class C { "set" = 1 } is not).
var keywordFields = map[string]bool{"get": true, "set": true, "async": true}

var keywordFieldRe = regexp.MustCompile(`\b(?:get|set|async)\s*=`)

// QuoteKeywordClassFields quotes class fields named get, set or async (as
// Hono's FrozenHeaders declares): same field, a name Goja parses.
func QuoteKeywordClassFields(code string) string {
	if !keywordFieldRe.MatchString(code) {
		return code
	}
	names := ogapi.ClassFieldNames("/og-bundle.js", code, keywordFields)
	if len(names) == 0 {
		return code
	}
	var sb strings.Builder
	last := 0
	for _, n := range names {
		sb.WriteString(code[last:n.Start])
		sb.WriteString(`"` + n.Text + `"`)
		last = n.End
	}
	sb.WriteString(code[last:])
	return sb.String()
}
