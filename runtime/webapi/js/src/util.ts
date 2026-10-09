// Defines a WebIDL-style global: writable, configurable, non-enumerable.
export function defineGlobal(name: string, value: unknown) {
  Object.defineProperty(globalThis, name, { value, writable: true, configurable: true, enumerable: false });
}

// Sets Symbol.toStringTag the way WebIDL interfaces do.
export function tag(ctor: Function, name: string) {
  Object.defineProperty(ctor.prototype, Symbol.toStringTag, { value: name, configurable: true });
}

export function toBytes(v: ArrayBuffer | ArrayBufferView | null | undefined): Uint8Array {
  if (v == null) return new Uint8Array(0);
  if (v instanceof Uint8Array) return v;
  if (ArrayBuffer.isView(v)) return new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
  if (v instanceof ArrayBuffer) return new Uint8Array(v);
  throw new TypeError("Expected an ArrayBuffer or ArrayBufferView");
}

export function isBufferSource(v: unknown): v is ArrayBuffer | ArrayBufferView {
  return v instanceof ArrayBuffer || ArrayBuffer.isView(v);
}

export function concatBytes(chunks: Uint8Array[]): Uint8Array {
  let n = 0;
  for (const c of chunks) n += c.byteLength;
  const out = new Uint8Array(n);
  let off = 0;
  for (const c of chunks) { out.set(c, off); off += c.byteLength; }
  return out;
}

export function copyBytes(v: Uint8Array): Uint8Array {
  return new Uint8Array(v);
}
