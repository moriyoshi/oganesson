# WebCrypto

## Summary

Oganesson's `crypto.subtle` implementation covers the asymmetric algorithms
needed by modern web and framework code without delegating to Node or native
addons. Go performs the cryptographic operations, while the TypeScript prelude
normalizes WebCrypto algorithm objects, key usages, formats, and JavaScript
values.

## Key Facts

- Supported asymmetric families: Ed25519 (EdDSA), ECDSA, RSA signatures,
  RSA-OAEP, ECDH, and X25519.
- ECDSA and ECDH support P-256, P-384, and P-521.
- RSA signing supports RSASSA-PKCS1-v1_5 and RSA-PSS.
- EC/RSA private and public keys cross the host boundary as PKCS #8 and SPKI
  DER; Ed25519/X25519 also support raw seed/public representations.
- ECDSA signatures use the WebCrypto IEEE P1363 `r || s` representation.
- Editing `runtime/webapi/js/src` requires regenerating
  `runtime/webapi/prelude.js` with `go generate ./runtime/webapi/`.

## Details

`runtime/webapi/crypto.go` implements key generation/import/export and the
algorithm operations. `runtime/webapi/js/src/crypto.ts` and `host.ts` implement
the JavaScript-facing WebCrypto behavior and host calls.

Ed25519 provides EdDSA signing and verification. ECDSA supports the three NIST
curves and converts signatures to and from fixed-width IEEE P1363 form rather
than exposing Go's ASN.1 encoding. RSA covers RSASSA-PKCS1-v1_5, RSA-PSS, and
RSA-OAEP. ECDH derives secrets on P-256/P-384/P-521; X25519 provides the modern
raw-key agreement path.

EC and RSA JWK handling follows the WebCrypto field shapes. RSA JWK conversion
is implemented in Go; EC JWK conversion splits and joins the uncompressed curve
point in the JavaScript layer. Key material crossing the Go/JS boundary uses
raw seed/public bytes for Ed25519/X25519 and PKCS #8/SPKI DER for EC/RSA,
avoiding lossy object conversion.

## Files

- `runtime/webapi/crypto.go` — Go cryptographic primitives and host operations.
- `runtime/webapi/js/src/crypto.ts` — WebCrypto API normalization and key
  format handling.
- `runtime/webapi/js/src/host.ts` — host interface declarations.
- `runtime/webapi/prelude.js` — generated runtime prelude.
- `runtime/webapi/webapi_test.go` — WebCrypto behavior tests.

## Test Coverage

`TestCrypto` has per-algorithm subtests covering generation, import/export,
sign/verify or derive/encrypt/decrypt round trips, tampered-signature rejection,
and re-import of every supported key format. The prelude was regenerated after
the TypeScript changes.

## Pitfalls

- Go's ECDSA helpers naturally use ASN.1 signatures, but WebCrypto requires
  fixed-width IEEE P1363 `r || s` bytes.
- Raw and DER representations are algorithm-specific; do not route private
  material through generic JSON conversion.
- Verification of a tampered signature must resolve to `false`, not surface a
  host error.
