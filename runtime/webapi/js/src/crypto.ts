import { host } from "./host";
import { DOMException } from "./events";
import { defineGlobal, tag, toBytes, isBufferSource } from "./util";

const kCreate = Symbol("create");
const keyData = new WeakMap<CryptoKey, Uint8Array>();

type Usage = "encrypt" | "decrypt" | "sign" | "verify" | "deriveKey" | "deriveBits" | "wrapKey" | "unwrapKey";

export class CryptoKey {
  #type: string; #extractable: boolean; #algorithm: any; #usages: Usage[];
  constructor(key: symbol, type: string, extractable: boolean, algorithm: any, usages: Usage[]) {
    if (key !== kCreate) throw new TypeError("Illegal constructor");
    this.#type = type; this.#extractable = extractable; this.#algorithm = algorithm; this.#usages = usages;
  }
  get type() { return this.#type; }
  get extractable() { return this.#extractable; }
  get algorithm() { return this.#algorithm; }
  get usages() { return this.#usages; }
}
tag(CryptoKey, "CryptoKey");

const hashes = ["SHA-1", "SHA-256", "SHA-384", "SHA-512"];
const hashBlockBits: Record<string, number> = { "SHA-1": 512, "SHA-256": 512, "SHA-384": 1024, "SHA-512": 1024 };

function normName(alg: any): string {
  const name = typeof alg === "string" ? alg : alg?.name;
  if (typeof name !== "string") throw new TypeError("Algorithm: name is required");
  const known = ["AES-GCM", "AES-CBC", "AES-CTR", "AES-KW", "HMAC", "PBKDF2", "HKDF", ...hashes,
    "ECDSA", "ECDH", "Ed25519", "X25519", "RSA-OAEP", "RSA-PSS", "RSASSA-PKCS1-v1_5"];
  return known.find((k) => k.toUpperCase() === name.toUpperCase()) ?? name;
}

function hashName(h: any): string {
  const n = normName(h);
  if (!hashes.includes(n)) throw new DOMException(`Unrecognized hash: ${n}`, "NotSupportedError");
  return n;
}

function bytesOf(v: unknown): Uint8Array {
  if (!isBufferSource(v)) throw new TypeError("Expected a BufferSource");
  return new Uint8Array(toBytes(v as any));
}

function op<T>(fn: () => T): Promise<T> {
  return new Promise((resolve, reject) => {
    try { resolve(fn()); } catch (e) { reject(e); }
  });
}

function fail(): never { throw new DOMException("The operation failed for an operation-specific reason", "OperationError"); }
function notSupported(what: string): never { throw new DOMException(`${what} is not supported`, "NotSupportedError"); }

function checkUsage(key: CryptoKey, usage: Usage, alg: string) {
  if (!(key instanceof CryptoKey)) throw new TypeError("Expected a CryptoKey");
  if (normName(key.algorithm) !== alg) throw new DOMException("Key algorithm mismatch", "InvalidAccessError");
  if (!key.usages.includes(usage)) throw new DOMException(`Key does not support the '${usage}' operation`, "InvalidAccessError");
}

function makeKey(alg: string, params: any, raw: Uint8Array, extractable: boolean, usages: Usage[]): CryptoKey {
  let algorithm: any;
  if (alg === "HMAC") {
    algorithm = { name: "HMAC", hash: { name: hashName(params.hash) }, length: params.length ?? raw.length * 8 };
  } else if (alg.startsWith("AES-")) {
    if (![16, 24, 32].includes(raw.length)) throw new DOMException("Invalid AES key length", "DataError");
    algorithm = { name: alg, length: raw.length * 8 };
  } else if (alg === "PBKDF2" || alg === "HKDF") {
    if (extractable) throw new SyntaxError(`${alg} keys must not be extractable`);
    algorithm = { name: alg };
  } else {
    notSupported(`Algorithm ${alg}`);
  }
  const key = new CryptoKey(kCreate, "secret", extractable, algorithm, [...usages]);
  keyData.set(key, raw);
  return key;
}

const b64url = {
  encode(b: Uint8Array) { let s = ""; for (const x of b) s += String.fromCharCode(x); return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""); },
  decode(s: string) { const bin = atob(s.replace(/-/g, "+").replace(/_/g, "/")); const out = new Uint8Array(bin.length); for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i); return out; },
};

function buf(v: ArrayBuffer | null): ArrayBuffer { return v ?? fail(); }
function asBytes(v: ArrayBuffer | null): Uint8Array { return new Uint8Array(buf(v)); }

// makeAsymKey holds an asymmetric key's bytes (Ed25519: the 32-byte seed for
// a private key, the public key for a public one; ECDSA: the pkcs8/spki DER).
function makeAsymKey(algorithm: any, type: "public" | "private", raw: Uint8Array, extractable: boolean, usages: Usage[]): CryptoKey {
  const key = new CryptoKey(kCreate, type, extractable, algorithm, [...usages]);
  keyData.set(key, raw);
  return key;
}

// ed25519Usages validates and returns the usages allowed for a key type: a
// private key signs, a public key verifies.
function ed25519Usages(type: "public" | "private", usages: Usage[]): Usage[] {
  const allowed: Usage = type === "private" ? "sign" : "verify";
  for (const u of usages) {
    if (u !== allowed) throw new SyntaxError(`Unsupported key usage '${u}' for an Ed25519 ${type} key`);
  }
  return usages;
}

function ecCurve(name: any): string {
  if (!["P-256", "P-384", "P-521"].includes(name)) throw new DOMException(`Unsupported named curve: ${name}`, "NotSupportedError");
  return name;
}

// rsaUsages returns [privateUsages, publicUsages] for an RSA algorithm.
function rsaUsages(alg: string): [Usage[], Usage[]] {
  if (alg === "RSA-OAEP") return [["decrypt", "unwrapKey"], ["encrypt", "wrapKey"]];
  return [["sign"], ["verify"]];
}

function isRSA(alg: string): boolean {
  return alg === "RSASSA-PKCS1-v1_5" || alg === "RSA-PSS" || alg === "RSA-OAEP";
}

function isEC(alg: string): boolean {
  return alg === "ECDSA" || alg === "ECDH";
}

// ecUsages returns [privateUsages, publicUsages] for an EC algorithm. An ECDH
// public key holds no usages; only the private key derives.
function ecUsages(alg: string): [Usage[], Usage[]] {
  if (alg === "ECDH") return [["deriveKey", "deriveBits"], []];
  return [["sign"], ["verify"]];
}

// derivePublic returns the stored bytes of the public key in
// algorithm.public (ECDH: spki DER; X25519: the raw public key).
function derivePublic(algorithm: any, name: string): Uint8Array {
  const pub = algorithm.public;
  if (!(pub instanceof CryptoKey) || normName(pub.algorithm) !== name || pub.type !== "public") {
    throw new DOMException(`${name} requires a public key in algorithm.public`, "InvalidAccessError");
  }
  return keyData.get(pub)!;
}

// ecPoint is the uncompressed EC point (0x04 || X || Y) from the jwk x and y.
function ecPoint(x: Uint8Array, y: Uint8Array): Uint8Array {
  const p = new Uint8Array(1 + x.length + y.length);
  p[0] = 0x04;
  p.set(x, 1);
  p.set(y, 1 + x.length);
  return p;
}

class SubtleCrypto {
  constructor(key?: symbol) { if (key !== kCreate) throw new TypeError("Illegal constructor"); }

  digest(algorithm: any, data: BufferSource) {
    return op(() => buf(host.digest(hashName(algorithm), bytesOf(data))));
  }

  generateKey(algorithm: any, extractable: boolean, usages: Usage[]) {
    return op(() => {
      const alg = normName(algorithm);
      if (alg === "HMAC") {
        const h = hashName(algorithm.hash);
        const bits = algorithm.length ?? hashBlockBits[h];
        return makeKey(alg, algorithm, new Uint8Array(host.randomBytes(Math.ceil(bits / 8))), extractable, usages);
      }
      if (alg.startsWith("AES-")) {
        if (![128, 192, 256].includes(algorithm.length)) throw new DOMException("Invalid AES key length", "OperationError");
        return makeKey(alg, algorithm, new Uint8Array(host.randomBytes(algorithm.length / 8)), extractable, usages);
      }
      if (alg === "Ed25519") {
        const seed = asBytes(host.ed25519Generate());
        const pub = asBytes(host.ed25519Public(seed));
        return {
          privateKey: makeAsymKey({ name: alg }, "private", seed, extractable, ed25519Usages("private", usages.filter((u) => u === "sign"))),
          publicKey: makeAsymKey({ name: alg }, "public", pub, true, ed25519Usages("public", usages.filter((u) => u === "verify"))),
        };
      }
      if (isEC(alg)) {
        const curve = ecCurve(algorithm.namedCurve);
        const pkcs8 = asBytes(host.ecdsaGenerate(curve));
        const spki = asBytes(host.ecdsaToSpki(pkcs8));
        const a = { name: alg, namedCurve: curve };
        const [privU, pubU] = ecUsages(alg);
        return {
          privateKey: makeAsymKey(a, "private", pkcs8, extractable, usages.filter((u) => privU.includes(u))),
          publicKey: makeAsymKey(a, "public", spki, true, usages.filter((u) => pubU.includes(u))),
        };
      }
      if (alg === "X25519") {
        const priv = asBytes(host.x25519Generate());
        const pub = asBytes(host.x25519Public(priv));
        return {
          privateKey: makeAsymKey({ name: alg }, "private", priv, extractable, usages.filter((u) => u === "deriveKey" || u === "deriveBits")),
          publicKey: makeAsymKey({ name: alg }, "public", pub, true, []),
        };
      }
      if (isRSA(alg)) {
        const bits = algorithm.modulusLength;
        const pkcs8 = asBytes(host.rsaGenerate(bits));
        const spki = asBytes(host.rsaToSpki(pkcs8));
        const a = { name: alg, modulusLength: bits, publicExponent: new Uint8Array(algorithm.publicExponent ?? [1, 0, 1]), hash: { name: hashName(algorithm.hash) } };
        const [privU, pubU] = rsaUsages(alg);
        return {
          privateKey: makeAsymKey(a, "private", pkcs8, extractable, usages.filter((u) => privU.includes(u))),
          publicKey: makeAsymKey(a, "public", spki, true, usages.filter((u) => pubU.includes(u))),
        };
      }
      notSupported(`generateKey for ${alg}`);
    });
  }

  importKey(format: string, keyData_: any, algorithm: any, extractable: boolean, usages: Usage[]) {
    return op(() => {
      const alg = normName(algorithm);
      if (alg === "Ed25519") return this.#importEd25519(format, keyData_, extractable, usages);
      if (alg === "X25519") return this.#importX25519(format, keyData_, extractable, usages);
      if (isEC(alg)) return this.#importEc(format, keyData_, algorithm, extractable, usages);
      if (isRSA(alg)) return this.#importRsa(format, keyData_, algorithm, extractable, usages);
      let raw: Uint8Array;
      if (format === "raw") raw = bytesOf(keyData_);
      else if (format === "jwk") {
        if (keyData_?.kty !== "oct" || typeof keyData_.k !== "string") notSupported("Non-oct JWK import");
        raw = b64url.decode(keyData_.k);
      } else notSupported(`Key format ${format}`);
      return makeKey(alg, algorithm, raw, extractable, usages);
    });
  }

  #importEd25519(format: string, data: any, extractable: boolean, usages: Usage[]): CryptoKey {
    let type: "public" | "private";
    let raw: Uint8Array;
    if (format === "raw") { type = "public"; raw = bytesOf(data); }
    else if (format === "spki") { type = "public"; raw = asBytes(host.ed25519PubFromSpki(bytesOf(data))); }
    else if (format === "pkcs8") { type = "private"; raw = asBytes(host.ed25519SeedFromPkcs8(bytesOf(data))); }
    else if (format === "jwk") {
      if (data?.kty !== "OKP" || data.crv !== "Ed25519" || typeof data.x !== "string") notSupported("invalid Ed25519 JWK");
      if (typeof data.d === "string") { type = "private"; raw = b64url.decode(data.d); }
      else { type = "public"; raw = b64url.decode(data.x); }
    } else notSupported(`Key format ${format}`);
    if (raw.length !== 32) throw new DOMException("Invalid Ed25519 key", "DataError");
    return makeAsymKey({ name: "Ed25519" }, type, raw, extractable, ed25519Usages(type, usages));
  }

  #importEc(format: string, data: any, algorithm: any, extractable: boolean, usages: Usage[]): CryptoKey {
    const name = normName(algorithm);
    let type: "public" | "private";
    let der: Uint8Array;
    let curve: string;
    if (format === "pkcs8") {
      type = "private"; der = bytesOf(data);
      curve = host.ecdsaCurve(der, true) || notSupported("unsupported EC private key");
    } else if (format === "spki") {
      type = "public"; der = bytesOf(data);
      curve = host.ecdsaCurve(der, false) || notSupported("unsupported EC public key");
    } else if (format === "raw") {
      type = "public"; curve = ecCurve(algorithm.namedCurve);
      der = asBytes(host.ecdsaSpkiFromPoint(curve, bytesOf(data)));
    } else if (format === "jwk") {
      if (data?.kty !== "EC" || typeof data.x !== "string" || typeof data.y !== "string") notSupported("invalid EC JWK");
      curve = ecCurve(data.crv);
      if (typeof data.d === "string") { type = "private"; der = asBytes(host.ecdsaPkcs8FromD(curve, b64url.decode(data.d))); }
      else { type = "public"; der = asBytes(host.ecdsaSpkiFromPoint(curve, ecPoint(b64url.decode(data.x), b64url.decode(data.y)))); }
    } else notSupported(`Key format ${format}`);
    const [privU, pubU] = ecUsages(name);
    const allowed = type === "private" ? privU : pubU;
    for (const u of usages) if (!allowed.includes(u)) throw new SyntaxError(`Unsupported key usage '${u}' for a ${name} ${type} key`);
    return makeAsymKey({ name, namedCurve: curve }, type, der, extractable, usages);
  }

