// CompressionStream / DecompressionStream (Compression Streams standard)
// over the host's codecs ("gzip", "deflate", "deflate-raw", "brotli").
import { host } from "./host";
import { defineGlobal, tag } from "./util";

const formats = ["gzip", "deflate", "deflate-raw", "brotli"];

function toBytes(chunk: unknown): Uint8Array {
  if (chunk instanceof ArrayBuffer) return new Uint8Array(chunk);
  if (ArrayBuffer.isView(chunk)) return new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
  throw new TypeError("The chunk is not a BufferSource");
}

function makeTransform(format: unknown, decompress: boolean, name: string) {
  const f = String(format);
  if (!formats.includes(f)) throw new TypeError(`Failed to construct '${name}': Unsupported compression format: '${f}'`);
  const codec = host.compressor(f, decompress);
  const emit = (out: ArrayBuffer, controller: TransformStreamDefaultController<Uint8Array>) => {
    if (out.byteLength > 0) controller.enqueue(new Uint8Array(out));
  };
  return new TransformStream<BufferSource, Uint8Array>({
    transform(chunk, controller) {
      const r = codec.write(toBytes(chunk));
      if (!Array.isArray(r)) return emit(r, controller);
      emit(r[0], controller);
      if (r[1] !== null) controller.error(new TypeError(r[1]));
    },
    flush(controller) {
      emit(codec.finish(), controller);
    },
  });
}

class CompressionStream {
  #transform: TransformStream<BufferSource, Uint8Array>;
  constructor(format: string) {
    this.#transform = makeTransform(format, false, "CompressionStream");
  }
  get readable() { return this.#transform.readable; }
  get writable() { return this.#transform.writable; }
}
tag(CompressionStream, "CompressionStream");

class DecompressionStream {
  #transform: TransformStream<BufferSource, Uint8Array>;
  constructor(format: string) {
    this.#transform = makeTransform(format, true, "DecompressionStream");
  }
  get readable() { return this.#transform.readable; }
  get writable() { return this.#transform.writable; }
}
tag(DecompressionStream, "DecompressionStream");

export function installCompression() {
  defineGlobal("CompressionStream", CompressionStream);
  defineGlobal("DecompressionStream", DecompressionStream);
}
