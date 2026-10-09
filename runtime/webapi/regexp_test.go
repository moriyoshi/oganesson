package webapi_test

import (
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

func TestRegExpPropertyEscapes(t *testing.T) {
	for _, c := range []struct{ name, js, want string }{
		{"category", `return new RegExp("\\p{L}+", "u").exec("日本語abc!")[0]`, "日本語abc"},
		{"negated", `return "a1-b2".replace(new RegExp("\\P{L}", "gu"), "_")`, "a__b_"},
		{"in class", `return "a1-b2".replace(new RegExp("[\\p{N}-]", "gu"), "")`, "ab"},
		{"negated in class", `return "a1-b2".replace(new RegExp("[\\P{L}x]", "gu"), "")`, "ab"},
		{"long names", `return [new RegExp("^\\p{General_Category=Uppercase_Letter}$", "u").test("A"), new RegExp("^\\p{gc=Lu}$", "u").test("a")].join()`, "true,false"},
		{"script vs extensions", `return [new RegExp("\\p{sc=Hira}", "u").test("ー"), new RegExp("\\p{scx=Hira}", "u").test("ー"), new RegExp("\\p{Script=Han}", "u").test("漢")].join()`, "false,true,true"},
		{"binary", `return ["\\p{Emoji_Presentation}", "\\p{Alphabetic}", "\\p{White_Space}", "\\p{ASCII}", "\\p{Any}"].map((p, i) => new RegExp("^" + p + "$", "u").test(["😀", "é", "　", "~", String.fromCodePoint(0x10FFFF)][i])).join()`, "true,true,true,true,true"},
		{"unassigned", `return [new RegExp("\\p{Cn}", "u").test("͸"), new RegExp("\\p{C}", "u").test("͸"), new RegExp("\\p{Assigned}", "u").test("͸")].join()`, "true,true,false"},
		{"astral", `return new RegExp("^\\p{Lu}$", "u").test("𝐀")`, "true"},
		{"invalid", `try { new RegExp("\\p{Nope}", "u"); return "no error"; } catch (e) { return e.name + ": " + e.message; }`, `SyntaxError: Invalid regular expression: /\p{Nope}/u: Invalid property name`},
		{"not a binary property in ECMAScript", `try { new RegExp("\\p{Other_Alphabetic}", "u"); return "no error"; } catch (e) { return e.name; }`, "SyntaxError"},
		{"without u", `return new RegExp("\\p{L}").test("p{L}")`, "true"},
		{"source", `const re = new RegExp("\\p{L}", "u"); return [re.source, String(re), re.flags].join(" ")`, `\p{L} /\p{L}/u u`},
		{"identity", `const re = new RegExp("\\p{L}", "u"); return [re instanceof RegExp, /a/ instanceof RegExp, re.constructor === RegExp, RegExp(re) === re, RegExp.name, RegExp.length, Object.prototype.toString.call(re)].join()`, "true,true,true,true,RegExp,2,[object RegExp]"},
		{"subclass", `class R extends RegExp {}; const r = new R("\\p{N}", "u"); return [r instanceof R, r.test("5"), r.test("x")].join()`, "true,true,false"},
		{"species", `return "a1b2c".split(new RegExp("\\p{N}", "u")).join()`, "a,b,c"},
		// Sobek's own tables (Unicode 17, as Node 24): characters and scripts
		// new in Unicode 16, unassigned code points and lone surrogates (og's
		// earlier rewriter got the last two wrong).
		{"unicode versions", `return [new RegExp("^\\p{Script=Unknown}$", "u").test("\u0378"), new RegExp("^\\p{gc=Cs}$", "u").test("\uD800"), new RegExp("^\\p{Script=Garay}$", "u").test("\u{10D40}"), new RegExp("^\\p{Lu}$", "u").test("\u1C89"), new RegExp("^\\p{Emoji}$", "u").test("\u{1FAE9}")].join()`, "true,true,true,true,true"},
		{"other syntax errors in V8's form", `try { new RegExp("a{2,1}"); return "no error"; } catch (e) { return e.message; }`, "Invalid regular expression: /a{2,1}/: numbers out of order in {} quantifier"},
		// The v flag (unicodeSets) without its set notation: run as u.
		{"v flag", `const re = new RegExp("[A-Z]", "gv"); return ["aBcD".replace(re, (c) => c.toLowerCase()), re.flags, re.unicodeSets, re.unicode, new RegExp("^\\p{Lu}$", "v").test("É"), /x/u.unicodeSets].join()`, "abcd,gv,true,false,true,false"},
		{"v flag escapes", `return new RegExp("^[\\(\\)]+$", "v").test("()")`, "true"},
		{"v flag set notation", `try { new RegExp("[\\p{L}--[a-z]]", "v"); return "no error"; } catch (e) { return e.name; }`, "no error"},
		{"u and v", `try { new RegExp("a", "uv"); return "no error"; } catch (e) { return e.name; }`, "SyntaxError"},
	} {
		check(t, c.name, eval(t, webapi.Options{}, c.js), c.want)
	}
}

// TestRegExpLiteralLowering: regex literals with property escapes, in and
// out of classes, run as written (esbuild no longer lowers them).
func TestRegExpLiteralLowering(t *testing.T) {
	res := api.Transform(`return [/^[\p{Lu}\d]+$/u.test("ÀB1"), /\p{scx=Han}/u.test("々")].join();`, gojatarget.TransformOptions(api.LoaderJS, "lit.js"))
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors[0].Text)
	}
	if strings.Contains(string(res.Code), "new RegExp") {
		t.Errorf("literal lowered: %s", res.Code)
	}
	check(t, "literal", eval(t, webapi.Options{}, string(res.Code)), "true,true")
}

