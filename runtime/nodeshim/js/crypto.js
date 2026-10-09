// node:crypto subset over Oganesson's Go crypto primitives.
"use strict";
const { Buffer } = require("buffer");
const g = globalThis[Symbol.for("og.internals")];

const algorithms = { md5: "MD5", sha1: "SHA-1", "sha-1": "SHA-1", sha256: "SHA-256", "sha-256": "SHA-256", sha384: "SHA-384", "sha-384": "SHA-384", sha512: "SHA-512", "sha-512": "SHA-512" };
function alg(name) {
  const a = algorithms[String(name).toLowerCase()];
  if (!a) throw new Error(`Digest method not supported: ${name}`);
  return a;
}
// KeyObject: secret keys only (createSecretKey); asymmetric key material is
// not supported, so createPrivateKey / createPublicKey throw, as Node does
// for input that is not a key (jsonwebtoken relies on that for HMAC secrets).
class KeyObject {
  constructor(type, data) { this.type = type; this._data = data; }
  get symmetricKeySize() { return this.type === "secret" ? this._data.length : undefined; }
  get asymmetricKeyType() { return undefined; }
  export() { return Buffer.from(this._data); }
  equals(other) { return other instanceof KeyObject && other.type === this.type && Buffer.compare(other._data, this._data) === 0; }
}
const bytes = (data, enc) => (data instanceof KeyObject ? Buffer.from(data._data) : typeof data === "string" ? Buffer.from(data, enc || "utf8") : Buffer.from(data.buffer ? new Uint8Array(data.buffer, data.byteOffset, data.byteLength) : data));
const noAsymmetric = (what) => () => { throw new Error(`og: crypto.${what}: asymmetric keys are not supported`); };
const out = (ab, enc) => { const b = Buffer.from(ab); return enc ? b.toString(enc) : b; };

class Hash {
  constructor(name, key) { this._alg = alg(name); this._key = key; this._chunks = []; }
  update(data, enc) { this._chunks.push(bytes(data, enc)); return this; }
  digest(enc) {
    const data = Buffer.concat(this._chunks);
    return out(this._key ? g.hmac(this._alg, this._key, data) : g.digest(this._alg, data), enc);
  }
  copy() { const h = new Hash("sha1"); h._alg = this._alg; h._key = this._key; h._chunks = this._chunks.slice(); return h; }
}

// Cipheriv / Decipheriv for aes-{128,192,256}-{gcm,cbc,ctr} over the Go
// primitives. Input is buffered and processed by final(): the output of
// update() + final() is Node's, but update() itself returns nothing.
const cipherModes = { gcm: true, cbc: true, ctr: true };
function parseCipher(name, key) {
  const m = /^aes-(128|192|256)-(gcm|cbc|ctr)$/.exec(String(name).toLowerCase());
  if (!m || !cipherModes[m[2]]) throw new Error("Unknown cipher");
  if (key.length * 8 !== Number(m[1])) throw new RangeError("Invalid key length");
  return m[2];
}
class Cipher {
  constructor(encrypt, name, key, iv, opts) {
    this._key = bytes(key);
    this._mode = parseCipher(name, this._key);
    this._iv = bytes(iv);
    this._encrypt = encrypt;
    this._tagBits = ((opts && opts.authTagLength) || 16) * 8;
    this._chunks = [];
    this._aad = Buffer.alloc(0);
    this._tag = null;
    this._done = false;
  }
  update(data, inEnc, outEnc) {
    if (this._done) throw new Error("Unsupported state");
    this._chunks.push(bytes(data, inEnc));
    const empty = Buffer.alloc(0);
    return outEnc ? empty.toString(outEnc) : empty;
  }
  final(outEnc) {
    if (this._done) throw new Error("Unsupported state");
    this._done = true;
    const data = Buffer.concat(this._chunks);
    let r;
    if (this._mode === "gcm") {
      if (this._encrypt) {
        r = g.aesGcm(true, this._key, this._iv, data, this._aad, this._tagBits);
        if (r !== null) {
          const all = Buffer.from(r), n = all.length - this._tagBits / 8;
          this._tag = all.subarray(n);
          r = all.subarray(0, n);
        }
      } else {
        if (!this._tag) throw new Error("Unsupported state or unable to authenticate data");
        r = g.aesGcm(false, this._key, this._iv, Buffer.concat([data, this._tag]), this._aad, this._tag.length * 8);
      }
    } else if (this._mode === "cbc") {
      r = g.aesCbc(this._encrypt, this._key, this._iv, data);
    } else {
      r = g.aesCtr(this._key, this._iv, data);
    }
    if (r === null || r === undefined) throw new Error(this._mode === "gcm" ? "Unsupported state or unable to authenticate data" : "bad decrypt");
    return out(r, outEnc);
  }
  setAAD(buf) { this._aad = bytes(buf); return this; }
  getAuthTag() {
    if (!this._encrypt || !this._tag) throw new Error("Invalid state for operation getAuthTag");
    return Buffer.from(this._tag);
  }
  setAuthTag(tag) { this._tag = bytes(tag); return this; }
  setAutoPadding(on) {
    if (on === false) throw new Error("og: setAutoPadding(false) is not supported");
    return this;
  }
}

