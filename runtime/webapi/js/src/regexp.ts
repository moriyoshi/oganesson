// RegExp flags the engine lacks. Sobek implements Unicode property escapes
// (\p{…} / \P{…}) itself; it has neither the v flag (unicodeSets) nor the d
// flag (match indices). This RegExp runs a v-flag pattern as u, its set
// notation translated (unicodesets.ts), and makes a d-flag RegExp without
// d, whose exec results get `indices` from regexp-match-indices (vendored,
// evaluated on first use). Everything else is the native
// RegExp: the prototype is shared, so instanceof, subclassing and
// Symbol.species keep working. esbuild lowers literals with these flags to
// `new RegExp(…)` calls, so they get here too.
//
// Syntax errors take V8's form (Invalid regular expression: /src/flags:
// reason); Sobek's carry regexp2's parse error.
declare const require: (id: string) => any;
import { defineGlobal } from "./util";
import { unicodeSetsToUnicode } from "./unicodesets";

// RegExps made with the v flag (run as u: see unicodesets.ts).
const withUnicodeSets = new WeakSet<RegExp>();

// v8SyntaxError restates a regexp2 parse error from the native RegExp
// ("Invalid regular expression (regexp2): src (… (error parsing regexp:
// reason in `src`))") as V8 words it, with V8's reason where they differ.
function v8SyntaxError(e: unknown, source: string, flags: string): unknown {
  const m = e instanceof SyntaxError ? /error parsing regexp: (.*?) in `/.exec(e.message) : null;
  if (!m) return e;
  let reason = m[1];
  if (/^unknown unicode category, script, or property/.test(reason)) reason = "Invalid property name";
  else if (/^invalid repeat count/.test(reason)) reason = "numbers out of order in {} quantifier";
  return new SyntaxError(`Invalid regular expression: /${source}/${flags}: ${reason}`);
}

// RegExps made with the d flag, each with its d-less twin for the polyfill.
const withIndices = new WeakMap<RegExp, RegExp>();
let execWithIndices: ((this: RegExp, s: string) => RegExpExecArray | null) | undefined;

export function installRegExp() {
  const Native = RegExp;
  const sourceGetter = Object.getOwnPropertyDescriptor(Native.prototype, "source")!.get!;

  function OgRegExp(this: unknown, pattern?: unknown, flags?: unknown): RegExp {
    const isRegExp = pattern instanceof Native;
    if (!new.target && isRegExp && flags === undefined && (pattern as any).constructor === OgRegExp) {
      return pattern as RegExp;
    }
    // (A translated v-flag RegExp keeps the written source as an own property.)
    const source = isRegExp ? (Object.prototype.hasOwnProperty.call(pattern, "source") ? (pattern as RegExp).source : sourceGetter.call(pattern)) : pattern === undefined ? "" : String(pattern);
    const f = flags === undefined ? (isRegExp ? (pattern as RegExp).flags : "") : String(flags);
    const hasIndices = f.includes("d");
    let nativeFlags = hasIndices ? f.replace("d", "") : f;
    // The v flag (unicodeSets), which Sobek lacks: the same pattern under u.
    const unicodeSets = f.includes("v");
    let translated = source;
    if (unicodeSets) {
      if (f.includes("u") || f.indexOf("v") !== f.lastIndexOf("v")) throw new SyntaxError(`Invalid flags supplied to RegExp constructor '${f}'`);
      translated = unicodeSetsToUnicode(source, f);
      nativeFlags = nativeFlags.replace("v", "u");
    }
    let re: RegExp;
    try {
      re = Reflect.construct(Native, [translated, nativeFlags], new.target ?? OgRegExp);
    } catch (e) {
      throw v8SyntaxError(e, source, f);
    }
    if (translated !== source) {
      // Keep the pattern the program wrote (source, toString), not the
      // translation.
      Object.defineProperty(re, "source", { value: source, configurable: true });
    }
    if (unicodeSets) withUnicodeSets.add(re);
    if (hasIndices) {
      if (f.indexOf("d") !== f.lastIndexOf("d")) throw new SyntaxError(`Invalid flags supplied to RegExp constructor '${f}'`);
      withIndices.set(re, Reflect.construct(Native, [translated, nativeFlags]));
    }
    return re;
  }

  // The d flag: hasIndices / flags report it, exec adds indices.
  const proto = Native.prototype as any;
  const nativeExec = proto.exec;
  const nativeFlagsGetter = Object.getOwnPropertyDescriptor(proto, "flags")!.get!;
  Object.defineProperty(proto, "hasIndices", {
    get(this: RegExp) { return withIndices.has(this); },
    configurable: true,
  });
  const nativeUnicode = Object.getOwnPropertyDescriptor(proto, "unicode")!.get!;
  Object.defineProperty(proto, "unicodeSets", {
    get(this: RegExp) { return withUnicodeSets.has(this); },
    configurable: true,
  });
  Object.defineProperty(proto, "unicode", {
    get(this: RegExp) { return !withUnicodeSets.has(this) && nativeUnicode.call(this); },
    configurable: true,
  });
  Object.defineProperty(proto, "flags", {
    get(this: RegExp) {
      let f = nativeFlagsGetter.call(this) as string;
      // (The native getter reads unicode, false for these: v goes in its
      // place in the canonical order, before y.)
      if (withUnicodeSets.has(this)) f = f.includes("y") ? f.replace("y", "vy") : f + "v";
      return withIndices.has(this) ? "d" + f : f;
    },
    configurable: true,
  });
  Object.defineProperty(proto, "exec", {
    value: function exec(this: RegExp, s: string) {
      const twin = withIndices.get(this);
      if (!twin) return nativeExec.call(this, s);
      // The polyfill captures RegExp.prototype.exec when it loads; it runs on
      // the d-less twin, which this exec passes straight to the native one.
      if (!execWithIndices) execWithIndices = require("../vendor/node_modules/regexp-match-indices/implementation.js");
      twin.lastIndex = this.lastIndex;
      const result = execWithIndices!.call(twin, String(s));
      this.lastIndex = twin.lastIndex;
      return result;
    },
    writable: true,
    configurable: true,
  });
  Object.defineProperty(OgRegExp, "name", { value: "RegExp" });
  Object.defineProperty(OgRegExp, "length", { value: 2 });
  Object.setPrototypeOf(OgRegExp, Object.getPrototypeOf(Native));
  Object.defineProperty(OgRegExp, "prototype", { value: Native.prototype, writable: false });
  Object.defineProperty(OgRegExp, Symbol.species, { get() { return this; }, configurable: true });
  for (const key of Reflect.ownKeys(Native)) {
    if (key === "prototype" || key === "name" || key === "length" || key === Symbol.species) continue;
    const d = Object.getOwnPropertyDescriptor(Native, key);
    if (d) Object.defineProperty(OgRegExp, key, d);
  }
  Object.defineProperty(Native.prototype, "constructor", { value: OgRegExp, writable: true, configurable: true });
  defineGlobal("RegExp", OgRegExp);
}