// TestRegExpUnicodeSets: the v flag's set notation (nested classes, &&, --,
// \q{…}) translated to u-flag patterns (unicodesets.ts), against Node 24's
// matches and errors.
func TestRegExpUnicodeSets(t *testing.T) {
	got := eval(t, webapi.Options{}, `const cases = [
  ["[\\p{L}--[a-z]]", "v", ["a", "B", "é", "1"]],
  ["^[\\p{L}&&\\p{sc=Greek}]+$", "v", ["αβ", "aβ"]],
  ["[[a-z][0-9]]", "v", ["k", "5", "-"]],
  ["^[\\q{abc|d}x]$", "v", ["abc", "d", "x", "ab"]],
  ["[\\q{abc|ab}]", "gv", ["abcab"]],
  ["[^[a-z]--[aeiou]]", "v", ["b", "a", "Z"]],
  ["[\\w--\\d]+", "v", ["ab12cd"]],
  ["^[[a-z]--\\q{b}]$", "v", ["b", "c"]],
  ["^[\\q{ab|cd}&&\\q{cd|ef}]$", "v", ["cd", "ab"]],
  ["^[\\q{ab|c}--\\q{ab}]$", "v", ["ab", "c"]],
  ["[\\&\\-\\!]", "v", ["&", "-", "!", "a"]],
  ["^[\\p{Lu}--[A-Z]]$", "iv", ["É", "a", "é"]],
  ["[a-c]+", "v", ["xabcz"]],
  ["\\p{L}+", "v", ["日本!"]],
  ["[\\p{ASCII}--\\p{L}--\\p{N}]+", "v", ["ab!?12"]],
  ["[a&&b--c]", "v", null],
  ["[(]", "v", null],
  ["[a&&&b]", "v", null],
  ["[a--]", "v", null],
  ["[\\p{RGI_Emoji}]", "v", ["😀"]],
  ["[^\\q{ab}]", "v", null],
];
const out = [];
for (const [p, f, inputs] of cases) {
  let re;
  try { re = new RegExp(p, f); } catch (e) { out.push(p + " /" + f + " -> " + e.name); continue; }
  if (!inputs) { out.push(p + " /" + f + " -> ok"); continue; }
  out.push(p + " /" + f + " -> " + inputs.map((s) => f.includes("g") ? JSON.stringify(s.match(re)) : (s.match(re) || [null])[0]).join(","));
}
var result = out.join("\n");

return result;`)
	check(t, "unicode sets", got, `[\p{L}--[a-z]] /v -> ,B,é,
^[\p{L}&&\p{sc=Greek}]+$ /v -> αβ,
[[a-z][0-9]] /v -> k,5,
^[\q{abc|d}x]$ /v -> abc,d,x,
[\q{abc|ab}] /gv -> ["abc","ab"]
[^[a-z]--[aeiou]] /v -> ,a,Z
[\w--\d]+ /v -> ab
^[[a-z]--\q{b}]$ /v -> ,c
^[\q{ab|cd}&&\q{cd|ef}]$ /v -> cd,
^[\q{ab|c}--\q{ab}]$ /v -> ,c
[\&\-\!] /v -> &,-,!,
^[\p{Lu}--[A-Z]]$ /iv -> É,,é
[a-c]+ /v -> abc
\p{L}+ /v -> 日本
[\p{ASCII}--\p{L}--\p{N}]+ /v -> !?
[a&&b--c] /v -> SyntaxError
[(] /v -> SyntaxError
[a&&&b] /v -> SyntaxError
[a--] /v -> SyntaxError
[\p{RGI_Emoji}] /v -> 😀
[^\q{ab}] /v -> SyntaxError`)
	res := api.Transform(`return [/^[\p{L}--[a-z]]+$/v.test("ÀB"), /[\q{ab|c}]/v.exec("xab")[0]].join();`, gojatarget.TransformOptions(api.LoaderJS, "v.js"))
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors[0].Text)
	}
	check(t, "literal", eval(t, webapi.Options{}, string(res.Code)), "true,ab")
	check(t, "source", eval(t, webapi.Options{}, `const re = new RegExp("[\\p{L}--[a-z]]", "gv"); return [re.source, String(re), re.flags, new RegExp(re).source, new RegExp(re).test("B")].join(" ");`), `[\p{L}--[a-z]] /[\p{L}--[a-z]]/gv gv [\p{L}--[a-z]] true`)
}

