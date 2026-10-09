import { Headers } from "./headers";
import { AbortSignal, newAbortSignal } from "./abort";
import { URL } from "./url";
import { host } from "./host";
import { extractBody, initBody, bodyState, cloneBody, applyBodyMixin, isDisturbed, BodyState } from "./body";
import { defineGlobal, tag } from "./util";

const token = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;
const normalized = new Set(["DELETE", "GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH"]);
const forbidden = new Set(["CONNECT", "TRACE", "TRACK"]);

const kClone = Symbol("clone");

export interface RequestInit {
  method?: string; headers?: unknown; body?: unknown; signal?: AbortSignal | null;
  redirect?: RequestRedirect; cache?: string; credentials?: string; mode?: string;
  referrer?: string; referrerPolicy?: string; integrity?: string; keepalive?: boolean;
  duplex?: "half"; window?: null;
}

type RequestRedirect = "follow" | "error" | "manual";

export class Request {
  #method = "GET";
  #url = "";
  #headers!: Headers;
  #signal!: AbortSignal;
  #redirect: RequestRedirect = "follow";
  #cache = "default";
  #credentials = "same-origin";
  #mode = "cors";
  #referrer = "about:client";
  #referrerPolicy = "";
  #integrity = "";
  #keepalive = false;

  constructor(input: unknown, init: RequestInit = {}) {
    if (arguments.length === 0) throw new TypeError("Request constructor requires 1 argument");
    if ((input as any) === kClone) return;
    init = init ?? {};
    let base: Request | null = null;
    // A brand check (see Headers): a lookalike with Request.prototype in
    // its chain has none of the fields read below.
    if (typeof input === "object" && input !== null && #url in input) {
      base = input;
      this.#url = base.#url;
      this.#method = base.#method;
      this.#redirect = base.#redirect;
      this.#cache = base.#cache;
      this.#credentials = base.#credentials;
      this.#mode = base.#mode;
      this.#referrer = base.#referrer;
      this.#referrerPolicy = base.#referrerPolicy;
      this.#integrity = base.#integrity;
      this.#keepalive = base.#keepalive;
    } else {
      let u: URL;
      try { u = new URL(String(input), host.baseURL || undefined); } catch { throw new TypeError(`Failed to parse URL from ${String(input)}`); }
      if (u.username || u.password) throw new TypeError(`Request cannot be constructed from a URL that includes credentials: ${u.href}`);
      this.#url = u.href;
    }

    if (init.method !== undefined) {
      const m = String(init.method);
      if (!token.test(m)) throw new TypeError(`'${m}' is not a valid HTTP method.`);
      const upper = m.toUpperCase();
      if (forbidden.has(upper)) throw new TypeError(`'${m}' HTTP method is unsupported.`);
      this.#method = normalized.has(upper) ? upper : m;
    }
    if (init.redirect !== undefined) {
      if (!["follow", "error", "manual"].includes(init.redirect)) throw new TypeError(`Invalid redirect mode: ${init.redirect}`);
      this.#redirect = init.redirect;
    }
    if (init.cache !== undefined) this.#cache = String(init.cache);
    if (init.credentials !== undefined) this.#credentials = String(init.credentials);
    if (init.mode !== undefined) {
      if (init.mode === "navigate") throw new TypeError("Request mode cannot be 'navigate'");
      this.#mode = String(init.mode);
    }
    if (init.referrer !== undefined) this.#referrer = init.referrer === "" ? "" : String(init.referrer);
    if (init.referrerPolicy !== undefined) this.#referrerPolicy = String(init.referrerPolicy);
    if (init.integrity !== undefined) this.#integrity = String(init.integrity);
    if (init.keepalive !== undefined) this.#keepalive = !!init.keepalive;

    // Signal: a dependent signal following init.signal or the base request's.
    const parent = init.signal !== undefined ? init.signal : base ? base.#signal : null;
    if (parent != null && !(parent instanceof AbortSignal)) throw new TypeError("Request signal must be an AbortSignal");
    this.#signal = parent ? AbortSignal.any([parent]) : newAbortSignal();

    this.#headers = new Headers(init.headers !== undefined ? init.headers : base ? base.#headers : undefined);
    Headers._setGuard(this.#headers, "request");

    const initBodyGiven = init.body !== undefined && init.body !== null;
    const baseBody = base ? bodyState(base).body : null;
    if ((initBodyGiven || baseBody) && (this.#method === "GET" || this.#method === "HEAD")) {
      throw new TypeError("Request with GET/HEAD method cannot have body.");
    }
    let body: BodyState | null = null;
    if (initBodyGiven) {
      const ex = extractBody(init.body);
      body = ex.body;
      if (ex.contentType && !this.#headers.has("content-type")) this.#headers.append("content-type", ex.contentType);
    } else if (baseBody) {
      if (baseBody.stream.locked || isDisturbed(baseBody.stream)) throw new TypeError("Cannot construct a Request with a Request object that has already been used.");
      // Proxy the base body: reading this request disturbs the original.
      body = { stream: baseBody.stream.pipeThrough(new TransformStream()), source: baseBody.source };
    }
    initBody(this, body, this.#headers);
  }

  get method() { return this.#method; }
  get url() { return this.#url; }
  get headers() { return this.#headers; }
  get signal() { return this.#signal; }
  get redirect() { return this.#redirect; }
  get cache() { return this.#cache; }
  get credentials() { return this.#credentials; }
  get mode() { return this.#mode; }
  get referrer() { return this.#referrer === "about:client" ? "about:client" : this.#referrer; }
  get referrerPolicy() { return this.#referrerPolicy; }
  get integrity() { return this.#integrity; }
  get keepalive() { return this.#keepalive; }
  get destination() { return ""; }
  get isHistoryNavigation() { return false; }
  get isReloadNavigation() { return false; }
  get duplex() { return "half"; }

  clone(): Request {
    const body = cloneBody(this);
    const r = new Request(kClone);
    r.#method = this.#method; r.#url = this.#url; r.#redirect = this.#redirect; r.#cache = this.#cache;
    r.#credentials = this.#credentials; r.#mode = this.#mode; r.#referrer = this.#referrer;
    r.#referrerPolicy = this.#referrerPolicy; r.#integrity = this.#integrity; r.#keepalive = this.#keepalive;
    r.#headers = new Headers(this.#headers);
    Headers._setGuard(r.#headers, "request");
    r.#signal = AbortSignal.any([this.#signal]);
    initBody(r, body, r.#headers);
    return r;
  }
}
applyBodyMixin(Request);
tag(Request, "Request");

export function installRequest() {
  defineGlobal("Request", Request);
}
