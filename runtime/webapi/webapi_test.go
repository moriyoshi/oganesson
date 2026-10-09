package webapi_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// eval runs an async JS function body and returns its awaited result as a string.
func eval(t *testing.T, opts webapi.Options, body string) string {
	t.Helper()
	e := engine.New(engine.Options{})
	defer e.Close()
	var out bytes.Buffer
	if opts.Stdout == nil {
		opts.Stdout = &out
	}
	if _, err := webapi.Install(e, opts); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := e.VM.RunString("(async () => {" + body + "\n})()")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Await(ctx, v)
	if err != nil {
		t.Fatalf("%v\nstdout: %s", err, out.String())
	}
	if sobek.IsUndefined(res) {
		return out.String()
	}
	return res.String()
}

func check(t *testing.T, name, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %q\nwant %q", name, got, want)
	}
}

func TestEncoding(t *testing.T) {
	check(t, "roundtrip", eval(t, webapi.Options{}, `
		const b = new TextEncoder().encode("héllo 🌍");
		return b.length + ":" + new TextDecoder().decode(b);
	`), "11:héllo 🌍")
	check(t, "maximal subpart", eval(t, webapi.Options{}, `
		return [...new TextDecoder().decode(new Uint8Array([0x61, 0xE2, 0x82, 0x41, 0xF0, 0x9F]))].map(c => c.codePointAt(0).toString(16)).join(" ");
	`), "61 fffd 41 fffd")
	check(t, "streaming", eval(t, webapi.Options{}, `
		const d = new TextDecoder();
		const bytes = new TextEncoder().encode("€");
		return d.decode(bytes.subarray(0, 1), {stream: true}) + d.decode(bytes.subarray(1), {stream: true}) + d.decode();
	`), "€")
	check(t, "bom", eval(t, webapi.Options{}, `
		return new TextDecoder().decode(new Uint8Array([0xEF,0xBB,0xBF,0x41])) + "|" +
			new TextDecoder("utf-8", {ignoreBOM: true}).decode(new Uint8Array([0xEF,0xBB,0xBF,0x41])).length;
	`), "A|2")
	check(t, "legacy", eval(t, webapi.Options{}, `
		const d = new TextDecoder("shift_jis");
		return d.encoding + ":" + d.decode(new Uint8Array([0x82, 0xa0]));
	`), "shift_jis:あ")
	check(t, "fatal", eval(t, webapi.Options{}, `
		try { new TextDecoder("utf-8", {fatal: true}).decode(new Uint8Array([0xff])); return "no throw"; }
		catch (e) { return e.name; }
	`), "TypeError")
	check(t, "base64", eval(t, webapi.Options{}, `return btoa("hello") + ":" + atob(" aGVs bG8= ");`), "aGVsbG8=:hello")
}

func TestURL(t *testing.T) {
	check(t, "parse", eval(t, webapi.Options{}, `
		const u = new URL("../x?a=1&b=two words#frag", "https://user:pw@EXAMPLE.com:8443/p/q/r");
		return [u.href, u.origin, u.host, u.pathname, u.search, u.hash, u.searchParams.get("b")].join("|");
	`), "https://user:pw@example.com:8443/p/x?a=1&b=two%20words#frag|https://example.com:8443|example.com:8443|/p/x|?a=1&b=two%20words|#frag|two words")
	check(t, "idna", eval(t, webapi.Options{}, `return new URL("http://日本.jp/").hostname;`), "xn--wgv71a.jp")
	check(t, "searchParams sync", eval(t, webapi.Options{}, `
		const u = new URL("http://h/?a=1");
		u.searchParams.append("b", "x y");
		u.searchParams.delete("a");
		const s1 = u.href;
		u.search = "?z=9";
		return s1 + "|" + u.searchParams.get("z") + "|" + [...new URLSearchParams({q: "1", r: "é"})].join(";");
	`), "http://h/?b=x+y|9|q,1;r,é")
	check(t, "invalid", eval(t, webapi.Options{}, `
		try { new URL("nope"); return "no throw"; } catch (e) { return e.constructor.name + ":" + URL.canParse("nope") + ":" + URL.canParse("a:b"); }
	`), "TypeError:false:true")
	check(t, "setters", eval(t, webapi.Options{}, `
		const u = new URL("http://example.com/a");
		u.port = "8080"; u.pathname = "/b c"; u.hash = "h"; u.protocol = "https";
		return u.href;
	`), "https://example.com:8080/b%20c#h")
}

func TestEventsAndAbort(t *testing.T) {
	check(t, "abort", eval(t, webapi.Options{}, `
		const log = [];
		const c = new AbortController();
		c.signal.addEventListener("abort", (e) => log.push("listener:" + e.type), { once: true });
		c.signal.onabort = () => log.push("onabort");
		const any = AbortSignal.any([c.signal]);
		c.abort();
		c.abort();
		log.push(c.signal.reason.name, any.aborted, c.signal.reason instanceof DOMException, c.signal.reason.code);
		// The timeout timer is unref'd (as in Node), so keep the loop alive.
		const t = AbortSignal.timeout(5);
		const keepAlive = setTimeout(() => {}, 1000);
		await new Promise((r) => t.addEventListener("abort", r));
		clearTimeout(keepAlive);
		log.push(t.reason.name);
		return log.join(",");
	`), "listener:abort,onabort,AbortError,true,true,20,TimeoutError")
}