// TestRegExpPropertiesOfStrings: the v flag's properties of strings
// (RGI_Emoji and the emoji sequence properties) from Unicode 17's emoji data
// as Go tables (emoji_tables.go), in and out of classes and in set
// operations; \P{…} and negated classes of them are SyntaxErrors. The
// expectation is Node 24's (match lengths in code points).
func TestRegExpPropertiesOfStrings(t *testing.T) {
	got := eval(t, webapi.Options{}, `const cases = [
  ["^\\p{RGI_Emoji}$", ["😀", "👨‍👩‍👧", "🇯🇵", "1️⃣", "👍🏽", "🏴󠁧󠁢󠁳󠁣󠁴󠁿", "a", "☺", "☺️", "©️"]],
  ["^[\\p{RGI_Emoji}--\\q{😀}]$", ["😀", "😃"]],
  ["^\\p{Emoji_Keycap_Sequence}$", ["1️⃣", "1"]],
  ["^\\p{RGI_Emoji_Flag_Sequence}+$", ["🇯🇵🇫🇷", "🇯"]],
  ["^\\p{RGI_Emoji_Modifier_Sequence}$", ["👍🏽", "👍"]],
  ["^\\p{RGI_Emoji_ZWJ_Sequence}$", ["👨‍👩‍👧", "😀"]],
  ["^\\p{Basic_Emoji}$", ["⌚", "©️", "©"]],
  ["^\\p{RGI_Emoji_Tag_Sequence}$", ["🏴󠁧󠁢󠁳󠁣󠁴󠁿", "🏴"]],
  ["\\p{RGI_Emoji}", ["a👨‍👩‍👧b"]],
];
const out = [];
for (const [p, inputs] of cases) {
  const re = new RegExp(p, "v");
  out.push(p + " -> " + inputs.map((s) => { const m = s.match(re); return m ? Array.from(m[0]).length : "-"; }).join(","));
}
try { new RegExp("\\P{RGI_Emoji}", "v"); out.push("P:ok"); } catch (e) { out.push("P:" + e.name); }
try { new RegExp("[^\\p{RGI_Emoji}]", "v"); out.push("neg:ok"); } catch (e) { out.push("neg:" + e.name); }
var result = out.join("\n");

return result;`)
	check(t, "properties of strings", got, `^\p{RGI_Emoji}$ -> 1,5,2,3,2,7,-,-,2,2
^[\p{RGI_Emoji}--\q{😀}]$ -> -,1
^\p{Emoji_Keycap_Sequence}$ -> 3,-
^\p{RGI_Emoji_Flag_Sequence}+$ -> 4,-
^\p{RGI_Emoji_Modifier_Sequence}$ -> 2,-
^\p{RGI_Emoji_ZWJ_Sequence}$ -> 5,-
^\p{Basic_Emoji}$ -> 1,2,-
^\p{RGI_Emoji_Tag_Sequence}$ -> 7,-
\p{RGI_Emoji} -> 5
P:SyntaxError
neg:SyntaxError`)
}