  #exportEc(format: string, key: CryptoKey, der: Uint8Array): ArrayBuffer | any {
    const spki = () => key.type === "public" ? der : asBytes(host.ecdsaToSpki(der));
    if (format === "pkcs8") {
      if (key.type !== "private") throw new DOMException("Only a private EC key exports as pkcs8", "InvalidAccessError");
      return der.slice().buffer;
    }
    if (format === "spki") {
      if (key.type !== "public") throw new DOMException("Only a public EC key exports as spki", "InvalidAccessError");
      return der.slice().buffer;
    }
    if (format === "raw") {
      if (key.type !== "public") throw new DOMException("Only a public EC key exports as raw", "InvalidAccessError");
      return buf(host.ecdsaPointFromSpki(der));
    }
    if (format === "jwk") {
      const point = asBytes(host.ecdsaPointFromSpki(spki()));
      const n = (point.length - 1) / 2;
      const jwk: any = { kty: "EC", crv: key.algorithm.namedCurve, x: b64url.encode(point.subarray(1, 1 + n)), y: b64url.encode(point.subarray(1 + n)), ext: true, key_ops: key.usages };
      if (key.type === "private") jwk.d = b64url.encode(asBytes(host.ecdsaDFromPkcs8(der)));
      return jwk;
    }
    notSupported(`Key format ${format}`);
  }

  #importRsa(format: string, data: any, algorithm: any, extractable: boolean, usages: Usage[]): CryptoKey {
    const name = normName(algorithm);
    const hash = hashName(algorithm.hash);
    let type: "public" | "private";
    let der: Uint8Array;
    if (format === "spki") { type = "public"; der = bytesOf(data); }
    else if (format === "pkcs8") { type = "private"; der = bytesOf(data); }
    else if (format === "jwk") {
      if (data?.kty !== "RSA" || typeof data.n !== "string" || typeof data.e !== "string") notSupported("invalid RSA JWK");
      type = typeof data.d === "string" ? "private" : "public";
      der = asBytes(host.rsaFromJwk(data, type === "private"));
    } else notSupported(`Key format ${format}`);
    // The modulus length and public exponent come off the parsed key (this
    // also validates an spki/pkcs8 import is really RSA).
    const info = host.rsaJwk(der, type === "private");
    if (!info) throw new DOMException("Invalid RSA key", "DataError");
    const [privU, pubU] = rsaUsages(name);
    const allowed = type === "private" ? privU : pubU;
    for (const u of usages) if (!allowed.includes(u)) throw new SyntaxError(`Unsupported key usage '${u}' for an RSA ${type} key`);
    const a = { name, modulusLength: b64url.decode(info.n).length * 8, publicExponent: b64url.decode(info.e), hash: { name: hash } };
    return makeAsymKey(a, type, der, extractable, usages);
  }

  #exportRsa(format: string, key: CryptoKey, der: Uint8Array): ArrayBuffer | any {
    if (format === "pkcs8") {
      if (key.type !== "private") throw new DOMException("Only a private RSA key exports as pkcs8", "InvalidAccessError");
      return der.slice().buffer;
    }
    if (format === "spki") {
      if (key.type !== "public") throw new DOMException("Only a public RSA key exports as spki", "InvalidAccessError");
      return der.slice().buffer;
    }
    if (format === "jwk") {
      const f = host.rsaJwk(der, key.type === "private");
      if (!f) fail();
      return { kty: "RSA", ...f, ext: true, key_ops: key.usages };
    }
    notSupported(`Key format ${format}`);
  }

  #exportEd25519(format: string, key: CryptoKey, raw: Uint8Array): ArrayBuffer | any {
    if (format === "jwk") {
      const pub = key.type === "private" ? asBytes(host.ed25519Public(raw)) : raw;
      const jwk: any = { kty: "OKP", crv: "Ed25519", x: b64url.encode(pub), ext: true, key_ops: key.usages };
      if (key.type === "private") jwk.d = b64url.encode(raw);
      return jwk;
    }
    if (format === "raw") {
      if (key.type !== "public") throw new DOMException("Only a public Ed25519 key exports as raw", "InvalidAccessError");
      return raw.slice().buffer;
    }
    if (format === "spki") {
      if (key.type !== "public") throw new DOMException("Only a public Ed25519 key exports as spki", "InvalidAccessError");
      return buf(host.ed25519Spki(raw));
    }
    if (format === "pkcs8") {
      if (key.type !== "private") throw new DOMException("Only a private Ed25519 key exports as pkcs8", "InvalidAccessError");
      return buf(host.ed25519Pkcs8(raw));
    }
    notSupported(`Key format ${format}`);
  }

  #importX25519(format: string, data: any, extractable: boolean, usages: Usage[]): CryptoKey {
    let type: "public" | "private";
    let raw: Uint8Array;
    if (format === "raw") { type = "public"; raw = bytesOf(data); }
    else if (format === "spki") { type = "public"; raw = asBytes(host.x25519PubFromSpki(bytesOf(data))); }
    else if (format === "pkcs8") { type = "private"; raw = asBytes(host.x25519PrivFromPkcs8(bytesOf(data))); }
    else if (format === "jwk") {
      if (data?.kty !== "OKP" || data.crv !== "X25519" || typeof data.x !== "string") notSupported("invalid X25519 JWK");
      if (typeof data.d === "string") { type = "private"; raw = b64url.decode(data.d); }
      else { type = "public"; raw = b64url.decode(data.x); }
    } else notSupported(`Key format ${format}`);
    if (raw.length !== 32) throw new DOMException("Invalid X25519 key", "DataError");
    // A private X25519 key derives; a public one holds no usages.
    const allowed: Usage[] = type === "private" ? ["deriveKey", "deriveBits"] : [];
    for (const u of usages) if (!allowed.includes(u)) throw new SyntaxError(`Unsupported key usage '${u}' for an X25519 ${type} key`);
    return makeAsymKey({ name: "X25519" }, type, raw, extractable, usages);
  }

  #exportX25519(format: string, key: CryptoKey, raw: Uint8Array): ArrayBuffer | any {
    if (format === "jwk") {
      const pub = key.type === "private" ? asBytes(host.x25519Public(raw)) : raw;
      const jwk: any = { kty: "OKP", crv: "X25519", x: b64url.encode(pub), ext: true, key_ops: key.usages };
      if (key.type === "private") jwk.d = b64url.encode(raw);
      return jwk;
    }
    if (format === "raw") {
      if (key.type !== "public") throw new DOMException("Only a public X25519 key exports as raw", "InvalidAccessError");
      return raw.slice().buffer;
    }
    if (format === "spki") {
      if (key.type !== "public") throw new DOMException("Only a public X25519 key exports as spki", "InvalidAccessError");
      return buf(host.x25519Spki(raw));
    }
    if (format === "pkcs8") {
      if (key.type !== "private") throw new DOMException("Only a private X25519 key exports as pkcs8", "InvalidAccessError");
      return buf(host.x25519Pkcs8(raw));
    }
    notSupported(`Key format ${format}`);
  }

  exportKey(format: string, key: CryptoKey) {
    return op(() => {
      if (!(key instanceof CryptoKey)) throw new TypeError("Expected a CryptoKey");
      if (!key.extractable) throw new DOMException("Key is not extractable", "InvalidAccessError");
      const raw = keyData.get(key)!;
      if (normName(key.algorithm) === "Ed25519") return this.#exportEd25519(format, key, raw);
      if (normName(key.algorithm) === "X25519") return this.#exportX25519(format, key, raw);
      if (isEC(normName(key.algorithm))) return this.#exportEc(format, key, raw);
      if (isRSA(normName(key.algorithm))) return this.#exportRsa(format, key, raw);
      if (format === "raw") return raw.slice().buffer;
      if (format === "jwk") {
        const a = key.algorithm;
        const jwk: any = { kty: "oct", k: b64url.encode(raw), ext: true, key_ops: key.usages };
        if (a.name === "HMAC") jwk.alg = "HS" + a.hash.name.slice(4);
        else if (a.name.startsWith("AES-")) jwk.alg = `A${a.length}${a.name.slice(4)}`;
        return jwk;
      }
      notSupported(`Key format ${format}`);
    });
  }

  sign(algorithm: any, key: CryptoKey, data: BufferSource) {
    return op(() => {
      const alg = normName(algorithm);
      if (alg === "Ed25519") {
        checkUsage(key, "sign", alg);
        if (key.type !== "private") throw new DOMException("Signing needs a private key", "InvalidAccessError");
        return buf(host.ed25519Sign(keyData.get(key)!, bytesOf(data)));
      }
      if (alg === "ECDSA") {
        checkUsage(key, "sign", alg);
        if (key.type !== "private") throw new DOMException("Signing needs a private key", "InvalidAccessError");
        return buf(host.ecdsaSign(keyData.get(key)!, hashName(algorithm.hash), bytesOf(data)));
      }
      if (alg === "RSASSA-PKCS1-v1_5" || alg === "RSA-PSS") {
        checkUsage(key, "sign", alg);
        if (key.type !== "private") throw new DOMException("Signing needs a private key", "InvalidAccessError");
        const pss = alg === "RSA-PSS";
        return buf(host.rsaSign(keyData.get(key)!, (key.algorithm as any).hash.name, pss, pss ? (algorithm.saltLength ?? 0) : 0, bytesOf(data)));
      }
      if (alg !== "HMAC") notSupported(`sign with ${alg}`);
      checkUsage(key, "sign", alg);
      return buf(host.hmac(key.algorithm.hash.name, keyData.get(key)!, bytesOf(data)));
    });
  }

  verify(algorithm: any, key: CryptoKey, signature: BufferSource, data: BufferSource) {
    return op(() => {
      const alg = normName(algorithm);
      if (alg === "Ed25519") {
        checkUsage(key, "verify", alg);
        if (key.type !== "public") throw new DOMException("Verifying needs a public key", "InvalidAccessError");
        return host.ed25519Verify(keyData.get(key)!, bytesOf(signature), bytesOf(data));
      }
      if (alg === "ECDSA") {
        checkUsage(key, "verify", alg);
        if (key.type !== "public") throw new DOMException("Verifying needs a public key", "InvalidAccessError");
        return host.ecdsaVerify(keyData.get(key)!, hashName(algorithm.hash), bytesOf(signature), bytesOf(data));
      }
      if (alg === "RSASSA-PKCS1-v1_5" || alg === "RSA-PSS") {
        checkUsage(key, "verify", alg);
        if (key.type !== "public") throw new DOMException("Verifying needs a public key", "InvalidAccessError");
        const pss = alg === "RSA-PSS";
        return host.rsaVerify(keyData.get(key)!, (key.algorithm as any).hash.name, pss, pss ? (algorithm.saltLength ?? 0) : 0, bytesOf(signature), bytesOf(data));
      }
      if (alg !== "HMAC") notSupported(`verify with ${alg}`);
      checkUsage(key, "verify", alg);
      const mac = new Uint8Array(buf(host.hmac(key.algorithm.hash.name, keyData.get(key)!, bytesOf(data))));
      const sig = bytesOf(signature);
      if (mac.length !== sig.length) return false;
      let diff = 0;
      for (let i = 0; i < mac.length; i++) diff |= mac[i] ^ sig[i];
      return diff === 0;
    });
  }

  encrypt(algorithm: any, key: CryptoKey, data: BufferSource) { return this.#encDec(true, algorithm, key, data); }
  decrypt(algorithm: any, key: CryptoKey, data: BufferSource) { return this.#encDec(false, algorithm, key, data); }

  #encDec(encrypt: boolean, algorithm: any, key: CryptoKey, data: BufferSource) {
    if (normName(algorithm) === "RSA-OAEP") {
      return op(() => {
        checkUsage(key, encrypt ? "encrypt" : "decrypt", "RSA-OAEP");
        if (encrypt && key.type !== "public") throw new DOMException("Encrypting needs a public key", "InvalidAccessError");
        if (!encrypt && key.type !== "private") throw new DOMException("Decrypting needs a private key", "InvalidAccessError");
        const label = algorithm.label === undefined ? new Uint8Array(0) : bytesOf(algorithm.label);
        const h = (key.algorithm as any).hash.name;
        const der = keyData.get(key)!;
        return buf(encrypt ? host.rsaOaepEncrypt(der, h, label, bytesOf(data)) : host.rsaOaepDecrypt(der, h, label, bytesOf(data)));
      });
    }
    return this.#aes(encrypt, algorithm, key, data);
  }

  #aes(encrypt: boolean, algorithm: any, key: CryptoKey, data: BufferSource) {
    return op(() => {
      const alg = normName(algorithm);
      checkUsage(key, encrypt ? "encrypt" : "decrypt", alg);
      const k = keyData.get(key)!;
      switch (alg) {
        case "AES-GCM": {
          const tag = algorithm.tagLength ?? 128;
          if (![32, 64, 96, 104, 112, 120, 128].includes(tag)) throw new DOMException("Invalid tagLength", "OperationError");
          const aad = algorithm.additionalData === undefined ? null : bytesOf(algorithm.additionalData);
          return buf(host.aesGcm(encrypt, k, bytesOf(algorithm.iv), bytesOf(data), aad, tag));
        }
        case "AES-CBC":
          return buf(host.aesCbc(encrypt, k, bytesOf(algorithm.iv), bytesOf(data)));
        case "AES-CTR":
          if (algorithm.length !== 128) notSupported("AES-CTR counter length other than 128");
          return buf(host.aesCtr(k, bytesOf(algorithm.counter), bytesOf(data)));
      }
      notSupported(`${encrypt ? "encrypt" : "decrypt"} with ${alg}`);
    });
  }

  deriveBits(algorithm: any, baseKey: CryptoKey, length: number) {
    return op(() => {
      const alg = normName(algorithm);
      checkUsage(baseKey, "deriveBits", alg);
      const k = keyData.get(baseKey)!;
      if (alg === "ECDH") {
        if (length != null && length % 8 !== 0) throw new DOMException("Invalid length", "OperationError");
        return buf(host.ecdhBits(k, derivePublic(algorithm, "ECDH"), length == null ? -1 : length));
      }
      if (alg === "X25519") {
        if (length != null && length % 8 !== 0) throw new DOMException("Invalid length", "OperationError");
        return buf(host.x25519Bits(k, derivePublic(algorithm, "X25519"), length == null ? -1 : length));
      }
      if (length === null || length === undefined || length % 8 !== 0) throw new DOMException("Invalid length", "OperationError");
      if (alg === "PBKDF2") return buf(host.pbkdf2(hashName(algorithm.hash), k, bytesOf(algorithm.salt), algorithm.iterations, length));
      if (alg === "HKDF") return buf(host.hkdf(hashName(algorithm.hash), k, bytesOf(algorithm.salt), bytesOf(algorithm.info), length));
      notSupported(`deriveBits with ${alg}`);
    });
  }

  async deriveKey(algorithm: any, baseKey: CryptoKey, derivedAlg: any, extractable: boolean, usages: Usage[]) {
    const alg = normName(algorithm);
    checkUsage(baseKey, "deriveKey", alg);
    const target = normName(derivedAlg);
    const length = target === "HMAC" ? (derivedAlg.length ?? hashBlockBits[hashName(derivedAlg.hash)]) : derivedAlg.length;
    const k = keyData.get(baseKey)!;
    let bits: ArrayBuffer | null;
    if (alg === "PBKDF2") bits = host.pbkdf2(hashName(algorithm.hash), k, bytesOf(algorithm.salt), algorithm.iterations, length);
    else if (alg === "HKDF") bits = host.hkdf(hashName(algorithm.hash), k, bytesOf(algorithm.salt), bytesOf(algorithm.info), length);
    else if (alg === "ECDH") bits = host.ecdhBits(k, derivePublic(algorithm, "ECDH"), length);
    else if (alg === "X25519") bits = host.x25519Bits(k, derivePublic(algorithm, "X25519"), length);
    else notSupported(`deriveKey with ${alg}`);
    return makeKey(target, derivedAlg, new Uint8Array(buf(bits)), extractable, usages);
  }
}
tag(SubtleCrypto, "SubtleCrypto");

const integerArrays = [Int8Array, Uint8Array, Uint8ClampedArray, Int16Array, Uint16Array, Int32Array, Uint32Array, BigInt64Array, BigUint64Array];

class Crypto {
  #subtle = new SubtleCrypto(kCreate);
  constructor(key?: symbol) { if (key !== kCreate) throw new TypeError("Illegal constructor"); }
  get subtle() { return this.#subtle; }
  getRandomValues<T extends ArrayBufferView>(array: T): T {
    if (!integerArrays.some((C) => array instanceof C)) throw new DOMException("The data argument must be an integer-type TypedArray", "TypeMismatchError");
    if (array.byteLength > 65536) throw new DOMException("The ArrayBufferView's byte length exceeds 65536", "QuotaExceededError");
    new Uint8Array(array.buffer, array.byteOffset, array.byteLength).set(new Uint8Array(host.randomBytes(array.byteLength)));
    return array;
  }
  randomUUID() { return host.randomUUID(); }
}
tag(Crypto, "Crypto");

export function installCrypto() {
  defineGlobal("CryptoKey", CryptoKey);
  defineGlobal("SubtleCrypto", SubtleCrypto);
  defineGlobal("Crypto", Crypto);
  defineGlobal("crypto", new Crypto(kCreate));
}
