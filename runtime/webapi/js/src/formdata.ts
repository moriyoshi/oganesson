import { Blob, File, blobBytes } from "./blob";
import { defineGlobal, tag, concatBytes } from "./util";
import { utf8Encode } from "./encoding";
import { host } from "./host";

type Entry = [string, string | File];

const usv = (v: unknown) => (String(v) as any).toWellFormed();

function toEntryValue(value: unknown, filename?: string): string | File {
  if (value instanceof Blob) {
    if (!(value instanceof File) || filename !== undefined) {
      return new File([value], filename !== undefined ? String(filename) : value instanceof File ? value.name : "blob", {
        type: value.type,
        lastModified: value instanceof File ? value.lastModified : undefined,
      });
    }
    return value;
  }
  return usv(value);
}

export class FormData {
  #list: Entry[] = [];
  constructor(form?: unknown) {
    if (form !== undefined) throw new TypeError("FormData: HTMLFormElement is not supported on the server");
  }
  append(name: string, value: unknown, filename?: string) {
    if (arguments.length < 2) throw new TypeError("FormData.append requires 2 arguments");
    this.#list.push([usv(name), toEntryValue(value, filename)]);
  }
  delete(name: string) { name = usv(name); this.#list = this.#list.filter(([k]) => k !== name); }
  get(name: string) { name = usv(name); const e = this.#list.find(([k]) => k === name); return e ? e[1] : null; }
  getAll(name: string) { name = usv(name); return this.#list.filter(([k]) => k === name).map(([, v]) => v); }
  has(name: string) { name = usv(name); return this.#list.some(([k]) => k === name); }
  set(name: string, value: unknown, filename?: string) {
    if (arguments.length < 2) throw new TypeError("FormData.set requires 2 arguments");
    name = usv(name);
    const entry: Entry = [name, toEntryValue(value, filename)];
    const i = this.#list.findIndex(([k]) => k === name);
    if (i < 0) this.#list.push(entry);
    else this.#list = [...this.#list.slice(0, i), entry, ...this.#list.slice(i + 1).filter(([k]) => k !== name)];
  }
  forEach(cb: (v: string | File, k: string, f: FormData) => void, thisArg?: unknown) {
    for (const [k, v] of this.#list.slice()) cb.call(thisArg, v, k, this);
  }
  *keys() { for (const [k] of this.#list) yield k; }
  *values() { for (const [, v] of this.#list) yield v; }
  *entries(): IterableIterator<Entry> { for (const e of this.#list) yield [e[0], e[1]]; }
  [Symbol.iterator]() { return this.entries(); }
}
tag(FormData, "FormData");

const escapeName = (s: string) => s.replace(/\n/g, "%0A").replace(/\r/g, "%0D").replace(/"/g, "%22");

// encodeMultipart serializes form data per the HTML multipart/form-data rules.
export function encodeMultipart(fd: FormData): { body: Uint8Array; contentType: string } {
  const boundary = "----ogFormBoundary" + host.randomUUID().replace(/-/g, "");
  const parts: Uint8Array[] = [];
  for (const [name, value] of fd) {
    let head = `--${boundary}\r\nContent-Disposition: form-data; name="${escapeName(name.replace(/\r\n?|\n/g, "\r\n"))}"`;
    if (typeof value === "string") {
      parts.push(utf8Encode(head + "\r\n\r\n" + value.replace(/\r\n?|\n/g, "\r\n") + "\r\n"));
    } else {
      head += `; filename="${escapeName(value.name)}"\r\nContent-Type: ${value.type || "application/octet-stream"}\r\n\r\n`;
      parts.push(utf8Encode(head), blobBytes(value), utf8Encode("\r\n"));
    }
  }
  parts.push(utf8Encode(`--${boundary}--\r\n`));
  return { body: concatBytes(parts), contentType: `multipart/form-data; boundary=${boundary}` };
}

function indexOf(hay: Uint8Array, needle: Uint8Array, from: number) {
  outer: for (let i = from; i <= hay.length - needle.length; i++) {
    for (let j = 0; j < needle.length; j++) if (hay[i + j] !== needle[j]) continue outer;
    return i;
  }
  return -1;
}

function param(header: string, name: string): string | undefined {
  const m = new RegExp(`;\\s*${name}\\*?=\\s*(?:"((?:[^"\\\\]|\\\\.)*)"|([^;\\s]*))`, "i").exec(header);
  if (!m) return undefined;
  return m[1] !== undefined ? m[1].replace(/\\(.)/g, "$1") : m[2];
}

// parseMultipart decodes a multipart/form-data body into a FormData.
export function parseMultipart(body: Uint8Array, contentType: string): FormData {
  const boundary = param(contentType, "boundary");
  if (!boundary) throw new TypeError("Missing multipart boundary");
  const delim = utf8Encode("--" + boundary);
  const fd = new FormData();
  const dec = new TextDecoder();
  let pos = indexOf(body, delim, 0);
  if (pos < 0) throw new TypeError("Malformed multipart body");
  for (;;) {
    pos += delim.length;
    if (body[pos] === 0x2d && body[pos + 1] === 0x2d) break; // closing "--"
    if (body[pos] === 0x0d && body[pos + 1] === 0x0a) pos += 2;
    const headerEnd = indexOf(body, utf8Encode("\r\n\r\n"), pos);
    if (headerEnd < 0) throw new TypeError("Malformed multipart body");
    const headers = dec.decode(body.subarray(pos, headerEnd)).split("\r\n");
    const next = indexOf(body, delim, headerEnd + 4);
    if (next < 0) throw new TypeError("Malformed multipart body");
    const content = body.subarray(headerEnd + 4, next - 2); // strip CRLF before delimiter
    let disposition = "", type = "";
    for (const h of headers) {
      const i = h.indexOf(":");
      const k = h.slice(0, i).trim().toLowerCase(), v = h.slice(i + 1).trim();
      if (k === "content-disposition") disposition = v;
      else if (k === "content-type") type = v;
    }
    const name = param(disposition, "name");
    if (name === undefined) throw new TypeError("Multipart part without a name");
    const filename = param(disposition, "filename");
    if (filename !== undefined) fd.append(name, new File([content.slice()], filename, { type }));
    else fd.append(name, dec.decode(content));
    pos = next;
  }
  return fd;
}

export function installFormData() {
  defineGlobal("FormData", FormData);
}
