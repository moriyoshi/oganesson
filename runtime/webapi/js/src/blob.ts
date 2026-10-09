import { defineGlobal, tag, toBytes, isBufferSource, concatBytes } from "./util";
import { utf8Encode } from "./encoding";

const kBytes = Symbol("bytes");

// WebIDL [Clamp] long long: round to nearest, ties to even.
function clampLongLong(v: unknown): number {
  const x = Number(v);
  if (Number.isNaN(x)) return 0;
  if (!Number.isFinite(x)) return x > 0 ? Number.MAX_SAFE_INTEGER : -Number.MAX_SAFE_INTEGER;
  const f = Math.floor(x), diff = x - f;
  if (diff < 0.5) return f;
  if (diff > 0.5) return f + 1;
  return f % 2 === 0 ? f : f + 1;
}

export class Blob {
  [kBytes]: Uint8Array;
  #type: string;
  constructor(parts: Iterable<unknown> = [], options: { type?: string; endings?: "transparent" | "native" } = {}) {
    if (parts === null || typeof parts !== "object" || typeof (parts as any)[Symbol.iterator] !== "function") {
      throw new TypeError("Blob: parts must be a sequence");
    }
    const chunks: Uint8Array[] = [];
    for (const p of parts) {
      if (p instanceof Blob) chunks.push(p[kBytes]);
      else if (isBufferSource(p)) chunks.push(new Uint8Array(toBytes(p)));
      else {
        let s = String(p);
        if (options.endings === "native") s = s.replace(/\r\n?/g, "\n");
        chunks.push(utf8Encode(s));
      }
    }
    this[kBytes] = concatBytes(chunks);
    const t = options.type === undefined ? "" : String(options.type);
    this.#type = /^[\x20-\x7e]*$/.test(t) ? t.toLowerCase() : "";
  }
  get size() { return this[kBytes].byteLength; }
  get type() { return this.#type; }
  slice(start?: number, end?: number, contentType?: string) {
    const n = this.size;
    const rel = (v: number | undefined, d: number) => {
      if (v === undefined) return d;
      const x = clampLongLong(v);
      return x < 0 ? Math.max(n + x, 0) : Math.min(x, n);
    };
    const from = rel(start, 0), to = rel(end, n);
    return new Blob([this[kBytes].subarray(from, Math.max(from, to))], { type: contentType });
  }
  async arrayBuffer() { return this[kBytes].slice().buffer; }
  async bytes() { return this[kBytes].slice(); }
  async text() { return new TextDecoder().decode(this[kBytes]); }
  stream(): ReadableStream<Uint8Array> {
    const bytes = this[kBytes];
    let off = 0;
    return new ReadableStream({
      pull(controller) {
        if (off >= bytes.length) { controller.close(); return; }
        const end = Math.min(off + 65536, bytes.length);
        controller.enqueue(bytes.slice(off, end));
        off = end;
      },
    });
  }
}
tag(Blob, "Blob");

export class File extends Blob {
  #name: string;
  #lastModified: number;
  constructor(parts: Iterable<unknown>, name: string, options: { type?: string; endings?: "transparent" | "native"; lastModified?: number } = {}) {
    if (arguments.length < 2) throw new TypeError("File constructor requires 2 arguments");
    super(parts, options);
    this.#name = String(name);
    this.#lastModified = options.lastModified === undefined ? Date.now() : Number(options.lastModified);
  }
  get name() { return this.#name; }
  get lastModified() { return this.#lastModified; }
  get webkitRelativePath() { return ""; }
}
tag(File, "File");

export function blobBytes(b: Blob): Uint8Array { return b[kBytes]; }

export function installBlob() {
  defineGlobal("Blob", Blob);
  defineGlobal("File", File);
}