func TestStreamsAndBodies(t *testing.T) {
	check(t, "pipe", eval(t, webapi.Options{}, `
		const rs = new ReadableStream({ start(c) { c.enqueue("a"); c.enqueue("é"); c.close(); } });
		const text = await new Response(rs.pipeThrough(new TextEncoderStream())).text();
		const chunks = [];
		for await (const c of new Blob(["xy"]).stream().pipeThrough(new TextDecoderStream())) chunks.push(c);
		return text + "|" + chunks.join("");
	`), "aé|xy")
	check(t, "response", eval(t, webapi.Options{}, `
		const r = Response.json({ a: 1 }, { status: 201, headers: { "X-A": "1" } });
		const c = r.clone();
		const j = await r.json();
		let used; try { await r.text(); } catch (e) { used = e.constructor.name; }
		return [r.status, r.ok, r.headers.get("content-type"), j.a, await c.text(), r.bodyUsed, used].join("|");
	`), "201|true|application/json|1|{\"a\":1}|true|TypeError")
	check(t, "formdata", eval(t, webapi.Options{}, `
		const fd = new FormData();
		fd.append("name", "Oganesson");
		fd.append("file", new Blob(["hello"], { type: "text/plain" }), "h.txt");
		const req = new Request("http://x/", { method: "POST", body: fd });
		const back = await req.formData();
		const f = back.get("file");
		return [back.get("name"), f.name, f.type, await f.text(), req.headers.get("content-type").startsWith("multipart/form-data; boundary=")].join("|");
	`), "Oganesson|h.txt|text/plain|hello|true")
	check(t, "urlencoded", eval(t, webapi.Options{}, `
		const r = new Response(new URLSearchParams("a=1&b=x+y"));
		return r.headers.get("content-type") + "|" + (await r.formData()).get("b");
	`), "application/x-www-form-urlencoded;charset=UTF-8|x y")
	// An async iterable body (a Node stream, as undici and Node servers'
	// adapters pass an IncomingMessage) is read chunk by chunk.
	check(t, "async iterable", eval(t, webapi.Options{}, `
		const chunks = [new TextEncoder().encode("a="), "1"];
		const body = { [Symbol.asyncIterator]() { return { next: async () => chunks.length ? { done: false, value: chunks.shift() } : { done: true } }; } };
		const req = new Request("http://x/", { method: "POST", body, duplex: "half" });
		return (await req.text()) + "|" + (req.body instanceof ReadableStream);
	`), "a=1|true")
	check(t, "validation", eval(t, webapi.Options{}, `
		const errs = [];
		for (const f of [
			() => new Response("x", { status: 204 }),
			() => new Response(null, { status: 600 }),
			() => new Request("http://x/", { method: "GET", body: "x" }),
			() => new Request("http://x/", { method: "TRACE" }),
			() => new Headers({ "bad name": "x" }),
			() => Response.error().headers.set("a", "b"),
		]) { try { f(); errs.push("ok"); } catch (e) { errs.push(e.constructor.name); } }
		return errs.join(",");
	`), "TypeError,RangeError,TypeError,TypeError,TypeError,TypeError")
	check(t, "headers order", eval(t, webapi.Options{}, `
		const h = new Headers([["B", "2"], ["a", "1"], ["set-cookie", "x=1"], ["set-cookie", "y=2"], ["b", "3"]]);
		return JSON.stringify([...h]) + "|" + h.getSetCookie().length;
	`), `[["a","1"],["b","2, 3"],["set-cookie","x=1"],["set-cookie","y=2"]]|2`)
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/echo", http.StatusFound)
		case "/echo":
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("X-Method", r.Method)
			fmt.Fprintf(w, "%s %s %s", r.Method, r.Header.Get("X-Test"), b)
		case "/stream":
			f := w.(http.Flusher)
			for i := 0; i < 3; i++ {
				fmt.Fprintf(w, "chunk%d;", i)
				f.Flush()
				time.Sleep(5 * time.Millisecond)
			}
		case "/hang":
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	base := `const base = "` + srv.URL + `";`

	check(t, "post", eval(t, webapi.Options{}, base+`
		const r = await fetch(base + "/echo", { method: "POST", body: "payload", headers: { "X-Test": "yes" } });
		return [r.status, r.headers.get("x-method"), await r.text()].join("|");
	`), "200|POST|POST yes payload")
	check(t, "redirect 302 POST becomes GET", eval(t, webapi.Options{}, base+`
		const r = await fetch(base + "/redirect", { method: "POST", body: "x" });
		return [r.redirected, r.url.endsWith("/echo"), await r.text()].join("|");
	`), "true|true|GET  ")
	check(t, "manual redirect", eval(t, webapi.Options{}, base+`
		const r = await fetch(base + "/redirect", { redirect: "manual" });
		return r.status + "|" + r.headers.get("location");
	`), "302|/echo")
	check(t, "streaming body", eval(t, webapi.Options{}, base+`
		const r = await fetch(base + "/stream");
		const parts = [];
		const dec = new TextDecoder();
		for await (const c of r.body) parts.push(dec.decode(c));
		return parts.join("");
	`), "chunk0;chunk1;chunk2;")
	check(t, "abort", eval(t, webapi.Options{}, base+`
		try { await fetch(base + "/hang", { signal: AbortSignal.timeout(20) }); return "no abort"; }
		catch (e) { return e.name; }
	`), "TimeoutError")
	check(t, "data url", eval(t, webapi.Options{}, `
		const r = await fetch("data:text/plain;base64,aGk=");
		const r2 = await fetch("data:,a%20b");
		return r.headers.get("content-type") + "|" + await r.text() + "|" + await r2.text();
	`), "text/plain|hi|a b")
}

