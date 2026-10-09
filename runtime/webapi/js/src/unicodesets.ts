// The RegExp v flag (unicodeSets) restated under the u flag, which the
// engine has. Outside character classes v reads as u. A v-mode class is a
// set expression: nested classes, intersection (&&), subtraction (--) and
// strings (\q{…}). Each class becomes an atom that matches what the set
// matches:
//
// - a class of single code points stays a u class where it can
//   ([a-z\p{L}]), else an alternation of one-code-point atoms;
// - A&&B is (?:(?=B)A) and A--B is (?:(?!B)A): both sides match exactly one
//   code point at the same position, so the lookaround is exact;
// - strings are alternatives, longest first, as the spec orders them
//   (CompileAtom: "sorted by descending length"); set operations on
//   strings are computed on the literal sets.
//
// Properties of strings (\p{RGI_Emoji} and the other emoji sequence
// properties, which the engine lacks) come from the host: Unicode's emoji
// data as Go tables (emoji_tables.go), a class body and the strings.

import { host } from "./host";

// One-code-point matcher: `content` is a u-class body when the set is a
// plain union (spliceable into another class), `atom` matches one code
// point (null: none). `strings` are the set's other strings (length 0 or 2+
// code points).
type CSet = { content: string | null; atom: string | null; strings: string[] };

const cp = (c: number) => `\\u{${c.toString(16)}}`;
const literal = (s: string) => Array.from(s, (ch) => cp(ch.codePointAt(0)!)).join("");
const length = (s: string) => Array.from(s).length;

const STRING_PROPERTIES = new Set([
  "Basic_Emoji", "Emoji_Keycap_Sequence", "RGI_Emoji", "RGI_Emoji_Flag_Sequence",
  "RGI_Emoji_Modifier_Sequence", "RGI_Emoji_Tag_Sequence", "RGI_Emoji_ZWJ_Sequence",
]);
// ClassSetSyntaxCharacter: must be escaped in a v-mode class.
const SYNTAX = "()[]{}/-\\|";
// ClassSetReservedDoublePunctuator's characters (&& and -- are operators).
const DOUBLE = "&!#$%*+,.:;<=>?@^`~";
// What an identity escape may name in a v-mode class: syntax characters
// and ClassSetReservedPunctuator.
const IDENTITY = "^$\\.*+?()[]{}|/&-!#%,:;<=>@`~";

const simple = (content: string): CSet => ({ content, atom: `[${content}]`, strings: [] });

function union(sets: CSet[]): CSet {
  const strings = [...new Set(sets.flatMap((s) => s.strings))];
  if (sets.every((s) => s.content !== null)) {
    const content = sets.map((s) => s.content).join("");
    return { content, atom: content === "" ? null : `[${content}]`, strings };
  }
  const contents = sets.filter((s) => s.content !== null).map((s) => s.content).join("");
  const atoms = [contents === "" ? null : `[${contents}]`, ...sets.filter((s) => s.content === null).map((s) => s.atom)].filter((a): a is string => a !== null);
  return { content: null, atom: atoms.length === 0 ? null : atoms.length === 1 ? atoms[0] : `(?:${atoms.join("|")})`, strings };
}

function intersect(a: CSet, b: CSet): CSet {
  const atom = a.atom === null || b.atom === null ? null : `(?:(?=${b.atom})${a.atom})`;
  return { content: null, atom, strings: a.strings.filter((s) => b.strings.includes(s)) };
}

function subtract(a: CSet, b: CSet): CSet {
  const atom = a.atom === null ? null : b.atom === null ? a.atom : `(?:(?!${b.atom})${a.atom})`;
  return { content: null, atom, strings: a.strings.filter((s) => !b.strings.includes(s)) };
}

// The atom for a whole class: strings longest first, then single code
// points, then the empty string.
function classAtom(s: CSet): string {
  const long = s.strings.filter((x) => length(x) > 1).sort((x, y) => length(y) - length(x));
  const alts = long.map(literal);
  if (s.atom !== null) alts.push(s.atom);
  if (s.strings.includes("")) alts.push("");
  if (alts.length === 0) return "[]";
  if (alts.length === 1 && long.length === 0 && s.atom !== null) return s.atom;
  return `(?:${alts.join("|")})`;
}