function randomBytes(n, cb) {
  const b = Buffer.from(g.randomBytes(n));
  if (cb) { queueMicrotask(() => cb(null, b)); return; }
  return b;
}

module.exports = Object.assign({}, require("unenv:node/crypto"), {
  createHash: (name) => new Hash(name),
  createHmac: (name, key) => new Hash(name, bytes(key)),
  getHashes: () => Object.keys(algorithms),
  KeyObject,
  createSecretKey: (key, enc) => new KeyObject("secret", bytes(key, enc)),
  createPrivateKey: noAsymmetric("createPrivateKey"),
  createPublicKey: noAsymmetric("createPublicKey"),
  createCipheriv: (name, key, iv, opts) => new Cipher(true, name, key, iv, opts),
  createDecipheriv: (name, key, iv, opts) => new Cipher(false, name, key, iv, opts),
  getCiphers: () => ["aes-128-cbc", "aes-128-ctr", "aes-128-gcm", "aes-192-cbc", "aes-192-ctr", "aes-192-gcm", "aes-256-cbc", "aes-256-ctr", "aes-256-gcm"],
  hash: (name, data, enc = "hex") => new Hash(name).update(data).digest(enc),
  randomBytes,
  pseudoRandomBytes: randomBytes,
  randomFillSync: (buf) => globalThis.crypto.getRandomValues(buf),
  randomUUID: () => g.randomUUID(),
  randomInt(min, max) {
    if (max === undefined) { max = min; min = 0; }
    const range = max - min;
    const n = new Uint32Array(1);
    globalThis.crypto.getRandomValues(n);
    return min + (n[0] % range);
  },
  getRandomValues: (a) => globalThis.crypto.getRandomValues(a),
  timingSafeEqual(a, b) {
    if (a.byteLength !== b.byteLength) throw new RangeError("Input buffers must have the same byte length");
    const x = bytes(a), y = bytes(b);
    let d = 0;
    for (let i = 0; i < x.length; i++) d |= x[i] ^ y[i];
    return d === 0;
  },
  pbkdf2Sync: (pw, salt, iter, keylen, digest) => Buffer.from(g.pbkdf2(alg(digest), bytes(pw), bytes(salt), iter, keylen * 8)),
  pbkdf2(pw, salt, iter, keylen, digest, cb) {
    let r, e = null;
    try { r = module.exports.pbkdf2Sync(pw, salt, iter, keylen, digest); } catch (err) { e = err; }
    queueMicrotask(() => cb(e, r));
  },
  hkdfSync: (digest, ikm, salt, info, keylen) => g.hkdf(alg(digest), bytes(ikm), bytes(salt), bytes(info), keylen * 8),
  webcrypto: globalThis.crypto,
  subtle: globalThis.crypto && globalThis.crypto.subtle,
  constants: {},
});
