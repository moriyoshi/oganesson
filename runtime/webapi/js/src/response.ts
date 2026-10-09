import { Headers } from "./headers";
import { URL } from "./url";
import { host } from "./host";
import { extractBody, initBody, cloneBody, applyBodyMixin, BodyState } from "./body";
import { defineGlobal, tag } from "./util";
import { utf8Encode } from "./encoding";
import { streamFromBytes } from "./body";

export const nullBodyStatus = new Set([101, 103, 204, 205, 304]);
const redirectStatus = new Set([301, 302, 303, 307, 308]);
const kInternal = Symbol("internal");

type ResponseType = "basic" | "cors" | "default" | "error" | "opaque" | "opaqueredirect";

export interface ResponseInit { status?: number; statusText?: string; headers?: unknown }

export class Response {
  #type: ResponseType = "default";
  #url = "";
  #redirected = false;
  #status = 200;
  #statusText = "";
  #headers!: Headers;

  constructor(body: unknown = null, init: ResponseInit = {}) {
    if (body === kInternal) return;
    init = init ?? {};
    const status = init.status === undefined ? 200 : Number(init.status);
    if (!Number.isInteger(status) || status < 200 || status > 599) {
      throw new RangeError(`Failed to construct 'Response': The status provided (${init.status}) is outside the range [200, 599].`);
    }
    const statusText = init.statusText === undefined ? "" : String(init.statusText);
    if (!/^[\t\x20-\x7e\x80-\xff]*$/.test(statusText)) throw new TypeError("Invalid statusText");
    this.#status = status;
    this.#statusText = statusText;
    this.#headers = new Headers(init.headers);
    Headers._setGuard(this.#headers, "response");
    let state: BodyState | null = null;
    if (body !== null && body !== undefined) {
      if (nullBodyStatus.has(status)) throw new TypeError(`Response with null body status (${status}) cannot have body`);
      const ex = extractBody(body);
      state = ex.body;
      if (ex.contentType && !this.#headers.has("content-type")) this.#headers.append("content-type", ex.contentType);
    }
    initBody(this, state, this.#headers);
  }

  static _network(p: { status: number; statusText: string; headers: [string, string][]; url: string; redirected: boolean; stream: ReadableStream<Uint8Array> | null }) {
    const r = new Response(kInternal);
    r.#type = "basic";
    r.#status = p.status;
    r.#statusText = p.statusText;
    r.#url = p.url;
    r.#redirected = p.redirected;
    r.#headers = new Headers(p.headers);
    Headers._setGuard(r.#headers, "immutable");
    initBody(r, p.stream ? { stream: p.stream, source: null } : null, r.#headers);
    return r;
  }

  static error() {
    const r = new Response(kInternal);
    r.#type = "error";
    r.#status = 0;
    r.#headers = new Headers();
    Headers._setGuard(r.#headers, "immutable");
    initBody(r, null, r.#headers);
    return r;
  }

  static redirect(url: unknown, status = 302) {
    let parsed: URL;
    try { parsed = new URL(String(url), host.baseURL || undefined); } catch { throw new TypeError(`Failed to parse URL from ${String(url)}`); }
    if (!redirectStatus.has(status)) throw new RangeError(`Invalid status code ${status}`);
    const r = new Response(kInternal);
    r.#status = status;
    r.#headers = new Headers({ location: parsed.href });
    Headers._setGuard(r.#headers, "immutable");
    initBody(r, null, r.#headers);
    return r;
  }

  static json(data: unknown, init: ResponseInit = {}) {
    const text = JSON.stringify(data);
    if (text === undefined) throw new TypeError("Value is not JSON serializable");
    const r = new Response(null, init);
    if (nullBodyStatus.has(r.#status)) throw new TypeError("Response with null body status cannot have body");
    const bytes = utf8Encode(text);
    initBody(r, { stream: streamFromBytes(bytes), source: bytes }, r.#headers);
    if (!r.#headers.has("content-type")) r.#headers.set("content-type", "application/json");
    return r;
  }

  get type() { return this.#type; }
  get url() { return this.#url; }
  get redirected() { return this.#redirected; }
  get status() { return this.#status; }
  get ok() { return this.#status >= 200 && this.#status <= 299; }
  get statusText() { return this.#statusText; }
  get headers() { return this.#headers; }

  clone(): Response {
    const body = cloneBody(this);
    const r = new Response(kInternal);
    r.#type = this.#type; r.#url = this.#url; r.#redirected = this.#redirected;
    r.#status = this.#status; r.#statusText = this.#statusText;
    r.#headers = new Headers(this.#headers);
    Headers._setGuard(r.#headers, this.#type === "default" ? "response" : "immutable");
    initBody(r, body, r.#headers);
    return r;
  }
}
applyBodyMixin(Response);
tag(Response, "Response");

export function installResponse() {
  defineGlobal("Response", Response);
}
