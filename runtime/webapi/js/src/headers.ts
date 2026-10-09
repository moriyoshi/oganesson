import { defineGlobal, tag } from "./util";

export type Guard = "none" | "request" | "response" | "immutable" | "request-no-cors";

const token = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;
// eslint-disable-next-line no-control-regex
const badValue = /[\0\r\n]/;

function normalizeValue(v: unknown) {
  return String(v).replace(/^[\t\n\r ]+|[\t\n\r ]+$/g, "");
}
function checkName(name: string) {
  if (!token.test(name)) throw new TypeError(`Invalid header name: "${name}"`);
}
function checkValue(value: string) {
  if (badValue.test(value) || /[^\x00-\xff]/.test(value)) throw new TypeError(`Invalid header value: "${value}"`);
}

export class Headers {
  // Insertion-ordered list of [lowercased name, value].
  #list: [string, string][] = [];
  #guard: Guard = "none";

  constructor(init?: unknown) {
    if (init === undefined || init === null) return;
    if (typeof init !== "object" && typeof init !== "function") throw new TypeError("Headers: init must be an object");
    // A brand check, not instanceof: a class whose prototype chain merely
    // includes Headers.prototype (srvx's Node request headers) has no #list
    // and is read as the iterable it is, as undici does.
    if (typeof init === "object" && #list in init) {
      for (const [k, v] of init.#list) this.#list.push([k, v]);
      return;
    }
    const it = (init as any)[Symbol.iterator];
    if (typeof it === "function") {
      for (const pair of init as Iterable<any>) {
        if (typeof pair !== "object" || pair === null) throw new TypeError("Headers: each pair must be a sequence");
        const arr = [...pair];
        if (arr.length !== 2) throw new TypeError("Headers: each pair must have exactly two items");
        this.append(arr[0], arr[1]);
      }
    } else {
      for (const k of Reflect.ownKeys(init as object)) {
        const d = Reflect.getOwnPropertyDescriptor(init as object, k);
        if (d && d.enumerable) {
          if (typeof k === "symbol") throw new TypeError("Headers: symbol keys are not allowed");
          this.append(k, (init as any)[k]);
        }
      }
    }
  }

  static _setGuard(h: Headers, g: Guard) { h.#guard = g; }
  static _list(h: Headers) { return h.#list; }

  #mutable() {
    if (this.#guard === "immutable") throw new TypeError("Headers are immutable");
  }

  append(name: string, value: string) {
    name = String(name); value = normalizeValue(value);
    checkName(name); checkValue(value);
    this.#mutable();
    const lower = name.toLowerCase();
    this.#list.push([lower, value]);
  }
  delete(name: string) {
    name = String(name); checkName(name); this.#mutable();
    const lower = name.toLowerCase();
    this.#list = this.#list.filter(([k]) => k !== lower);
  }
  get(name: string): string | null {
    name = String(name); checkName(name);
    const lower = name.toLowerCase();
    const vals = this.#list.filter(([k]) => k === lower).map(([, v]) => v);
    return vals.length ? vals.join(", ") : null;
  }
  getSetCookie(): string[] { return this.#list.filter(([k]) => k === "set-cookie").map(([, v]) => v); }
  has(name: string) { name = String(name); checkName(name); const l = name.toLowerCase(); return this.#list.some(([k]) => k === l); }
  set(name: string, value: string) {
    name = String(name); value = normalizeValue(value);
    checkName(name); checkValue(value);
    this.#mutable();
    const lower = name.toLowerCase();
    const i = this.#list.findIndex(([k]) => k === lower);
    if (i < 0) this.#list.push([lower, value]);
    else this.#list = [...this.#list.slice(0, i), [lower, value] as [string, string], ...this.#list.slice(i + 1).filter(([k]) => k !== lower)];
  }
  // "sort and combine": sorted by name, values combined except set-cookie.
  #sorted(): [string, string][] {
    const names = [...new Set(this.#list.map(([k]) => k))].sort();
    const out: [string, string][] = [];
    for (const n of names) {
      if (n === "set-cookie") for (const v of this.getSetCookie()) out.push([n, v]);
      else out.push([n, this.get(n)!]);
    }
    return out;
  }
  forEach(cb: (v: string, k: string, h: Headers) => void, thisArg?: unknown) {
    for (const [k, v] of this.#sorted()) cb.call(thisArg, v, k, this);
  }
  *keys() { for (const [k] of this.#sorted()) yield k; }
  *values() { for (const [, v] of this.#sorted()) yield v; }
  *entries(): IterableIterator<[string, string]> { yield* this.#sorted(); }
  [Symbol.iterator]() { return this.entries(); }
}
tag(Headers, "Headers");

export function installHeaders() {
  defineGlobal("Headers", Headers);
}
