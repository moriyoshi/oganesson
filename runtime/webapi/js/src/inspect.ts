// A compact subset of Node's util.inspect, enough for readable console output.
export function inspect(value: unknown, opts: { depth?: number } = {}): string {
  return fmt(value, opts.depth ?? 2, new Set(), 0, true);
}

const ident = /^[A-Za-z_$][\w$]*$/;

function quote(s: string) {
  return "'" + s.replace(/\\/g, "\\\\").replace(/'/g, "\\'").replace(/\n/g, "\\n") + "'";
}

function key(k: PropertyKey) {
  if (typeof k === "symbol") return `[${k.toString()}]`;
  return ident.test(k as string) ? (k as string) : quote(String(k));
}

function fmt(v: unknown, depth: number, seen: Set<unknown>, indent: number, top: boolean): string {
  switch (typeof v) {
    case "string": return top ? v : quote(v);
    case "number": return Object.is(v, -0) ? "-0" : String(v);
    case "bigint": return `${v}n`;
    case "boolean": case "undefined": return String(v);
    case "symbol": return v.toString();
    case "function": {
      const name = (v as Function).name;
      const isClass = /^class\b/.test(Function.prototype.toString.call(v));
      return `[${isClass ? "class" : "Function"}${name ? ": " + name : " (anonymous)"}]`;
    }
  }
  if (v === null) return "null";
  const o = v as any;
  if (seen.has(o)) return "[Circular *1]";
  if (o instanceof Error) return o.stack || `${o.name}: ${o.message}`;
  if (o instanceof Date) return isNaN(o.getTime()) ? "Invalid Date" : o.toISOString();
  if (o instanceof RegExp) return String(o);
  if (o instanceof Promise) return "Promise { <unknown> }";
  if (o instanceof WeakMap) return "WeakMap { <items unknown> }";
  if (o instanceof WeakSet) return "WeakSet { <items unknown> }";
  if (o instanceof ArrayBuffer) return `ArrayBuffer { byteLength: ${o.byteLength} }`;
  const pad = "  ".repeat(indent + 1), end = "  ".repeat(indent);
  const wrapItems = (open: string, items: string[], close: string) => {
    if (items.length === 0) return `${open}${close}`;
    const flat = `${open} ${items.join(", ")} ${close}`;
    return flat.length <= 72 && !flat.includes("\n") ? flat : `${open}\n${pad}${items.join(`,\n${pad}`)}\n${end}${close}`;
  };
  if (depth < 0) return Array.isArray(o) ? "[Array]" : `[${ctorName(o) || "Object"}]`;
  seen.add(o);
  try {
    const sub = (x: unknown) => fmt(x, depth - 1, seen, indent + 1, false);
    if (Array.isArray(o)) {
      const items = o.slice(0, 100).map(sub);
      if (o.length > 100) items.push(`... ${o.length - 100} more items`);
      return wrapItems("[", items, "]");
    }
    if (ArrayBuffer.isView(o) && !(o instanceof DataView)) {
      const arr = Array.from(o as any as ArrayLike<number>).slice(0, 100).map((x) => String(x));
      return wrapItems(`${ctorName(o)}(${(o as any).length}) [`, arr, "]");
    }
    if (o instanceof Map) {
      return wrapItems(`Map(${o.size}) {`, [...o].map(([k, x]) => `${sub(k)} => ${sub(x)}`), "}");
    }
    if (o instanceof Set) return wrapItems(`Set(${o.size}) {`, [...o].map(sub), "}");
    const name = ctorName(o);
    const keys: PropertyKey[] = [...Object.keys(o), ...Object.getOwnPropertySymbols(o).filter((s) => Object.getOwnPropertyDescriptor(o, s)!.enumerable)];
    const items = keys.map((k) => {
      const d = Object.getOwnPropertyDescriptor(o, k)!;
      const val = d.get ? (d.set ? "[Getter/Setter]" : "[Getter]") : sub(d.value);
      return `${key(k)}: ${val}`;
    });
    const prefix = name && name !== "Object" ? `${name} ` : (Object.getPrototypeOf(o) === null ? "[Object: null prototype] " : "");
    const tagName = o[Symbol.toStringTag];
    if (items.length === 0 && typeof tagName === "string" && name !== tagName) return `${prefix}[${tagName}] {}`;
    return prefix + wrapItems("{", items, "}");
  } finally {
    seen.delete(o);
  }
}

function ctorName(o: any): string {
  try {
    const c = Object.getPrototypeOf(o)?.constructor;
    return typeof c === "function" ? c.name : "";
  } catch { return ""; }
}

// format implements console's printf-style substitutions.
export function format(args: unknown[]): string {
  if (args.length === 0) return "";
  const first = args[0];
  let rest = args.slice(1);
  let out = "";
  if (typeof first === "string") {
    out = first.replace(/%([sdifjoOc%])/g, (m, c) => {
      if (c === "%") return "%";
      if (rest.length === 0) return m;
      const a = rest.shift();
      switch (c) {
        case "s": return typeof a === "string" ? a : typeof a === "bigint" ? `${a}n` : (typeof a === "object" && a !== null ? inspect(a, { depth: 1 }) : String(a));
        case "d": case "i": {
          if (typeof a === "bigint") return `${a}n`;
          const n = Number(a);
          return String(c === "i" ? Math.trunc(n) : n);
        }
        case "f": return String(parseFloat(a as any));
        case "j": try { return JSON.stringify(a); } catch { return "[Circular]"; }
        case "o": case "O": return inspect(a, { depth: c === "o" ? 4 : 2 });
        case "c": return "";
      }
      return m;
    });
  } else {
    rest = args;
  }
  const parts = typeof first === "string" ? [out] : [];
  for (const a of rest) parts.push(inspect(a));
  return parts.join(" ");
}
