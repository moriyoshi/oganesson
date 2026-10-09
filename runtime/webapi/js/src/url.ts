import { host, URLRecord } from "./host";
import { defineGlobal, tag } from "./util";
import { utf8Encode } from "./encoding";

// Percent-decode to bytes, then UTF-8 decode (application/x-www-form-urlencoded).
function percentDecode(s: string): string {
  const bytes = utf8Encode(s);
  const out: number[] = [];
  for (let i = 0; i < bytes.length; i++) {
    const b = bytes[i];
    if (b === 0x25 && i + 2 < bytes.length) {
      const h = String.fromCharCode(bytes[i + 1], bytes[i + 2]);
      if (/^[0-9A-Fa-f]{2}$/.test(h)) { out.push(parseInt(h, 16)); i += 2; continue; }
    }
    out.push(b);
  }
  return new TextDecoder().decode(new Uint8Array(out));
}

function formEncode(s: string): string {
  const bytes = utf8Encode(s);
  let out = "";
  for (const b of bytes) {
    if (b === 0x20) out += "+";
    else if ((b >= 0x30 && b <= 0x39) || (b >= 0x41 && b <= 0x5a) || (b >= 0x61 && b <= 0x7a) || b === 0x2a || b === 0x2d || b === 0x2e || b === 0x5f) out += String.fromCharCode(b);
    else out += "%" + b.toString(16).toUpperCase().padStart(2, "0");
  }
  return out;
}

export function parseFormUrlencoded(input: string): [string, string][] {
  const out: [string, string][] = [];
  for (const seq of input.split("&")) {
    if (!seq) continue;
    const i = seq.indexOf("=");
    const name = i >= 0 ? seq.slice(0, i) : seq;
    const value = i >= 0 ? seq.slice(i + 1) : "";
    out.push([percentDecode(name.replace(/\+/g, " ")), percentDecode(value.replace(/\+/g, " "))]);
  }
  return out;
}

export function serializeFormUrlencoded(list: [string, string][]): string {
  return list.map(([k, v]) => formEncode(k) + "=" + formEncode(v)).join("&");
}

// USVString conversion: lone surrogates become U+FFFD.
const usv = (v: unknown) => (String(v) as any).toWellFormed();

export class URLSearchParams {
  #list: [string, string][] = [];
  #url: URL | null = null;

  constructor(init: unknown = "") {
    if (typeof init === "object" && init !== null && #list in init) {
      this.#list = init.#list.map(([k, v]) => [k, v]);
    } else if (typeof init === "object" && init !== null || typeof init === "function") {
      const it = (init as any)[Symbol.iterator];
      if (typeof it === "function") {
        for (const pair of init as Iterable<any>) {
          const arr = [...pair];
          if (arr.length !== 2) throw new TypeError("Each query pair must be an iterable [name, value] tuple");
          this.#list.push([usv(arr[0]), usv(arr[1])]);
        }
      } else {
        for (const k of Reflect.ownKeys(init as object)) {
          const d = Reflect.getOwnPropertyDescriptor(init as object, k);
          if (d && d.enumerable && typeof k === "string") this.#list.push([usv(k), usv((init as any)[k])]);
        }
      }
    } else {
      let s = usv(init);
      if (s.startsWith("?")) s = s.slice(1);
      this.#list = parseFormUrlencoded(s);
    }
  }

