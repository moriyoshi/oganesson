import { host } from "./host";
import { DOMException } from "./events";
import { defineGlobal, tag, toBytes, isBufferSource } from "./util";

export function utf8Encode(s: string): Uint8Array {
  return new Uint8Array(host.utf8Encode(String(s)));
}

export function utf8Decode(b: Uint8Array): string {
  return new TextDecoder().decode(b);
}

export class TextEncoder {
  get encoding() { return "utf-8"; }
  encode(input = "") { return utf8Encode(String(input)); }
  encodeInto(src: string, dest: Uint8Array) {
    if (!(dest instanceof Uint8Array)) throw new TypeError("encodeInto: destination must be a Uint8Array");
    // Encode code point by code point so that `read` counts UTF-16 units
    // fully written, as the spec requires.
    const s = String(src);
    let read = 0, written = 0;
    for (const ch of s) {
      const bytes = utf8Encode(ch);
      if (written + bytes.length > dest.length) break;
      dest.set(bytes, written);
      written += bytes.length;
      read += ch.length;
    }
    return { read, written };
  }
}
tag(TextEncoder, "TextEncoder");

export class TextDecoder {
  #name: string;
  #fatal: boolean;
  #ignoreBOM: boolean;
  #decoder: { decode(b: Uint8Array, stream: boolean): string };
  #bomPending = true;
  constructor(label: string = "utf-8", options: { fatal?: boolean; ignoreBOM?: boolean } = {}) {
    const d = host.newDecoder(String(label), !!options.fatal);
    if (!d) throw new RangeError(`The encoding label provided ('${label}') is invalid.`);
    this.#name = d.name;
    this.#decoder = d;
    this.#fatal = !!options.fatal;
    this.#ignoreBOM = !!options.ignoreBOM;
  }
  get encoding() { return this.#name; }
  get fatal() { return this.#fatal; }
  get ignoreBOM() { return this.#ignoreBOM; }
  decode(input?: ArrayBuffer | ArrayBufferView, options: { stream?: boolean } = {}) {
    if (input !== undefined && !isBufferSource(input)) throw new TypeError("decode: input must be a BufferSource");
    const stream = !!options.stream;
    let out = this.#decoder.decode(toBytes(input as any), stream);
    if (this.#bomPending && out.length > 0) {
      this.#bomPending = false;
      if (!this.#ignoreBOM && out.charCodeAt(0) === 0xfeff && /^utf-(8|16le|16be)$/.test(this.#name)) out = out.slice(1);
    }
    if (!stream) this.#bomPending = true;
    return out;
  }
}
tag(TextDecoder, "TextDecoder");

class TextEncoderStream {
  #transform: TransformStream<string, Uint8Array>;
  constructor() {
    let pendingHigh = "";
    this.#transform = new TransformStream({
      transform(chunk, controller) {
        let s = pendingHigh + String(chunk);
        pendingHigh = "";
        const last = s.charCodeAt(s.length - 1);
        if (last >= 0xd800 && last <= 0xdbff) { pendingHigh = s[s.length - 1]; s = s.slice(0, -1); }
        if (s) controller.enqueue(utf8Encode(s));
      },
      flush(controller) { if (pendingHigh) controller.enqueue(utf8Encode("�")); },
    });
  }
  get encoding() { return "utf-8"; }
  get readable() { return this.#transform.readable; }
  get writable() { return this.#transform.writable; }
}
tag(TextEncoderStream, "TextEncoderStream");

class TextDecoderStream {
  #transform: TransformStream<BufferSource, string>;
  #decoder: TextDecoder;
  constructor(label = "utf-8", options: { fatal?: boolean; ignoreBOM?: boolean } = {}) {
    const dec = (this.#decoder = new TextDecoder(label, options));
    this.#transform = new TransformStream({
      transform(chunk, controller) {
        const s = dec.decode(chunk as any, { stream: true });
        if (s) controller.enqueue(s);
      },
      flush(controller) {
        const s = dec.decode();
        if (s) controller.enqueue(s);
      },
    });
  }
  get encoding() { return this.#decoder.encoding; }
  get fatal() { return this.#decoder.fatal; }
  get ignoreBOM() { return this.#decoder.ignoreBOM; }
  get readable() { return this.#transform.readable; }
  get writable() { return this.#transform.writable; }
}
tag(TextDecoderStream, "TextDecoderStream");

const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

function btoa(data: unknown) {
  if (arguments.length === 0) throw new TypeError("btoa requires 1 argument");
  const s = String(data);
  let out = "";
  for (let i = 0; i < s.length; i += 3) {
    const a = s.charCodeAt(i), b = s.charCodeAt(i + 1), c = s.charCodeAt(i + 2);
    if (a > 255 || b > 255 || c > 255) throw new DOMException("Invalid character", "InvalidCharacterError");
    const n = (a << 16) | ((b || 0) << 8) | (c || 0);
    out += b64[n >> 18] + b64[(n >> 12) & 63] + (i + 1 < s.length ? b64[(n >> 6) & 63] : "=") + (i + 2 < s.length ? b64[n & 63] : "=");
  }
  return out;
}

// atob implements the "forgiving-base64 decode" algorithm.
function atob(data: unknown) {
  if (arguments.length === 0) throw new TypeError("atob requires 1 argument");
  let s = String(data).replace(/[\t\n\f\r ]/g, "");
  if (s.length % 4 === 0) s = s.replace(/==?$/, "");
  if (s.length % 4 === 1 || /[^A-Za-z0-9+/]/.test(s)) throw new DOMException("The string to be decoded is not correctly encoded.", "InvalidCharacterError");
  let out = "", buf = 0, bits = 0;
  for (const ch of s) {
    buf = (buf << 6) | b64.indexOf(ch);
    bits += 6;
    if (bits >= 8) { bits -= 8; out += String.fromCharCode((buf >> bits) & 0xff); }
  }
  return out;
}

export function installEncoding() {
  defineGlobal("TextEncoder", TextEncoder);
  defineGlobal("TextDecoder", TextDecoder);
  defineGlobal("TextEncoderStream", TextEncoderStream);
  defineGlobal("TextDecoderStream", TextDecoderStream);
  defineGlobal("btoa", btoa);
  defineGlobal("atob", atob);
}