export function unicodeSetsToUnicode(source: string, flags: string): string {
  let i = 0;
  const fail = (reason: string) => new SyntaxError(`Invalid regular expression: /${source}/${flags}: ${reason}`);
  const peek = (s: string) => source.startsWith(s, i);

  // A character escape after "\" in a class: its code point.
  function charEscape(): number {
    const c = source[i++];
    switch (c) {
      case "n": return 10;
      case "r": return 13;
      case "t": return 9;
      case "v": return 11;
      case "f": return 12;
      case "b": return 8;
      case "0":
        if (/[0-9]/.test(source[i] ?? "")) throw fail("Invalid class escape");
        return 0;
      case "c": {
        const l = source[i];
        if (!/[A-Za-z]/.test(l ?? "")) throw fail("Invalid class escape");
        i++;
        return l.charCodeAt(0) % 32;
      }
      case "x": {
        const h = source.slice(i, i + 2);
        if (!/^[0-9A-Fa-f]{2}$/.test(h)) throw fail("Invalid escape");
        i += 2;
        return parseInt(h, 16);
      }
      case "u": {
        if (source[i] === "{") {
          const end = source.indexOf("}", i);
          const h = end < 0 ? "" : source.slice(i + 1, end);
          if (!/^[0-9A-Fa-f]+$/.test(h) || parseInt(h, 16) > 0x10ffff) throw fail("Invalid Unicode escape");
          i = end + 1;
          return parseInt(h, 16);
        }
        const h = source.slice(i, i + 4);
        if (!/^[0-9A-Fa-f]{4}$/.test(h)) throw fail("Invalid Unicode escape");
        i += 4;
        const hi = parseInt(h, 16);
        const lo = source.slice(i, i + 6).match(/^\\u([dD][c-fC-F][0-9A-Fa-f]{2})$/);
        if (hi >= 0xd800 && hi <= 0xdbff && lo) {
          i += 6;
          return (hi - 0xd800) * 0x400 + (parseInt(lo[1], 16) - 0xdc00) + 0x10000;
        }
        return hi;
      }
    }
    if (c !== undefined && IDENTITY.includes(c)) return c.codePointAt(0)!;
    throw fail("Invalid escape");
  }

  // A single ClassSetCharacter: its code point.
  function setCharacter(): number {
    if (i >= source.length) throw fail("Unterminated character class");
    if (source[i] === "\\") {
      i++;
      return charEscape();
    }
    const c = source.codePointAt(i)!;
    const ch = String.fromCodePoint(c);
    if (SYNTAX.includes(ch)) throw fail("Invalid character in character class");
    if (DOUBLE.includes(ch) && source[i + 1] === ch) throw fail("Invalid set operation in character class");
    i += ch.length;
    return c;
  }

  // An operand: a nested class, a class escape, \q{…}, or a character (as
  // a code point, so the caller can make it a range).
  function operand(): CSet | number {
    if (peek("[")) {
      i++;
      return classContents();
    }
    if (peek("\\q{")) {
      i += 3;
      const strings: string[] = [];
      let cur = "";
      for (;;) {
        if (i >= source.length) throw fail("Unterminated character class");
        if (source[i] === "}") break;
        if (source[i] === "|") {
          strings.push(cur);
          cur = "";
          i++;
          continue;
        }
        cur += String.fromCodePoint(setCharacter());
      }
      strings.push(cur);
      i++;
      const singles = strings.filter((s) => length(s) === 1);
      const rest = [...new Set(strings.filter((s) => length(s) !== 1))];
      return { ...union(singles.map((s) => simple(cp(s.codePointAt(0)!)))), strings: rest };
    }
    const m = /^\\([pP])\{([^}]*)\}/.exec(source.slice(i)) ?? /^\\([dDwWsS])/.exec(source.slice(i));
    if (m) {
      i += m[0].length;
      if (m[2] !== undefined && STRING_PROPERTIES.has(m[2])) {
        if (m[1] === "P") throw fail("Invalid property name");
        return stringProperty(m[2]);
      }
      return simple(m[0]);
    }
    return setCharacter();
  }

  const asSet = (o: CSet | number): CSet => (typeof o === "number" ? simple(cp(o)) : o);

  // A property of strings: its single code points and its strings.
  function stringProperty(name: string): CSet {
    const p = host.stringProperty(name);
    if (!p) throw fail("Invalid property name");
    return { content: p.content === "" ? null : p.content, atom: p.content === "" ? null : `[${p.content}]`, strings: [...p.strings] };
  }

  // After "[": the class up to and including its "]".
  function classContents(): CSet {
    const negated = peek("^");
    if (negated) i++;
    let result: CSet;
    if (peek("]")) {
      result = union([]);
    } else {
      const first = operand();
      if (peek("&&") || peek("--")) {
        const op = source.slice(i, i + 2);
        result = asSet(first);
        while (peek(op)) {
          i += 2;
          if (peek("&") && op === "&&") throw fail("Invalid set operation in character class");
          if (peek("]")) throw fail("Invalid set operation in character class");
          const next = operand();
          if (typeof next === "number" && peek("-") && !peek("--")) throw fail("Invalid set operation in character class");
          result = op === "&&" ? intersect(result, asSet(next)) : subtract(result, asSet(next));
        }
        if (!peek("]")) throw fail("Invalid set operation in character class");
      } else {
        const parts: CSet[] = [];
        let o: CSet | number = first;
        for (;;) {
          if (typeof o === "number" && peek("-") && !peek("--")) {
            i++;
            const hi = setCharacter();
            if (hi < o) throw fail("Range out of order in character class");
            parts.push(simple(`${cp(o)}-${cp(hi)}`));
          } else {
            parts.push(asSet(o));
          }
          if (peek("]")) break;
          if (peek("&&") || peek("--")) throw fail("Invalid set operation in character class");
          o = operand();
        }
        result = union(parts);
      }
    }
    i++; // "]"
    if (!negated) return result;
    if (result.strings.length > 0) throw fail("Negated character class may contain strings");
    if (result.content !== null) return { content: null, atom: `[^${result.content}]`, strings: [] };
    return { content: null, atom: result.atom === null ? "[\\s\\S]" : `(?:(?!${result.atom})[\\s\\S])`, strings: [] };
  }

  let out = "";
  while (i < source.length) {
    const c = source[i];
    if (c === "\\") {
      // A property of strings outside a class matches as the class of it.
      const sp = /^\\p\{([^}]*)\}/.exec(source.slice(i));
      if (sp && STRING_PROPERTIES.has(sp[1])) {
        out += classAtom(stringProperty(sp[1]));
        i += sp[0].length;
        continue;
      }
      // Copy the escape whole: \p{…}, \u{…} and \k<…> hold no class.
      const m = /^\\(?:[pP]\{[^}]*\}|u\{[^}]*\}|k<[^>]*>|[\s\S])/.exec(source.slice(i))!;
      out += m[0];
      i += m[0].length;
    } else if (c === "[") {
      i++;
      out += classAtom(classContents());
    } else {
      out += c;
      i++;
    }
  }
  return out;
}