  static _attach(p: URLSearchParams, url: URL, query: string) {
    p.#url = url;
    p.#list = parseFormUrlencoded(query);
  }
  static _reset(p: URLSearchParams, query: string) { p.#list = parseFormUrlencoded(query); }

  #update() {
    if (!this.#url) return;
    const s = this.toString();
    URL._setQueryFromParams(this.#url, s);
  }

  get size() { return this.#list.length; }
  append(name: string, value: string) { this.#list.push([usv(name), usv(value)]); this.#update(); }
  delete(name: string, value?: string) {
    name = usv(name);
    const v = value === undefined ? undefined : usv(value);
    this.#list = this.#list.filter(([k, x]) => !(k === name && (v === undefined || x === v)));
    this.#update();
  }
  get(name: string) { name = usv(name); const e = this.#list.find(([k]) => k === name); return e ? e[1] : null; }
  getAll(name: string) { name = usv(name); return this.#list.filter(([k]) => k === name).map(([, v]) => v); }
  has(name: string, value?: string) {
    name = usv(name);
    const v = value === undefined ? undefined : usv(value);
    return this.#list.some(([k, x]) => k === name && (v === undefined || x === v));
  }
  set(name: string, value: string) {
    name = usv(name); value = usv(value);
    const i = this.#list.findIndex(([k]) => k === name);
    if (i < 0) this.#list.push([name, value]);
    else {
      this.#list[i][1] = value;
      this.#list = this.#list.filter(([k], j) => j <= i || k !== name);
    }
    this.#update();
  }
  sort() {
    // Stable sort by UTF-16 code units of the name.
    this.#list = this.#list.map((e, i) => [e, i] as const)
      .sort((a, b) => (a[0][0] < b[0][0] ? -1 : a[0][0] > b[0][0] ? 1 : a[1] - b[1]))
      .map(([e]) => e);
    this.#update();
  }
  forEach(cb: (v: string, k: string, p: URLSearchParams) => void, thisArg?: unknown) {
    for (let i = 0; i < this.#list.length; i++) cb.call(thisArg, this.#list[i][1], this.#list[i][0], this);
  }
  *keys() { for (let i = 0; i < this.#list.length; i++) yield this.#list[i][0]; }
  *values() { for (let i = 0; i < this.#list.length; i++) yield this.#list[i][1]; }
  *entries(): IterableIterator<[string, string]> { for (let i = 0; i < this.#list.length; i++) yield [this.#list[i][0], this.#list[i][1]]; }
  [Symbol.iterator]() { return this.entries(); }
  toString() { return serializeFormUrlencoded(this.#list); }
}
tag(URLSearchParams, "URLSearchParams");

const blobURLs = new Map<string, unknown>();

export class URL {
  #r: URLRecord;
  #params: URLSearchParams;

  constructor(url: unknown, base?: unknown) {
    if (arguments.length === 0) throw new TypeError("URL constructor requires 1 argument");
    const r = host.urlParse(usv(url), base === undefined ? undefined : usv(base));
    if (!r) throw new TypeError(`Invalid URL: ${String(url)}`);
    this.#r = r;
    this.#params = new URLSearchParams();
    URLSearchParams._attach(this.#params, this, r.search.slice(1));
  }
  static canParse(url: unknown, base?: unknown) {
    return host.urlParse(usv(url), base === undefined ? undefined : usv(base)) !== null;
  }
  static parse(url: unknown, base?: unknown) {
    try { return new URL(url, base); } catch { return null; }
  }
  static createObjectURL(obj: unknown) {
    const u = "blob:null/" + host.randomUUID();
    blobURLs.set(u, obj);
    return u;
  }
  static revokeObjectURL(u: string) { blobURLs.delete(String(u)); }
  static _resolveBlob(u: string) { return blobURLs.get(u); }
  static _setQueryFromParams(url: URL, query: string) {
    // The search setter maps "" to a null query, matching the spec's
    // "update steps" for an empty serialization.
    const r = host.urlSet(url.#r.href, "search", query);
    if (r) url.#r = r;
  }

  #set(component: string, value: unknown) {
    const r = host.urlSet(this.#r.href, component, usv(value));
    if (r) this.#r = r;
    if (component === "search") URLSearchParams._reset(this.#params, this.#r.search.slice(1));
  }

  get href() { return this.#r.href; }
  set href(v) {
    const r = host.urlParse(usv(v));
    if (!r) throw new TypeError(`Invalid URL: ${String(v)}`);
    this.#r = r;
    URLSearchParams._reset(this.#params, r.search.slice(1));
  }
  get origin() { return this.#r.origin; }
  get protocol() { return this.#r.protocol; }
  set protocol(v) { this.#set("protocol", v); }
  get username() { return this.#r.username; }
  set username(v) { this.#set("username", v); }
  get password() { return this.#r.password; }
  set password(v) { this.#set("password", v); }
  get host() { return this.#r.host; }
  set host(v) { this.#set("host", v); }
  get hostname() { return this.#r.hostname; }
  set hostname(v) { this.#set("hostname", v); }
  get port() { return this.#r.port; }
  set port(v) { this.#set("port", v); }
  get pathname() { return this.#r.pathname; }
  set pathname(v) { this.#set("pathname", v); }
  get search() { return this.#r.search; }
  set search(v) { this.#set("search", v); }
  get searchParams() { return this.#params; }
  get hash() { return this.#r.hash; }
  set hash(v) { this.#set("hash", v); }
  toString() { return this.#r.href; }
  toJSON() { return this.#r.href; }
}
tag(URL, "URL");

export function installURL() {
  defineGlobal("URL", URL);
  defineGlobal("URLSearchParams", URLSearchParams);
}