func TestCrypto(t *testing.T) {
	check(t, "digest", eval(t, webapi.Options{}, `
		const d = await crypto.subtle.digest("SHA-256", new TextEncoder().encode("abc"));
		return [...new Uint8Array(d)].map(b => b.toString(16).padStart(2, "0")).join("");
	`), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
	check(t, "hmac + aes-gcm + pbkdf2", eval(t, webapi.Options{}, `
		const enc = new TextEncoder();
		const hk = await crypto.subtle.importKey("raw", enc.encode("key"), { name: "HMAC", hash: "SHA-256" }, true, ["sign", "verify"]);
		const sig = await crypto.subtle.sign("HMAC", hk, enc.encode("msg"));
		const ok = await crypto.subtle.verify("HMAC", hk, sig, enc.encode("msg"));
		const jwk = await crypto.subtle.exportKey("jwk", hk);

		const ak = await crypto.subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
		const iv = crypto.getRandomValues(new Uint8Array(12));
		const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, ak, enc.encode("secret"));
		const pt = new TextDecoder().decode(await crypto.subtle.decrypt({ name: "AES-GCM", iv }, ak, ct));
		let tamper; try { await crypto.subtle.decrypt({ name: "AES-GCM", iv }, ak, new Uint8Array(ct).fill(0)); } catch (e) { tamper = e.name; }

		const pk = await crypto.subtle.importKey("raw", enc.encode("password"), "PBKDF2", false, ["deriveBits"]);
		const bits = await crypto.subtle.deriveBits({ name: "PBKDF2", hash: "SHA-256", salt: enc.encode("salt"), iterations: 1 }, pk, 256);
		const hex = [...new Uint8Array(bits)].map(b => b.toString(16).padStart(2, "0")).join("");
		return [ok, jwk.alg, pt, tamper, hex.slice(0, 16), crypto.randomUUID().length].join("|");
	`), "true|HS256|secret|OperationError|120fb6cffcf8b32c|36")
	check(t, "ed25519", eval(t, webapi.Options{}, `
		const enc = new TextEncoder();
		const msg = enc.encode("sign me");
		const { publicKey, privateKey } = await crypto.subtle.generateKey("Ed25519", true, ["sign", "verify"]);
		const sig = await crypto.subtle.sign("Ed25519", privateKey, msg);
		const ok = await crypto.subtle.verify("Ed25519", publicKey, sig, msg);
		const bad = await crypto.subtle.verify("Ed25519", publicKey, sig, enc.encode("other"));
		// Public key round-trips through spki and raw, still verifying the signature.
		const pubSpki = await crypto.subtle.importKey("spki", await crypto.subtle.exportKey("spki", publicKey), "Ed25519", true, ["verify"]);
		const okSpki = await crypto.subtle.verify("Ed25519", pubSpki, sig, msg);
		const pubRaw = await crypto.subtle.importKey("raw", await crypto.subtle.exportKey("raw", publicKey), "Ed25519", true, ["verify"]);
		const okRaw = await crypto.subtle.verify("Ed25519", pubRaw, sig, msg);
		// Private key round-trips through pkcs8 and jwk; Ed25519 is deterministic,
		// so the re-imported key signs identically.
		const privPkcs8 = await crypto.subtle.importKey("pkcs8", await crypto.subtle.exportKey("pkcs8", privateKey), "Ed25519", true, ["sign"]);
		const sig2 = await crypto.subtle.sign("Ed25519", privPkcs8, msg);
		const det = [...new Uint8Array(sig)].join() === [...new Uint8Array(sig2)].join();
		const jwk = await crypto.subtle.exportKey("jwk", privateKey);
		const privJwk = await crypto.subtle.importKey("jwk", jwk, "Ed25519", true, ["sign"]);
		const okJwk = await crypto.subtle.verify("Ed25519", publicKey, await crypto.subtle.sign("Ed25519", privJwk, msg), msg);
		return [ok, bad, okSpki, okRaw, det, okJwk, jwk.kty, jwk.crv, typeof jwk.d, new Uint8Array(sig).length].join("|");
	`), "true|false|true|true|true|true|OKP|Ed25519|string|64")
	check(t, "ecdsa", eval(t, webapi.Options{}, `
		const enc = new TextEncoder();
		const msg = enc.encode("sign me");
		const ec = { name: "ECDSA", namedCurve: "P-256" };
		const h = { name: "ECDSA", hash: "SHA-256" };
		const kp = await crypto.subtle.generateKey(ec, true, ["sign", "verify"]);
		const sig = await crypto.subtle.sign(h, kp.privateKey, msg);
		const ok = await crypto.subtle.verify(h, kp.publicKey, sig, msg);
		const bad = await crypto.subtle.verify(h, kp.publicKey, sig, enc.encode("other"));
		// Public key round-trips through spki, raw and jwk, still verifying.
		const pubSpki = await crypto.subtle.importKey("spki", await crypto.subtle.exportKey("spki", kp.publicKey), ec, true, ["verify"]);
		const okSpki = await crypto.subtle.verify(h, pubSpki, sig, msg);
		const pubRaw = await crypto.subtle.importKey("raw", await crypto.subtle.exportKey("raw", kp.publicKey), ec, true, ["verify"]);
		const okRaw = await crypto.subtle.verify(h, pubRaw, sig, msg);
		const jwk = await crypto.subtle.exportKey("jwk", kp.publicKey);
		const pubJwk = await crypto.subtle.importKey("jwk", jwk, ec, true, ["verify"]);
		const okJwk = await crypto.subtle.verify(h, pubJwk, sig, msg);
		// Private key round-trips through pkcs8 and jwk (ECDSA is not
		// deterministic, so re-sign and verify rather than compare bytes).
		const privPkcs8 = await crypto.subtle.importKey("pkcs8", await crypto.subtle.exportKey("pkcs8", kp.privateKey), ec, true, ["sign"]);
		const okPkcs8 = await crypto.subtle.verify(h, kp.publicKey, await crypto.subtle.sign(h, privPkcs8, msg), msg);
		const privJwk = await crypto.subtle.importKey("jwk", await crypto.subtle.exportKey("jwk", kp.privateKey), ec, true, ["sign"]);
		const okPrivJwk = await crypto.subtle.verify(h, kp.publicKey, await crypto.subtle.sign(h, privJwk, msg), msg);
		// A second curve exercises the curve dispatch and the signature size.
		const kp384 = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-384" }, true, ["sign", "verify"]);
		const h384 = { name: "ECDSA", hash: "SHA-384" };
		const sig384 = await crypto.subtle.sign(h384, kp384.privateKey, msg);
		const ok384 = await crypto.subtle.verify(h384, kp384.publicKey, sig384, msg);
		return [ok, bad, okSpki, okRaw, okJwk, okPkcs8, okPrivJwk, jwk.kty, jwk.crv, new Uint8Array(sig).length, ok384, new Uint8Array(sig384).length].join("|");
	`), "true|false|true|true|true|true|true|EC|P-256|64|true|96")
	check(t, "rsa", eval(t, webapi.Options{}, `
		const enc = new TextEncoder();
		const msg = enc.encode("sign me");
		// RSASSA-PKCS1-v1_5 (RS256).
		const rs = { name: "RSASSA-PKCS1-v1_5", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" };
		const kp = await crypto.subtle.generateKey(rs, true, ["sign", "verify"]);
		const sig = await crypto.subtle.sign("RSASSA-PKCS1-v1_5", kp.privateKey, msg);
		const ok = await crypto.subtle.verify("RSASSA-PKCS1-v1_5", kp.publicKey, sig, msg);
		const bad = await crypto.subtle.verify("RSASSA-PKCS1-v1_5", kp.publicKey, sig, enc.encode("other"));
		const pubSpki = await crypto.subtle.importKey("spki", await crypto.subtle.exportKey("spki", kp.publicKey), rs, true, ["verify"]);
		const okSpki = await crypto.subtle.verify("RSASSA-PKCS1-v1_5", pubSpki, sig, msg);
		const jwk = await crypto.subtle.exportKey("jwk", kp.publicKey);
		const pubJwk = await crypto.subtle.importKey("jwk", jwk, rs, true, ["verify"]);
		const okJwk = await crypto.subtle.verify("RSASSA-PKCS1-v1_5", pubJwk, sig, msg);
		const privPkcs8 = await crypto.subtle.importKey("pkcs8", await crypto.subtle.exportKey("pkcs8", kp.privateKey), rs, true, ["sign"]);
		const okPkcs8 = await crypto.subtle.verify("RSASSA-PKCS1-v1_5", kp.publicKey, await crypto.subtle.sign("RSASSA-PKCS1-v1_5", privPkcs8, msg), msg);
		const privJwk = await crypto.subtle.importKey("jwk", await crypto.subtle.exportKey("jwk", kp.privateKey), rs, true, ["sign"]);
		const okPrivJwk = await crypto.subtle.verify("RSASSA-PKCS1-v1_5", kp.publicKey, await crypto.subtle.sign("RSASSA-PKCS1-v1_5", privJwk, msg), msg);
		// RSA-PSS (PS256).
		const ps = { name: "RSA-PSS", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" };
		const kpp = await crypto.subtle.generateKey(ps, true, ["sign", "verify"]);
		const pss = { name: "RSA-PSS", saltLength: 32 };
		const sigp = await crypto.subtle.sign(pss, kpp.privateKey, msg);
		const okPss = await crypto.subtle.verify(pss, kpp.publicKey, sigp, msg);
		// RSA-OAEP encrypt/decrypt.
		const oa = { name: "RSA-OAEP", modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: "SHA-256" };
		const kpo = await crypto.subtle.generateKey(oa, true, ["encrypt", "decrypt"]);
		const ct = await crypto.subtle.encrypt({ name: "RSA-OAEP" }, kpo.publicKey, enc.encode("secret"));
		const pt = new TextDecoder().decode(await crypto.subtle.decrypt({ name: "RSA-OAEP" }, kpo.privateKey, ct));
		return [ok, bad, okSpki, okJwk, okPkcs8, okPrivJwk, jwk.kty, jwk.e, okPss, pt, new Uint8Array(sig).length].join("|");
	`), "true|false|true|true|true|true|RSA|AQAB|true|secret|256")
	check(t, "ecdh", eval(t, webapi.Options{}, `
		const ec = { name: "ECDH", namedCurve: "P-256" };
		const a = await crypto.subtle.generateKey(ec, true, ["deriveKey", "deriveBits"]);
		const b = await crypto.subtle.generateKey(ec, true, ["deriveKey", "deriveBits"]);
		// Both sides derive the same shared secret.
		const ab = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: b.publicKey }, a.privateKey, 256));
		const ba = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: a.publicKey }, b.privateKey, 256));
		const agree = [...ab].join() === [...ba].join();
		const full = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: b.publicKey }, a.privateKey, null));
		// deriveKey yields a usable AES-GCM key.
		const key = await crypto.subtle.deriveKey({ name: "ECDH", public: b.publicKey }, a.privateKey, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
		const enc = new TextEncoder();
		const iv = crypto.getRandomValues(new Uint8Array(12));
		const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, enc.encode("hi"));
		const pt = new TextDecoder().decode(await crypto.subtle.decrypt({ name: "AES-GCM", iv }, key, ct));
		// Public keys agreed across import: round-trip Bob's public key through spki.
		const bPub = await crypto.subtle.importKey("spki", await crypto.subtle.exportKey("spki", b.publicKey), ec, true, []);
		const ab2 = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: bPub }, a.privateKey, 256));
		const agree2 = [...ab].join() === [...ab2].join();
		// A different peer yields a different secret (not a constant).
		const c = await crypto.subtle.generateKey(ec, true, ["deriveKey", "deriveBits"]);
		const ac = new Uint8Array(await crypto.subtle.deriveBits({ name: "ECDH", public: c.publicKey }, a.privateKey, 256));
		const distinct = [...ab].join() !== [...ac].join();
		return [agree, agree2, distinct, ab.length, full.length, pt, a.publicKey.usages.length].join("|");
	`), "true|true|true|32|32|hi|0")
	check(t, "x25519", eval(t, webapi.Options{}, `
		const a = await crypto.subtle.generateKey("X25519", true, ["deriveKey", "deriveBits"]);
		const b = await crypto.subtle.generateKey("X25519", true, ["deriveKey", "deriveBits"]);
		const ab = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: b.publicKey }, a.privateKey, 256));
		const ba = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: a.publicKey }, b.privateKey, 256));
		const agree = [...ab].join() === [...ba].join();
		const c = await crypto.subtle.generateKey("X25519", true, ["deriveBits"]);
		const ac = new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: c.publicKey }, a.privateKey, 256));
		const distinct = [...ab].join() !== [...ac].join();
		// spki + pkcs8 round-trips still agree.
		const bPub = await crypto.subtle.importKey("spki", await crypto.subtle.exportKey("spki", b.publicKey), "X25519", true, []);
		const aPriv = await crypto.subtle.importKey("pkcs8", await crypto.subtle.exportKey("pkcs8", a.privateKey), "X25519", true, ["deriveBits"]);
		const okRt = [...ab].join() === [...new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: bPub }, aPriv, 256))].join();
		// jwk private round-trip.
		const aPrivJwk = await crypto.subtle.importKey("jwk", await crypto.subtle.exportKey("jwk", a.privateKey), "X25519", true, ["deriveBits"]);
		const okJwk = [...ab].join() === [...new Uint8Array(await crypto.subtle.deriveBits({ name: "X25519", public: b.publicKey }, aPrivJwk, 256))].join();
		// deriveKey yields a usable AES-GCM key.
		const key = await crypto.subtle.deriveKey({ name: "X25519", public: b.publicKey }, a.privateKey, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
		const enc = new TextEncoder();
		const iv = crypto.getRandomValues(new Uint8Array(12));
		const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, enc.encode("hi"));
		const pt = new TextDecoder().decode(await crypto.subtle.decrypt({ name: "AES-GCM", iv }, key, ct));
		const rawPub = new Uint8Array(await crypto.subtle.exportKey("raw", b.publicKey));
		return [agree, distinct, okRt, okJwk, pt, ab.length, rawPub.length, a.publicKey.usages.length].join("|");
	`), "true|true|true|true|hi|32|32|0")
}

func TestMiscGlobals(t *testing.T) {
	check(t, "structuredClone", eval(t, webapi.Options{}, `
		const o = { d: new Date(0), m: new Map([[1, { x: 1 }]]), u: new Uint8Array([1, 2]) };
		o.self = o;
		const c = structuredClone(o);
		let fn; try { structuredClone(() => 1); } catch (e) { fn = e.name; }
		return [c !== o, c.self === c, c.d.getTime(), c.m.get(1).x, c.u[1], fn].join("|");
	`), "true|true|0|1|2|DataCloneError")
	check(t, "polyfills", eval(t, webapi.Options{}, `
		const { promise, resolve } = Promise.withResolvers();
		resolve(1);
		const g = Object.groupBy([1, 2, 3], (x) => (x % 2 ? "odd" : "even"));
		const e = {}; Error.captureStackTrace(e);
		return [await promise, g.odd.length, typeof e.stack, new WeakRef(e).deref() === e, typeof Symbol.dispose, "a\uD800".isWellFormed()].join("|");
	`), "1|2|string|true|symbol|false")
	// The d flag (match indices), which Goja lacks: exec, matchAll (as
	// Vite's import scanner uses it), flags / hasIndices, named groups.
	check(t, "regexp indices", eval(t, webapi.Options{}, `
		const re = new RegExp("(?<!\\.)\\bimport\\s*\\((\"[^\"]*\")\\)", "dg");
		const code = 'a; import("x"); b.import("no"); import("yz")';
		const spans = [...code.matchAll(re)].map((m) => m.indices.map(([s, e]) => code.slice(s, e)).join("|"));
		const m = new RegExp("(?<word>b+)c", "d").exec("aabbbc"); // esbuild lowers d literals to this
		const plain = /b/g.exec("abc");
		return [spans.join(" ; "), re.flags, re.hasIndices, /x/.hasIndices, JSON.stringify(m.indices), JSON.stringify(m.indices.groups), "indices" in plain, re.lastIndex].join(" / ");
	`), `import("x")|"x" ; import("yz")|"yz" / dg / true / false / [[2,6],[2,5]] / {"word":[2,5]} / false / 0`)
	// V8's structured stack API (depd, callsites): CallSites from Goja frames.
	check(t, "prepareStackTrace", eval(t, webapi.Options{}, `
		function outer() { return inner(); }
		function inner() {
			const prev = Error.prepareStackTrace;
			Error.prepareStackTrace = (_obj, sites) => sites;
			const obj = {};
			Error.captureStackTrace(obj);
			const sites = obj.stack;
			Error.prepareStackTrace = prev;
			return sites;
		}
		const sites = outer();
		const s = sites[0];
		// Its methods are the prototype's, as V8's are (source-map-support's
		// cloneCallSite copies them from there).
		const names = Object.getOwnPropertyNames(Object.getPrototypeOf(s));
		const clone = {};
		for (const n of names) clone[n] = /^(?:is|get)/.test(n) ? function () { return s[n].call(s); } : s[n];
		return [Array.isArray(sites), s.getFunctionName(), typeof s.getFileName(), typeof s.getLineNumber(), typeof s.getColumnNumber(), s.isNative(), sites[1].getFunctionName(), String(s).startsWith("inner ("),
			s.constructor.name, names.includes("isNative"), Object.prototype.hasOwnProperty.call(s, "isNative"), clone.isNative(), typeof clone.getLineNumber()].join("|");
	`), "true|inner|string|number|number|false|outer|true|CallSite|true|false|false|number")
	check(t, "console", eval(t, webapi.Options{}, `
		console.log("a %s %d", "b", 42, { k: [1, "x"] }, new Map([["m", 1]]));
	`), "a b 42 { k: [ 1, 'x' ] } Map(1) { 'm' => 1 }\n")
	check(t, "timers", eval(t, webapi.Options{}, `
		const log = [];
		const t = setTimeout(() => log.push("never"), 1);
		clearTimeout(t);
		setTimeout((a, b) => log.push(a + b), 2, "x", "y");
		setImmediate(() => log.push("imm"));
		queueMicrotask(() => log.push("micro"));
		const id = setTimeout(() => {}, 1000).unref();
		await new Promise((r) => setTimeout(r, 10));
		return log.join(",") + "|" + typeof +id + "|" + id.hasRef();
	`), "micro,imm,xy|number|false")
	check(t, "env", eval(t, webapi.Options{Env: map[string]string{"NEXT_RUNTIME": "edge"}}, `return process.env.NEXT_RUNTIME + "|" + typeof performance.now();`), "edge|number")
	check(t, "als global", eval(t, webapi.Options{}, `
		const als = new AsyncLocalStorage();
		return await als.run("ctx", async () => { await new Promise((r) => setTimeout(r, 1)); return als.getStore(); });
	`), "ctx")
}

func TestUnicodeConsole(t *testing.T) {
	out := eval(t, webapi.Options{}, `console.error("ignored"); console.log("✓");`)
	if !strings.Contains(out, "✓") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestIntlSegmenter(t *testing.T) {
	check(t, "graphemes", eval(t, webapi.Options{}, `
		const s = new Intl.Segmenter("en", { granularity: "grapheme" });
		return [...s.segment("é👍🏽a")].map(x => x.segment + "@" + x.index).join(" ");
	`), "é@0 👍🏽@2 a@6")
	check(t, "words", eval(t, webapi.Options{}, `
		const s = new Intl.Segmenter("en", { granularity: "word" });
		return [...s.segment("Hi, 世界 42!")].map(x => JSON.stringify([x.segment, x.index, x.isWordLike])).join("");
	`), `["Hi",0,true][",",2,false][" ",3,false]["世",4,true]["界",5,true][" ",6,false]["42",7,true]["!",9,false]`)
	check(t, "containing and options", eval(t, webapi.Options{}, `
		const s = new Intl.Segmenter(["ja"], { granularity: "sentence" });
		const segs = s.segment("One. Two.");
		return [segs.containing(6).segment, segs.containing(99), s.resolvedOptions().locale, s.resolvedOptions().granularity, Object.prototype.toString.call(s)].join("|");
	`), "Two.||ja|sentence|[object Intl.Segmenter]")
}

// Intl.NumberFormat / DateTimeFormat / PluralRules (FormatJS, English data,
// IANA zones), loaded on first use.
func TestIntlNumberDate(t *testing.T) {
	check(t, "number, plural, date", eval(t, webapi.Options{}, `
		const out = [];
		out.push(new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(1234.5));
		out.push(new Intl.NumberFormat("en", { maximumFractionDigits: 1 }).format(3.14159));
		out.push(new Intl.PluralRules("en").select(1), new Intl.PluralRules("en").select(2));
		const d = new Date(Date.UTC(2026, 9, 7, 15, 4, 5));
		out.push(new Intl.DateTimeFormat("en-US", { timeZone: "UTC", dateStyle: "medium" }).format(d));
		out.push(new Intl.DateTimeFormat("en-US", { timeZone: "Asia/Tokyo", hour: "2-digit", minute: "2-digit", hour12: false }).format(d));
		out.push(new Intl.DateTimeFormat([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false, timeZone: "UTC" }).format(d));
		return out.join("|");
	`), "$1,234.50|3.1|one|other|Oct 7, 2026|00:04|15:04:05")
	// SvelteKit formats lists with en-GB (English data covers it).
	check(t, "list, relative time, display names", eval(t, webapi.Options{}, `
		return [
			new Intl.ListFormat("en-gb", { type: "conjunction" }).format(["a", "b", "c"]),
			new Intl.ListFormat("en", { type: "disjunction" }).format(["x", "y"]),
			new Intl.RelativeTimeFormat("en", { numeric: "auto" }).format(-1, "day"),
			new Intl.RelativeTimeFormat("en").format(3, "hour"),
			new Intl.DisplayNames("en", { type: "region" }).of("JP"),
		].join("|");
	`), "a, b and c|x or y|yesterday|in 3 hours|Japan")
	// Collation (CLDR, as ICU): case and accents after the base letter,
	// numeric option, sensitivity; localeCompare goes through it.
	check(t, "collator", eval(t, webapi.Options{}, `
		const c = new Intl.Collator("en-US");
		const n = new Intl.Collator("en", { numeric: true });
		const base = new Intl.Collator("en", { sensitivity: "base" });
		return [
			["b", "A", "a", "B", "á", "Z"].sort(c.compare).join(""),
			["item10", "item2", "item1"].sort(n.compare).join(","),
			base.compare("a", "Á"), c.compare("a", "Á"),
			"a".localeCompare("B"), "ä".localeCompare("z", "de"), "ä".localeCompare("z", "sv"),
			c.resolvedOptions().locale, c.resolvedOptions().sensitivity, n.resolvedOptions().numeric,
			Object.prototype.toString.call(c),
		].join("|");
	`), "aAábBZ|item1,item2,item10|0|-1|-1|-1|1|en-US|variant|true|[object Intl.Collator]")
	// Number / BigInt / Date locale methods format through Intl (Goja's
	// own took a locale for a radix).
	check(t, "toLocaleString", eval(t, webapi.Options{}, `
		return [
			(1234.5).toLocaleString("en", { maximumFractionDigits: 2 }), (1234.5).toLocaleString(), (12345n).toLocaleString("en"),
			new Date(0).toLocaleString("en-US", { timeZone: "UTC" }), new Date(0).toLocaleDateString("en-US", { timeZone: "UTC" }),
			new Date(0).toLocaleTimeString("en-US", { timeZone: "UTC" }),
		].join("|");
	`), "1,234.5|1,234.5|12,345|1/1/1970, 12:00:00 AM|1/1/1970|12:00:00 AM")
}

// Headers, URLSearchParams and Request copy a source through a brand
// check, not instanceof: srvx's Node request headers are a class whose
// prototype chain includes Headers.prototype but which has none of its
// private fields; Node (undici) reads such an object as the iterable it
// is. The expectations are Node 24's.
func TestInitFromLookalikes(t *testing.T) {
	got := eval(t, webapi.Options{}, `const out = [];
class FakeHeaders { constructor() { this.m = [["x-a", "1"], ["content-type", "text/plain"]]; } *[Symbol.iterator]() { yield* this.m; } }
Object.setPrototypeOf(FakeHeaders.prototype, Headers.prototype);
const fh = new FakeHeaders();
out.push(fh instanceof Headers, [...new Headers(fh)].join(";"));
const r = new Request("http://x/", { method: "POST", headers: fh, body: "b" });
out.push(r.headers.get("x-a"), await r.text());
class FakeParams { *[Symbol.iterator]() { yield ["q", "1"]; } }
Object.setPrototypeOf(FakeParams.prototype, URLSearchParams.prototype);
out.push(String(new URLSearchParams(new FakeParams())));
out.push([...new Headers(new Headers({ a: "1" }))].join(), String(new URLSearchParams(new URLSearchParams("a=1&b=2"))), new Request(new Request("http://y/", { method: "PUT" })).method);
return out.join("|");
`)
	check(t, "lookalikes", got, "true|content-type,text/plain;x-a,1|1|b|q=1|a,1|a=1&b=2|PUT")
}

// %TypedArray%.from takes a primitive source (its @@iterator is read with
// GetMethod, which boxes): Shiki decodes its inlined WebAssembly with
// Uint8Array.from(atob(...), c => c.charCodeAt(0)). The engine required an
// object. The expectations are Node 24's.
func TestTypedArrayFromPrimitive(t *testing.T) {
	got := eval(t, webapi.Options{}, `
const out = []; const t = (f) => { try { out.push(String(f())); } catch (e) { out.push(e.name); } };
t(() => Uint8Array.from("ab", (c) => c.charCodeAt(0)).join());
t(() => Uint8Array.from("ab").join());
t(() => Float64Array.from(5).length);
t(() => Uint8Array.from(null));
t(() => Uint8Array.from([1, 2], function (x) { return x * this.k; }, { k: 3 }).join());
t(() => Uint8Array.from.length + "," + Uint8Array.from.name);
t(() => Uint8Array.from("😀", (c) => c.length).join());
return out.join("|");`)
	check(t, "from", got, "97,98|0,0|0|TypeError|3,6|1,from|2")
}

// TestMessaging: MessageChannel / MessagePort / MessageEvent /
// BroadcastChannel, Node's globals (undici reads MessagePort when it
// loads): structured-cloned data delivered on a later task, onmessage /
// Node's on / addEventListener starting a port, BroadcastChannel by name
// excluding the sender. The expectation is Node 24's.
func TestMessaging(t *testing.T) {
	got := eval(t, webapi.Options{}, `const out = [];
const { port1, port2 } = new MessageChannel();
const obj = { a: [1, 2], d: new Date(0) };
port2.onmessage = (e) => { out.push("onmessage:" + JSON.stringify(e.data) + ":" + (e.data !== obj) + ":" + (e instanceof MessageEvent) + ":" + e.type); };
port1.postMessage(obj);
out.push("sync-after-post");
const c2 = new MessageChannel();
c2.port2.on("message", (data) => out.push("on:" + data));
c2.port1.postMessage("x");
const c3 = new MessageChannel();
c3.port2.addEventListener("message", (e) => out.push("listener:" + e.data));
c3.port1.postMessage("queued");
const b1 = new BroadcastChannel("ch"), b2 = new BroadcastChannel("ch"), b3 = new BroadcastChannel("other");
b2.onmessage = (e) => out.push("bc:" + e.data);
b3.onmessage = (e) => out.push("bc-other:" + e.data);
b1.onmessage = (e) => out.push("bc-self:" + e.data);
b1.postMessage("hello");
const me = new MessageEvent("message", { data: 7 });
out.push("event:" + me.data + ":" + me.origin + ":" + me.ports.length + ":" + (me.source === null));
let err = "";
try { port1.postMessage(() => 1); } catch (e) { err = e.name; }
out.push("clone-error:" + err);
setTimeout(() => {
  out.push("after-timeout");
  c3.port2.start();
  setTimeout(() => {
    b1.close(); b2.close(); b3.close(); port1.close(); c2.port1.close(); c3.port1.close();
    globalThis.result = out.join("|");
  }, 10);
}, 10);

await new Promise((r) => { const w = () => (globalThis.result ? r() : setTimeout(w, 5)); w(); });
return globalThis.result;`)
	check(t, "messaging", got, `sync-after-post|event:7::0:true|clone-error:DataCloneError|onmessage:{"a":[1,2],"d":"1970-01-01T00:00:00.000Z"}:true:true:message|on:x|listener:queued|bc:hello|after-timeout`)
}

// TestProxyInstanceof: instanceof with a Proxy of a function on its right,
// which the engine rejects on its own: a plain one, msw's Proxy of
// globalThis.Headers, a handler's own Symbol.hasInstance, a revocable one
// (revoked: TypeError), and Proxy itself (needs new, no prototype). The
// expected output is Node 24's.
func TestProxyInstanceof(t *testing.T) {
	got := eval(t, webapi.Options{}, `const out = [];
class A {}
const P = new Proxy(A, {});
out.push(new A() instanceof P, {} instanceof P);
const H = globalThis.Headers;
globalThis.Headers = new Proxy(H, { construct(t, args, nt) { return Reflect.construct(t, args, nt); } });
out.push(new Headers({ a: "1" }) instanceof Headers, new H() instanceof Headers, typeof Headers);
const custom = new Proxy(function F() {}, { get(t, k, r) { return k === Symbol.hasInstance ? () => true : Reflect.get(t, k, r); } });
out.push(1 instanceof custom);
const { proxy, revoke } = Proxy.revocable(A, {});
out.push(new A() instanceof proxy);
revoke();
try { new A() instanceof proxy; out.push("no throw"); } catch (e) { out.push(e.constructor.name); }
try { Proxy(A, {}); out.push("no throw"); } catch (e) { out.push(e.constructor.name); }
out.push(Proxy.name, Proxy.length, "prototype" in Proxy, typeof new Proxy({}, {}));
globalThis.Headers = H;
return JSON.stringify(out);
`)
	if want := `[true,false,true,true,"function",true,true,"TypeError","TypeError","Proxy",2,false,"object"]`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestTimerIdleStart: a Timeout's _idleStart is when it was armed, and
// moving it moves the timer's due time, as in Node (Next's
// createAtomicTimerGroup relies on it). Node 24 gives the same output.
func TestTimerIdleStart(t *testing.T) {
	got := eval(t, webapi.Options{}, `
const order = [];
const a = setTimeout(() => order.push("a"), 20);
const b = setTimeout(() => order.push("b"), 5);
b._idleStart = a._idleStart + 30;
await new Promise((r) => setTimeout(r, 120));
return JSON.stringify([typeof a._idleStart, order]);
`)
	if want := `["number",["a","b"]]`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
