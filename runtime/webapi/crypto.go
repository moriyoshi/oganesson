package webapi

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"hash"
	"math/big"

	"github.com/grafana/sobek"
)

// installCrypto registers the primitives behind crypto and crypto.subtle.
// Failures return null; the JS layer turns that into an OperationError.
//
// Ed25519 (EdDSA), ECDSA, ECDH (P-256/384/521), X25519 and RSA
// (RSASSA-PKCS1-v1_5, RSA-PSS, RSA-OAEP) are supported, with raw/spki/pkcs8/jwk
// key formats. This covers the WebCrypto asymmetric algorithms.
func (h *host) installCrypto() {
	h.set("randomBytes", func(n int) sobek.Value {
		b := make([]byte, n)
		_, _ = rand.Read(b)
		return h.buffer(b)
	})
	h.set("randomUUID", uuid4)
	h.set("digest", func(alg string, data sobek.Value) sobek.Value {
		newHash := hashFor(alg)
		if newHash == nil {
			return sobek.Null()
		}
		hh := newHash()
		hh.Write(h.bytes(data))
		return h.buffer(hh.Sum(nil))
	})
	h.set("hmac", func(alg string, key, data sobek.Value) sobek.Value {
		newHash := hashFor(alg)
		if newHash == nil {
			return sobek.Null()
		}
		m := hmac.New(newHash, h.bytes(key))
		m.Write(h.bytes(data))
		return h.buffer(m.Sum(nil))
	})
	h.set("aesGcm", func(encrypt bool, key, iv, data, aad sobek.Value, tagBits int) sobek.Value {
		block, err := aes.NewCipher(h.bytes(key))
		if err != nil {
			return sobek.Null()
		}
		nonce := h.bytes(iv)
		var gcm cipher.AEAD
		switch {
		case tagBits/8 != 16 && len(nonce) == 12:
			gcm, err = cipher.NewGCMWithTagSize(block, tagBits/8)
		case tagBits/8 == 16 && len(nonce) != 12:
			gcm, err = cipher.NewGCMWithNonceSize(block, len(nonce))
		case tagBits/8 == 16:
			gcm, err = cipher.NewGCM(block)
		default:
			return sobek.Null() // non-default tag and nonce sizes together
		}
		if err != nil {
			return sobek.Null()
		}
		if encrypt {
			return h.buffer(gcm.Seal(nil, nonce, h.bytes(data), h.bytes(aad)))
		}
		out, err := gcm.Open(nil, nonce, h.bytes(data), h.bytes(aad))
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(out)
	})
	h.set("aesCbc", func(encrypt bool, key, iv, data sobek.Value) sobek.Value {
		block, err := aes.NewCipher(h.bytes(key))
		in, ivb := h.bytes(data), h.bytes(iv)
		if err != nil || len(ivb) != aes.BlockSize {
			return sobek.Null()
		}
		if encrypt {
			pad := aes.BlockSize - len(in)%aes.BlockSize
			for i := 0; i < pad; i++ {
				in = append(in, byte(pad))
			}
			cipher.NewCBCEncrypter(block, ivb).CryptBlocks(in, in)
			return h.buffer(in)
		}
		if len(in) == 0 || len(in)%aes.BlockSize != 0 {
			return sobek.Null()
		}
		cipher.NewCBCDecrypter(block, ivb).CryptBlocks(in, in)
		pad := int(in[len(in)-1])
		if pad == 0 || pad > aes.BlockSize || pad > len(in) {
			return sobek.Null()
		}
		for _, b := range in[len(in)-pad:] {
			if int(b) != pad {
				return sobek.Null()
			}
		}
		return h.buffer(in[:len(in)-pad])
	})
	h.set("aesCtr", func(key, counter, data sobek.Value) sobek.Value {
		block, err := aes.NewCipher(h.bytes(key))
		ctr := h.bytes(counter)
		if err != nil || len(ctr) != aes.BlockSize {
			return sobek.Null()
		}
		in := h.bytes(data)
		cipher.NewCTR(block, ctr).XORKeyStream(in, in)
		return h.buffer(in)
	})
	h.set("pbkdf2", func(alg string, password, salt sobek.Value, iterations, bits int) sobek.Value {
		newHash := hashFor(alg)
		if newHash == nil || iterations <= 0 || bits%8 != 0 {
			return sobek.Null()
		}
		out, err := pbkdf2.Key(newHash, string(h.bytes(password)), h.bytes(salt), iterations, bits/8)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(out)
	})
	h.set("hkdf", func(alg string, ikm, salt, info sobek.Value, bits int) sobek.Value {
		newHash := hashFor(alg)
		if newHash == nil || bits%8 != 0 {
			return sobek.Null()
		}
		out, err := hkdf.Key(newHash, h.bytes(ikm), h.bytes(salt), string(h.bytes(info)), bits/8)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(out)
	})

	// Ed25519 (EdDSA): keys are stored raw by the JS layer (the 32-byte seed
	// for a private key, the 32-byte public key for a public one); the DER
	// helpers convert to and from the pkcs8/spki import-export formats.
	h.set("ed25519Generate", func() sobek.Value {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(priv.Seed())
	})
	h.set("ed25519Public", func(seed sobek.Value) sobek.Value {
		s := h.bytes(seed)
		if len(s) != ed25519.SeedSize {
			return sobek.Null()
		}
		return h.buffer(ed25519.NewKeyFromSeed(s).Public().(ed25519.PublicKey))
	})
	h.set("ed25519Sign", func(seed, data sobek.Value) sobek.Value {
		s := h.bytes(seed)
		if len(s) != ed25519.SeedSize {
			return sobek.Null()
		}
		return h.buffer(ed25519.Sign(ed25519.NewKeyFromSeed(s), h.bytes(data)))
	})
	h.set("ed25519Verify", func(pub, sig, data sobek.Value) bool {
		p := h.bytes(pub)
		return len(p) == ed25519.PublicKeySize && ed25519.Verify(ed25519.PublicKey(p), h.bytes(data), h.bytes(sig))
	})
	h.set("ed25519Pkcs8", func(seed sobek.Value) sobek.Value {
		s := h.bytes(seed)
		if len(s) != ed25519.SeedSize {
			return sobek.Null()
		}
		der, err := x509.MarshalPKCS8PrivateKey(ed25519.NewKeyFromSeed(s))
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("ed25519Spki", func(pub sobek.Value) sobek.Value {
		p := h.bytes(pub)
		if len(p) != ed25519.PublicKeySize {
			return sobek.Null()
		}
		der, err := x509.MarshalPKIXPublicKey(ed25519.PublicKey(p))
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("ed25519SeedFromPkcs8", func(der sobek.Value) sobek.Value {
		k, err := x509.ParsePKCS8PrivateKey(h.bytes(der))
		if err != nil {
			return sobek.Null()
		}
		ed, ok := k.(ed25519.PrivateKey)
		if !ok {
			return sobek.Null()
		}
		return h.buffer(ed.Seed())
	})
	h.set("ed25519PubFromSpki", func(der sobek.Value) sobek.Value {
		k, err := x509.ParsePKIXPublicKey(h.bytes(der))
		if err != nil {
			return sobek.Null()
		}
		ed, ok := k.(ed25519.PublicKey)
		if !ok {
			return sobek.Null()
		}
		return h.buffer([]byte(ed))
	})

	// ECDSA (P-256/384/521): keys are stored by the JS layer as DER (pkcs8 for
	// a private key, spki for a public one); signatures are the WebCrypto IEEE
	// P1363 fixed-length r||s form, not ASN.1. jwk coordinates travel as the
	// uncompressed EC point (0x04||X||Y) and the private scalar d.
	h.set("ecdsaGenerate", func(curve string) sobek.Value {
		c := ecCurve(curve)
		if c == nil {
			return sobek.Null()
		}
		priv, err := ecdsa.GenerateKey(c, rand.Reader)
		if err != nil {
			return sobek.Null()
		}
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("ecdsaToSpki", func(pkcs8 sobek.Value) sobek.Value {
		priv := parseECPriv(h.bytes(pkcs8))
		if priv == nil {
			return sobek.Null()
		}
		der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("ecdsaSign", func(pkcs8 sobek.Value, hashAlg string, data sobek.Value) sobek.Value {
		priv := parseECPriv(h.bytes(pkcs8))
		newHash := hashFor(hashAlg)
		if priv == nil || newHash == nil {
			return sobek.Null()
		}
		hh := newHash()
		hh.Write(h.bytes(data))
		r, s, err := ecdsa.Sign(rand.Reader, priv, hh.Sum(nil))
		if err != nil {
			return sobek.Null()
		}
		n := (priv.Curve.Params().BitSize + 7) / 8
		sig := make([]byte, 2*n)
		r.FillBytes(sig[:n])
		s.FillBytes(sig[n:])
		return h.buffer(sig)
	})
	h.set("ecdsaVerify", func(spki sobek.Value, hashAlg string, sig, data sobek.Value) bool {
		pub := parseECPub(h.bytes(spki))
		newHash := hashFor(hashAlg)
		if pub == nil || newHash == nil {
			return false
		}
		n := (pub.Curve.Params().BitSize + 7) / 8
		s := h.bytes(sig)
		if len(s) != 2*n {
			return false
		}
		hh := newHash()
		hh.Write(h.bytes(data))
		return ecdsa.Verify(pub, hh.Sum(nil), new(big.Int).SetBytes(s[:n]), new(big.Int).SetBytes(s[n:]))
	})
	h.set("ecdsaPointFromSpki", func(spki sobek.Value) sobek.Value {
		pub := parseECPub(h.bytes(spki))
		if pub == nil {
			return sobek.Null()
		}
		return h.buffer(elliptic.Marshal(pub.Curve, pub.X, pub.Y))
	})
	h.set("ecdsaSpkiFromPoint", func(curve string, point sobek.Value) sobek.Value {
		c := ecCurve(curve)
		if c == nil {
			return sobek.Null()
		}
		x, y := elliptic.Unmarshal(c, h.bytes(point))
		if x == nil {
			return sobek.Null()
		}
		der, err := x509.MarshalPKIXPublicKey(&ecdsa.PublicKey{Curve: c, X: x, Y: y})
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("ecdsaDFromPkcs8", func(pkcs8 sobek.Value) sobek.Value {
		priv := parseECPriv(h.bytes(pkcs8))
		if priv == nil {
			return sobek.Null()
		}
		n := (priv.Curve.Params().BitSize + 7) / 8
		d := make([]byte, n)
		priv.D.FillBytes(d)
		return h.buffer(d)
	})
	h.set("ecdsaPkcs8FromD", func(curve string, d sobek.Value) sobek.Value {
		c := ecCurve(curve)
		if c == nil {
			return sobek.Null()
		}
		db := h.bytes(d)
		priv := new(ecdsa.PrivateKey)
		priv.Curve = c
		priv.D = new(big.Int).SetBytes(db)
		priv.X, priv.Y = c.ScalarBaseMult(db)
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("ecdsaCurve", func(der sobek.Value, private bool) string {
		if private {
			if priv := parseECPriv(h.bytes(der)); priv != nil {
				return curveName(priv.Curve)
			}
			return ""
		}
		if pub := parseECPub(h.bytes(der)); pub != nil {
			return curveName(pub.Curve)
		}
		return ""
	})
	h.set("ecdhBits", func(privPkcs8, pubSpki sobek.Value, bits int) sobek.Value {
		priv, pub := parseECPriv(h.bytes(privPkcs8)), parseECPub(h.bytes(pubSpki))
		if priv == nil || pub == nil {
			return sobek.Null()
		}
		pe, err := priv.ECDH()
		if err != nil {
			return sobek.Null()
		}
		qe, err := pub.ECDH()
		if err != nil {
			return sobek.Null()
		}
		secret, err := pe.ECDH(qe) // the shared secret is the X coordinate
		if err != nil {
			return sobek.Null()
		}
		if bits < 0 { // a null length derives the whole secret
			return h.buffer(secret)
		}
		if bits/8 > len(secret) {
			return sobek.Null()
		}
		return h.buffer(secret[:bits/8])
	})

	// X25519 (ECDH over Curve25519): keys are the raw 32-byte scalar (private)
	// or public key, like Ed25519 but via crypto/ecdh.
	h.set("x25519Generate", func() sobek.Value {
		priv, err := ecdh.X25519().GenerateKey(rand.Reader)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(priv.Bytes())
	})
	h.set("x25519Public", func(priv sobek.Value) sobek.Value {
		k, err := ecdh.X25519().NewPrivateKey(h.bytes(priv))
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(k.PublicKey().Bytes())
	})
	h.set("x25519Bits", func(priv, pub sobek.Value, bits int) sobek.Value {
		pk, err := ecdh.X25519().NewPrivateKey(h.bytes(priv))
		if err != nil {
			return sobek.Null()
		}
		qk, err := ecdh.X25519().NewPublicKey(h.bytes(pub))
		if err != nil {
			return sobek.Null()
		}
		secret, err := pk.ECDH(qk)
		if err != nil {
			return sobek.Null()
		}
		if bits < 0 {
			return h.buffer(secret)
		}
		if bits/8 > len(secret) {
			return sobek.Null()
		}
		return h.buffer(secret[:bits/8])
	})
	h.set("x25519Pkcs8", func(priv sobek.Value) sobek.Value {
		k, err := ecdh.X25519().NewPrivateKey(h.bytes(priv))
		if err != nil {
			return sobek.Null()
		}
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("x25519Spki", func(pub sobek.Value) sobek.Value {
		k, err := ecdh.X25519().NewPublicKey(h.bytes(pub))
		if err != nil {
			return sobek.Null()
		}
		der, err := x509.MarshalPKIXPublicKey(k)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("x25519PrivFromPkcs8", func(der sobek.Value) sobek.Value {
		k, err := x509.ParsePKCS8PrivateKey(h.bytes(der))
		if err != nil {
			return sobek.Null()
		}
		pk, ok := k.(*ecdh.PrivateKey)
		if !ok || pk.Curve() != ecdh.X25519() {
			return sobek.Null()
		}
		return h.buffer(pk.Bytes())
	})
	h.set("x25519PubFromSpki", func(der sobek.Value) sobek.Value {
		k, err := x509.ParsePKIXPublicKey(h.bytes(der))
		if err != nil {
			return sobek.Null()
		}
		pk, ok := k.(*ecdh.PublicKey)
		if !ok || pk.Curve() != ecdh.X25519() {
			return sobek.Null()
		}
		return h.buffer(pk.Bytes())
	})

	// RSA (RSASSA-PKCS1-v1_5, RSA-PSS, RSA-OAEP): keys are stored by the JS
	// layer as DER (pkcs8 private, spki public); the hash lives on the JS
	// key's algorithm. jwk fields travel as base64url strings in an object.
	h.set("rsaGenerate", func(bits int) sobek.Value {
		if bits < 512 || bits > 16384 {
			return sobek.Null()
		}
		priv, err := rsa.GenerateKey(rand.Reader, bits) // Go fixes e = 65537
		if err != nil {
			return sobek.Null()
		}
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("rsaToSpki", func(pkcs8 sobek.Value) sobek.Value {
		priv := parseRSAPriv(h.bytes(pkcs8))
		if priv == nil {
			return sobek.Null()
		}
		der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
	h.set("rsaSign", func(pkcs8 sobek.Value, hashAlg string, pss bool, saltLen int, data sobek.Value) sobek.Value {
		priv := parseRSAPriv(h.bytes(pkcs8))
		ch := cryptoHash(hashAlg)
		newHash := hashFor(hashAlg)
		if priv == nil || ch == 0 || newHash == nil {
			return sobek.Null()
		}
		hh := newHash()
		hh.Write(h.bytes(data))
		digest := hh.Sum(nil)
		var sig []byte
		var err error
		if pss {
			sig, err = rsa.SignPSS(rand.Reader, priv, ch, digest, &rsa.PSSOptions{SaltLength: saltLen, Hash: ch})
		} else {
			sig, err = rsa.SignPKCS1v15(rand.Reader, priv, ch, digest)
		}
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(sig)
	})
	h.set("rsaVerify", func(spki sobek.Value, hashAlg string, pss bool, saltLen int, sig, data sobek.Value) bool {
		pub := parseRSAPub(h.bytes(spki))
		ch := cryptoHash(hashAlg)
		newHash := hashFor(hashAlg)
		if pub == nil || ch == 0 || newHash == nil {
			return false
		}
		hh := newHash()
		hh.Write(h.bytes(data))
		digest := hh.Sum(nil)
		if pss {
			return rsa.VerifyPSS(pub, ch, digest, h.bytes(sig), &rsa.PSSOptions{SaltLength: saltLen, Hash: ch}) == nil
		}
		return rsa.VerifyPKCS1v15(pub, ch, digest, h.bytes(sig)) == nil
	})
	h.set("rsaOaepEncrypt", func(spki sobek.Value, hashAlg string, label, data sobek.Value) sobek.Value {
		pub := parseRSAPub(h.bytes(spki))
		newHash := hashFor(hashAlg)
		if pub == nil || newHash == nil {
			return sobek.Null()
		}
		ct, err := rsa.EncryptOAEP(newHash(), rand.Reader, pub, h.bytes(data), h.bytes(label))
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(ct)
	})
	h.set("rsaOaepDecrypt", func(pkcs8 sobek.Value, hashAlg string, label, data sobek.Value) sobek.Value {
		priv := parseRSAPriv(h.bytes(pkcs8))
		newHash := hashFor(hashAlg)
		if priv == nil || newHash == nil {
			return sobek.Null()
		}
		pt, err := rsa.DecryptOAEP(newHash(), rand.Reader, priv, h.bytes(data), h.bytes(label))
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(pt)
	})
	h.set("rsaJwk", func(der sobek.Value, private bool) sobek.Value {
		b64 := func(x *big.Int) string { return base64.RawURLEncoding.EncodeToString(x.Bytes()) }
		if private {
			priv := parseRSAPriv(h.bytes(der))
			if priv == nil || len(priv.Primes) < 2 {
				return sobek.Null()
			}
			priv.Precompute()
			p, q := priv.Primes[0], priv.Primes[1]
			return h.vm.ToValue(map[string]any{
				"n": b64(priv.N), "e": b64(big.NewInt(int64(priv.E))), "d": b64(priv.D),
				"p": b64(p), "q": b64(q),
				"dp": b64(priv.Precomputed.Dp), "dq": b64(priv.Precomputed.Dq), "qi": b64(priv.Precomputed.Qinv),
			})
		}
		pub := parseRSAPub(h.bytes(der))
		if pub == nil {
			return sobek.Null()
		}
		return h.vm.ToValue(map[string]any{"n": b64(pub.N), "e": b64(big.NewInt(int64(pub.E)))})
	})
	h.set("rsaFromJwk", func(jwk sobek.Value, private bool) sobek.Value {
		o := jwk.ToObject(h.vm)
		field := func(name string) *big.Int {
			v := o.Get(name)
			if v == nil || sobek.IsUndefined(v) {
				return nil
			}
			b, err := base64.RawURLEncoding.DecodeString(v.String())
			if err != nil {
				return nil
			}
			return new(big.Int).SetBytes(b)
		}
		n, e := field("n"), field("e")
		if n == nil || e == nil || !e.IsInt64() {
			return sobek.Null()
		}
		pub := rsa.PublicKey{N: n, E: int(e.Int64())}
		if !private {
			der, err := x509.MarshalPKIXPublicKey(&pub)
			if err != nil {
				return sobek.Null()
			}
			return h.buffer(der)
		}
		d, p, q := field("d"), field("p"), field("q")
		if d == nil || p == nil || q == nil {
			return sobek.Null()
		}
		priv := &rsa.PrivateKey{PublicKey: pub, D: d, Primes: []*big.Int{p, q}}
		if err := priv.Validate(); err != nil {
			return sobek.Null()
		}
		priv.Precompute()
		der, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return sobek.Null()
		}
		return h.buffer(der)
	})
}

func cryptoHash(alg string) crypto.Hash {
	switch alg {
	case "SHA-1":
		return crypto.SHA1
	case "SHA-256":
		return crypto.SHA256
	case "SHA-384":
		return crypto.SHA384
	case "SHA-512":
		return crypto.SHA512
	}
	return 0
}

func parseRSAPriv(der []byte) *rsa.PrivateKey {
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil
	}
	p, _ := k.(*rsa.PrivateKey)
	return p
}

func parseRSAPub(der []byte) *rsa.PublicKey {
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil
	}
	p, _ := k.(*rsa.PublicKey)
	return p
}

func ecCurve(name string) elliptic.Curve {
	switch name {
	case "P-256":
		return elliptic.P256()
	case "P-384":
		return elliptic.P384()
	case "P-521":
		return elliptic.P521()
	}
	return nil
}

func curveName(c elliptic.Curve) string {
	switch c {
	case elliptic.P256():
		return "P-256"
	case elliptic.P384():
		return "P-384"
	case elliptic.P521():
		return "P-521"
	}
	return ""
}

func parseECPriv(der []byte) *ecdsa.PrivateKey {
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil
	}
	p, _ := k.(*ecdsa.PrivateKey)
	return p
}

func parseECPub(der []byte) *ecdsa.PublicKey {
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil
	}
	p, _ := k.(*ecdsa.PublicKey)
	return p
}

func hashFor(alg string) func() hash.Hash {
	switch alg {
	case "MD5": // not WebCrypto; used by node:crypto createHash
		return md5.New
	case "SHA-1":
		return sha1.New
	case "SHA-256":
		return sha256.New
	case "SHA-384":
		return sha512.New384
	case "SHA-512":
		return sha512.New
	}
	return nil
}

func uuid4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, c := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hex[c>>4], hex[c&15])
	}
	return string(out)
}
