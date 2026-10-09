// Oganesson's sharp: the commonly used part of sharp's API on Go image code
// (runtime/natives/sharp.go). Methods outside it throw.
const host = () => {
  const h = globalThis[Symbol.for("og.sharp")];
  if (!h) throw new Error("sharp: Oganesson's image service is not installed in this runtime");
  return h;
};

const toBytes = (b) => (globalThis.Buffer ? globalThis.Buffer.from(b) : new Uint8Array(b));

function inputOf(input) {
  if (typeof input === "string") return input;
  if (input instanceof ArrayBuffer) return input;
  if (ArrayBuffer.isView(input)) return input.buffer.slice(input.byteOffset, input.byteOffset + input.byteLength);
  throw new Error("sharp: Oganesson supports Buffer, typed array, ArrayBuffer and file path inputs");
}

const unsupported = (name) =>
  function () {
    throw new Error(`sharp: ${name}() is not supported by Oganesson`);
  };

function colour(c) {
  if (c == null) return [0, 0, 0, 1];
  if (typeof c === "object") return [c.r ?? 0, c.g ?? 0, c.b ?? 0, c.alpha ?? 1];
  const m = /^#?([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})?$/i.exec(String(c));
  if (!m) throw new Error(`sharp: unsupported colour ${c}`);
  return [parseInt(m[1], 16), parseInt(m[2], 16), parseInt(m[3], 16), m[4] ? parseInt(m[4], 16) / 255 : 1];
}

class Sharp {
  constructor(input, options) {
    if (input === undefined) throw new Error("sharp: streaming input is not supported by Oganesson");
    if (options && options.create) throw new Error("sharp: create is not supported by Oganesson");
    this._input = inputOf(input);
    this._ops = {};
  }
  clone() {
    const c = new Sharp(this._input);
    c._ops = JSON.parse(JSON.stringify(this._ops));
    return c;
  }
  rotate(angle, options) {
    if (angle === undefined) this._ops.autoOrient = true;
    else this._ops.rotate = Number(angle);
    if (options && options.background !== undefined) colour(options.background);
    return this;
  }
  autoOrient() {
    this._ops.autoOrient = true;
    return this;
  }
  flip(on = true) {
    this._ops.flip = !!on;
    return this;
  }
  flop(on = true) {
    this._ops.flop = !!on;
    return this;
  }
  extract({ left, top, width, height }) {
    this._ops.extract = { Left: left, Top: top, Width: width, Height: height };
    return this;
  }
  resize(width, height, options) {
    if (width && typeof width === "object") ({ width, height, ...options } = width);
    else if (height && typeof height === "object") (options = height), (height = undefined);
    options = options || {};
    this._ops.resize = {
      width: width ?? 0,
      height: height ?? 0,
      fit: options.fit || "cover",
      position: String(options.position ?? options.gravity ?? "centre"),
      background: colour(options.background),
      withoutEnlargement: !!options.withoutEnlargement,
      withoutReduction: !!options.withoutReduction,
    };
    return this;
  }
  grayscale(on = true) {
    this._ops.grayscale = !!on;
    return this;
  }
  greyscale(on = true) {
    return this.grayscale(on);
  }
  toFormat(format, options) {
    const f = typeof format === "object" ? format.id : String(format);
    this._ops.format = f === "jpg" ? "jpeg" : f;
    if (options && options.quality !== undefined) this._ops.quality = Number(options.quality);
    return this;
  }
  jpeg(options) {
    return this.toFormat("jpeg", options);
  }
  png(options) {
    return this.toFormat("png", options);
  }
  webp(options) {
    return this.toFormat("webp", options);
  }
  gif(options) {
    return this.toFormat("gif", options);
  }
  withMetadata() {
    return this; // metadata is not carried over; accepted for compatibility
  }
  keepMetadata() {
    return this;
  }
  _run() {
    const { data, info } = host().process(this._input, JSON.stringify(this._ops));
    return { data: toBytes(data), info };
  }
  toBuffer(options, callback) {
    if (typeof options === "function") (callback = options), (options = {});
    const withObject = options && options.resolveWithObject;
    const p = new Promise((resolve) => resolve(this._run()));
    if (callback) {
      p.then((r) => callback(null, r.data, r.info), (e) => callback(e));
      return this;
    }
    return p.then((r) => (withObject ? r : r.data));
  }
  toFile(path, callback) {
    const p = new Promise((resolve) => {
      const { data, info } = this._run();
      host().writeFile(String(path), data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength));
      resolve(info);
    });
    if (callback) {
      p.then((info) => callback(null, info), (e) => callback(e));
      return this;
    }
    return p;
  }
  metadata(callback) {
    const p = new Promise((resolve) => resolve(host().metadata(this._input)));
    if (callback) {
      p.then((m) => callback(null, m), (e) => callback(e));
      return this;
    }
    return p;
  }
}

for (const name of ["avif", "heif", "tiff", "raw", "jp2", "jxl", "composite", "blur", "sharpen", "median", "normalise", "normalize", "modulate", "tint", "negate", "threshold", "linear", "recomb", "extend", "trim", "affine", "pipe", "stats"]) {
  Sharp.prototype[name] = unsupported(name);
}

export default function sharp(input, options) {
  return new Sharp(input, options);
}
sharp.cache = () => ({});
sharp.concurrency = () => 1;
sharp.simd = () => false;
sharp.format = { jpeg: { id: "jpeg" }, png: { id: "png" }, webp: { id: "webp" }, gif: { id: "gif" } };
sharp.versions = { sharp: "oganesson" };
