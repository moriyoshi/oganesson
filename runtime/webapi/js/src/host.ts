// The Go host object, injected by webapi.Install. Shapes mirror the Go
// registrations in runtime/engine and runtime/webapi.
export interface Host {
  timers: {
    set(fn: () => void, ms: number, repeat: boolean): number;
    clear(id: number): void;
    ref(id: number, ref: boolean): void;
    now(): number;
  };
  AsyncLocalStorage: any;
  write(stream: 1 | 2, s: string): void;
  stringProperty(name: string): { content: string; strings: string[] } | undefined;
  env: Record<string, string>;
  pid: number;
  ppid: number;
  baseURL: string;
  utf8Encode(s: string): ArrayBuffer;
  abDetach(b: ArrayBuffer): boolean;
  abDetached(b: ArrayBuffer): boolean;
  newDecoder(label: string, fatal: boolean): { name: string; decode(b: Uint8Array, stream: boolean): string } | null;
  urlParse(input: string, base?: string): URLRecord | null;
  urlSet(href: string, component: string, value: string): URLRecord | null;
  randomBytes(n: number): ArrayBuffer;
  randomUUID(): string;
  digest(alg: string, data: Uint8Array): ArrayBuffer | null;
  hmac(alg: string, key: Uint8Array, data: Uint8Array): ArrayBuffer | null;
  aesGcm(encrypt: boolean, key: Uint8Array, iv: Uint8Array, data: Uint8Array, aad: Uint8Array | null, tagBits: number): ArrayBuffer | null;
  aesCbc(encrypt: boolean, key: Uint8Array, iv: Uint8Array, data: Uint8Array): ArrayBuffer | null;
  aesCtr(key: Uint8Array, counter: Uint8Array, data: Uint8Array): ArrayBuffer | null;
  pbkdf2(alg: string, password: Uint8Array, salt: Uint8Array, iterations: number, bits: number): ArrayBuffer | null;
  hkdf(alg: string, ikm: Uint8Array, salt: Uint8Array, info: Uint8Array, bits: number): ArrayBuffer | null;
  // Ed25519: keys are the raw 32-byte seed (private) or public key.
  ed25519Generate(): ArrayBuffer | null;
  ed25519Public(seed: Uint8Array): ArrayBuffer | null;
  ed25519Sign(seed: Uint8Array, data: Uint8Array): ArrayBuffer | null;
  ed25519Verify(pub: Uint8Array, sig: Uint8Array, data: Uint8Array): boolean;
  ed25519Pkcs8(seed: Uint8Array): ArrayBuffer | null;
  ed25519Spki(pub: Uint8Array): ArrayBuffer | null;
  ed25519SeedFromPkcs8(der: Uint8Array): ArrayBuffer | null;
  ed25519PubFromSpki(der: Uint8Array): ArrayBuffer | null;
  // ECDSA: keys are DER (pkcs8 private, spki public); sigs are IEEE P1363 r||s;
  // jwk coordinates travel as the uncompressed point (0x04||X||Y) and scalar d.
  ecdsaGenerate(curve: string): ArrayBuffer | null;
  ecdsaToSpki(pkcs8: Uint8Array): ArrayBuffer | null;
  ecdsaSign(pkcs8: Uint8Array, hash: string, data: Uint8Array): ArrayBuffer | null;
  ecdsaVerify(spki: Uint8Array, hash: string, sig: Uint8Array, data: Uint8Array): boolean;
  ecdsaPointFromSpki(spki: Uint8Array): ArrayBuffer | null;
  ecdsaSpkiFromPoint(curve: string, point: Uint8Array): ArrayBuffer | null;
  ecdsaDFromPkcs8(pkcs8: Uint8Array): ArrayBuffer | null;
  ecdsaPkcs8FromD(curve: string, d: Uint8Array): ArrayBuffer | null;
  ecdsaCurve(der: Uint8Array, isPrivate: boolean): string;
  // ECDH: the shared secret of a private (pkcs8) and public (spki) EC key;
  // bits < 0 derives the whole secret.
  ecdhBits(privPkcs8: Uint8Array, pubSpki: Uint8Array, bits: number): ArrayBuffer | null;
  // X25519: keys are the raw 32-byte scalar (private) or public key.
  x25519Generate(): ArrayBuffer | null;
  x25519Public(priv: Uint8Array): ArrayBuffer | null;
  x25519Bits(priv: Uint8Array, pub: Uint8Array, bits: number): ArrayBuffer | null;
  x25519Pkcs8(priv: Uint8Array): ArrayBuffer | null;
  x25519Spki(pub: Uint8Array): ArrayBuffer | null;
  x25519PrivFromPkcs8(der: Uint8Array): ArrayBuffer | null;
  x25519PubFromSpki(der: Uint8Array): ArrayBuffer | null;
  // RSA: keys are DER (pkcs8 private, spki public); jwk fields are an object
  // of base64url strings (n, e [, d, p, q, dp, dq, qi]).
  rsaGenerate(bits: number): ArrayBuffer | null;
  rsaToSpki(pkcs8: Uint8Array): ArrayBuffer | null;
  rsaSign(pkcs8: Uint8Array, hash: string, pss: boolean, saltLen: number, data: Uint8Array): ArrayBuffer | null;
  rsaVerify(spki: Uint8Array, hash: string, pss: boolean, saltLen: number, sig: Uint8Array, data: Uint8Array): boolean;
  rsaOaepEncrypt(spki: Uint8Array, hash: string, label: Uint8Array, data: Uint8Array): ArrayBuffer | null;
  rsaOaepDecrypt(pkcs8: Uint8Array, hash: string, label: Uint8Array, data: Uint8Array): ArrayBuffer | null;
  rsaJwk(der: Uint8Array, isPrivate: boolean): Record<string, string> | null;
  rsaFromJwk(jwk: any, isPrivate: boolean): ArrayBuffer | null;
  fetchStart(spec: { method: string; url: string; headers: [string, string][]; body: Uint8Array | null }): [number, Promise<FetchResult>];
  fetchAbort(id: number): void;
  bodyRead(id: number): Promise<ArrayBuffer | null>;
  bodyClose(id: number): void;
  collator(locales: string[], sensitivity: string, numeric: boolean): { locale: string; compare(a: string, b: string): number };
  // write: compression returns the output; decompression [output, error].
  compressor(format: string, decompress: boolean): { write(b: Uint8Array): ArrayBuffer | [ArrayBuffer, string | null]; finish(): ArrayBuffer };
}

export interface URLRecord {
  href: string; origin: string; protocol: string; username: string; password: string;
  host: string; hostname: string; port: string; pathname: string; search: string; hash: string;
}

export interface FetchResult {
  status: number; statusText: string; url: string; headers: [string, string][]; bodyId: number;
}

export let host: Host;
export function setHost(h: Host) { host = h; }
