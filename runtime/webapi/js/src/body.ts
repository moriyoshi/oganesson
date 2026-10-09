import { Blob, File, blobBytes } from "./blob";
import { FormData, encodeMultipart, parseMultipart } from "./formdata";
import { URLSearchParams, parseFormUrlencoded } from "./url";
import { utf8Encode } from "./encoding";
import { toBytes, isBufferSource, concatBytes } from "./util";

export interface BodyState {
  stream: ReadableStream<Uint8Array>;
  // Known length and bytes for non-stream sources (used for Content-Length
  // and to let fetch send without re-reading the stream).
  source: Uint8Array | null;
}

export interface Extracted {
  body: BodyState;
  contentType: string | null;
}

export function streamFromBytes(bytes: Uint8Array): ReadableStream<Uint8Array> {
  let done = false;
  return new ReadableStream({
    pull(c) {
      if (!done && bytes.length > 0) c.enqueue(bytes.slice());
      done = true;
      c.close();
    },
  });
}

// extractBody implements the Fetch "extract a body" algorithm.
export function extractBody(init: unknown): Extracted {
  let source: Uint8Array | null = null;
  let contentType: string | null = null;
  if (init instanceof ReadableStream) {
    if ((init as any).locked) throw new TypeError("Body stream is locked");
    return { body: { stream: init as ReadableStream<Uint8Array>, source: null }, contentType: null };
  }
  if (init instanceof Blob) {
    source = blobBytes(init);
    if (init.type) contentType = init.type;
  } else if (isBufferSource(init)) {
    source = new Uint8Array(toBytes(init));
  } else if (init instanceof FormData) {
    const m = encodeMultipart(init);
    source = m.body;
    contentType = m.contentType;
  } else if (init instanceof URLSearchParams) {
    source = utf8Encode(init.toString());
    contentType = "application/x-www-form-urlencoded;charset=UTF-8";
  } else if (init !== null && typeof init === "object" && typeof (init as any)[Symbol.asyncIterator] === "function") {
    // An async iterable (a Node stream): undici, Node's fetch, takes it as
    // a body (with duplex: "half"), and Node servers' adapters pass the
    // IncomingMessage this way (@astrojs/node).
    const it = (init as any)[Symbol.asyncIterator]();
    const stream = new ReadableStream<Uint8Array>({
      async pull(controller) {
        const { done, value } = await it.next();
        if (done) return controller.close();
        controller.enqueue(typeof value === "string" ? utf8Encode(value) : new Uint8Array(toBytes(value)));
      },
      async cancel(reason) {
        await it.return?.(reason);
      },
    });
    return { body: { stream, source: null }, contentType: null };
  } else {
    source = utf8Encode(String(init));
    contentType = "text/plain;charset=UTF-8";
  }
  return { body: { stream: streamFromBytes(source), source }, contentType };
}

const kState = Symbol("bodyState");

export interface BodyHost {
  [kState]: { body: BodyState | null; headers: Headers };
}

export function bodyState(o: any): { body: BodyState | null; headers: Headers } { return o[kState]; }
export function initBody(o: any, body: BodyState | null, headers: Headers) {
  Object.defineProperty(o, kState, { value: { body, headers }, writable: true });
}

async function readAll(stream: ReadableStream<Uint8Array>): Promise<Uint8Array> {
  const reader = stream.getReader();
  const chunks: Uint8Array[] = [];
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    if (!(value instanceof Uint8Array)) throw new TypeError("Body stream chunks must be Uint8Array");
    chunks.push(value);
  }
  return concatBytes(chunks);
}

// The streams ponyfill records the spec's [[disturbed]] slot as _disturbed.
export function isDisturbed(stream: ReadableStream) {
  return (stream as any)._disturbed === true;
}

function checkUnused(b: BodyState) {
  if (b.stream.locked || isDisturbed(b.stream)) throw new TypeError("Body has already been consumed");
}

// consume implements "fully read body" with the bodyUsed checks.
export async function consume(o: any): Promise<Uint8Array> {
  const st = bodyState(o);
  if (!st.body) return new Uint8Array(0);
  checkUnused(st.body);
  return readAll(st.body.stream);
}

function mimeEssence(ct: string | null) {
  return (ct ?? "").split(";")[0].trim().toLowerCase();
}

// Methods shared by Request and Response.
export const BodyMixin = {
  get body(this: any): ReadableStream<Uint8Array> | null {
    const b = bodyState(this).body;
    if (!b) return null;
    return b.stream;
  },
  get bodyUsed(this: any): boolean {
    const b = bodyState(this).body;
    return !!b && isDisturbed(b.stream);
  },
  async arrayBuffer(this: any) { const b = await consume(this); return b.buffer.byteLength === b.byteLength ? b.buffer : b.slice().buffer; },
  async bytes(this: any) { return consume(this); },
  async blob(this: any) {
    const b = await consume(this);
    return new Blob([b], { type: bodyState(this).headers.get("content-type") ?? "" });
  },
  async text(this: any) { return new TextDecoder().decode(await consume(this)); },
  async json(this: any) { return JSON.parse(new TextDecoder().decode(await consume(this))); },
  async formData(this: any) {
    const ct = bodyState(this).headers.get("content-type");
    const essence = mimeEssence(ct);
    const bytes = await consume(this);
    if (essence === "multipart/form-data") return parseMultipart(bytes, ct!);
    if (essence === "application/x-www-form-urlencoded") {
      const fd = new FormData();
      for (const [k, v] of parseFormUrlencoded(new TextDecoder().decode(bytes))) fd.append(k, v);
      return fd;
    }
    throw new TypeError(`Could not parse content as FormData (content-type: ${ct})`);
  },
};

export function applyBodyMixin(ctor: Function) {
  for (const k of Object.getOwnPropertyNames(BodyMixin)) {
    Object.defineProperty(ctor.prototype, k, Object.getOwnPropertyDescriptor(BodyMixin, k)!);
  }
}

// cloneBody tees the stream: the original keeps one branch.
export function cloneBody(o: any): BodyState | null {
  const st = bodyState(o);
  if (!st.body) return null;
  checkUnused(st.body);
  const [a, b] = st.body.stream.tee();
  st.body = { stream: a, source: st.body.source };
  return { stream: b, source: st.body.source };
}

export { File };
