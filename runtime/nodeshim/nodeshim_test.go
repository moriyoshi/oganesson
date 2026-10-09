package nodeshim_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// run bundles src (which must assign globalThis.result, possibly a promise)
// against the nodeshim plugin and evaluates it in a fresh engine.
func run(t *testing.T, opts nodeshim.Options, src string) string {
	t.Helper()
	return runWith(t, opts, src, nil)
}

// runWith is run with extra host setup (e.g. nodeshim.InstallZlib).
func runWith(t *testing.T, opts nodeshim.Options, src string, setup func(*engine.Engine) error) string {
	t.Helper()
	return runEngine(t, opts, engine.Options{}, src, setup)
}

// runEngine is runWith with the engine's options.
func runEngine(t *testing.T, opts nodeshim.Options, eopts engine.Options, src string, setup func(*engine.Engine) error) string {
	t.Helper()
	bo := api.BuildOptions{
		Stdin:   &api.StdinOptions{Contents: "import " + jsonQuote(nodeshim.GlobalsModule) + ";\n" + src, Loader: api.LoaderJS, ResolveDir: opts.ResolveDir},
		Bundle:  true,
		Write:   false,
		Plugins: []api.Plugin{nodeshim.Plugin(opts)},
		Define:  map[string]string{"__dirname": `"/"`},
	}
	gojatarget.Apply(&bo)
	bo.Platform = api.PlatformBrowser
	res := api.Build(bo)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors[0].Text)
	}
	e := engine.New(eopts)
	defer e.Close()
	if _, err := webapi.Install(e, webapi.Options{Stdout: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		if err := setup(e); err != nil {
			t.Fatal(err)
		}
	}
	// An infinite loop in the shim fails the test instead of hanging it.
	timer := time.AfterFunc(10*time.Second, func() { e.VM.Interrupt("timeout") })
	defer timer.Stop()
	// Finish's engine workarounds, as every og bundle gets them.
	if _, err := e.VM.RunString(gojatarget.Finish(string(res.OutputFiles[0].Contents))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := e.Await(ctx, e.VM.Get("result"))
	if err != nil {
		t.Fatal(err)
	}
	return v.String()
}

// nextOptions are the overrides the Next build uses (nextNodeOverrides).
func nextOptions(dir string) nodeshim.Options {
	return nodeshim.Options{ResolveDir: dir, Overrides: map[string]string{
		"buffer":         "next/dist/compiled/buffer",
		"stream":         "next/dist/compiled/stream-browserify",
		"string_decoder": "next/dist/compiled/string_decoder",
	}}
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Node's EventEmitter is a callable function whose on/off are addListener
// and removeListener; pre-class npm code depends on both.
func TestEventEmitterNodeCompat(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const EventEmitter = require("events");
const out = [];
out.push(EventEmitter.EventEmitter === EventEmitter, typeof EventEmitter.once === "function");
out.push(EventEmitter.prototype.on === EventEmitter.prototype.addListener, EventEmitter.prototype.off === EventEmitter.prototype.removeListener);
// Pre-class subclassing (util.inherits style), with readable-stream's alias.
function Legacy() { EventEmitter.call(this); this.legacy = true; }
Legacy.prototype = Object.create(EventEmitter.prototype, { constructor: { value: Legacy } });
Legacy.prototype.on = function (ev, fn) { return EventEmitter.prototype.on.call(this, ev, fn); };
Legacy.prototype.addListener = Legacy.prototype.on;
const l = new Legacy();
let seen = 0;
l.on("x", (v) => (seen += v));
l.addListener("x", (v) => (seen += v));
l.emit("x", 2);
out.push(l instanceof EventEmitter, l.legacy, seen, l.listenerCount("x"));
// Class subclassing and plain construction.
class Modern extends EventEmitter {}
const m = new Modern();
m.once("y", () => out.push("once"));
m.emit("y"); m.emit("y");
out.push(m instanceof Modern, new EventEmitter() instanceof EventEmitter);
globalThis.result = out.join(",");
`)
	if want := "true,true,true,true,true,true,4,2,once,true,true"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// readable-stream (Next's compiled stream-browserify, which the Next build
// uses as node:stream) runs on the shimmed events.
func TestReadableStream(t *testing.T) {
	dir, _ := filepath.Abs("../../testdata/apps/next-pages")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-pages)")
	}
	opts := nextOptions(dir)
	got := run(t, opts, `
const { Readable, Writable } = require("stream");
const log = [];
const chunks = ["a", "b", null];
const r = new Readable({ read() { this.push(chunks.shift()); } });
const w = new Writable({ write(c, _e, cb) { log.push("w:" + c); cb(); } });
globalThis.result = new Promise((resolve) => {
  r.on("data", (c) => log.push("d:" + c));
  w.on("finish", () => resolve(log.join(",")));
  r.pipe(w);
});
`)
	if want := "d:a,w:a,d:b,w:b"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// raw-body (what Next's API body parser reads requests with) loads depd,
// which reads V8 CallSites through Error.prepareStackTrace.
func TestRawBody(t *testing.T) {
	dir, _ := filepath.Abs("../../testdata/apps/next-pages")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-pages)")
	}
	opts := nextOptions(dir)
	got := run(t, opts, `
const { Readable } = require("stream");
const getRawBody = require("next/dist/compiled/raw-body");
const chunks = [Buffer.from("hi "), Buffer.from("there"), null];
const r = new Readable({ read() { this.push(chunks.shift()); } });
globalThis.result = getRawBody(r, { encoding: "utf-8", limit: "1mb" }).then((s) => "ok " + s, (e) => "err " + e.stack);
`)
	if got != "ok hi there" {
		t.Fatal(got)
	}
}

// util.debuglog logs only for the sections NODE_DEBUG names.
func TestDebuglog(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const util = require("util");
const before = process.env.NODE_DEBUG;
process.env.NODE_DEBUG = "http, NET*";
const out = [util.debuglog("stream").enabled, util.debuglog("http").enabled, util.debuglog("net_x").enabled, util.debuglog("https").enabled];
process.env.NODE_DEBUG = before;
globalThis.result = out.join(",");
`)
	if want := "false,true,true,false"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// createCipheriv / createDecipheriv against NIST SP 800-38A/D vectors, the
// GCM auth tag check, and Next's encryptWithSecret round trip shape.
func TestCipheriv(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const crypto = require("crypto");
const hex = (s) => Buffer.from(s, "hex");
const out = [];
// GCM test case 2: zero key and IV, one zero block.
let c = crypto.createCipheriv("aes-128-gcm", Buffer.alloc(16), Buffer.alloc(12));
out.push(Buffer.concat([c.update(Buffer.alloc(16)), c.final()]).toString("hex"), c.getAuthTag().toString("hex"));
// CBC F.2.1 (first block; auto padding adds a second) and CTR F.5.1.
const key = hex("2b7e151628aed2a6abf7158809cf4f3c"), pt = hex("6bc1bee22e409f96e93d7e117393172a");
c = crypto.createCipheriv("aes-128-cbc", key, hex("000102030405060708090a0b0c0d0e0f"));
const cbc = Buffer.concat([c.update(pt), c.final()]);
out.push(cbc.subarray(0, 16).toString("hex"), cbc.length);
let d = crypto.createDecipheriv("aes-128-cbc", key, hex("000102030405060708090a0b0c0d0e0f"));
out.push(d.update(cbc, undefined, "hex") + d.final("hex"));
c = crypto.createCipheriv("aes-128-ctr", key, hex("f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff"));
out.push(c.update(pt, undefined, "hex") + c.final("hex"));
// aes-256-gcm with a 16-byte IV (Next's crypto-utils), round trip and tamper.
const k = crypto.randomBytes(32), iv = crypto.randomBytes(16);
c = crypto.createCipheriv("aes-256-gcm", k, iv);
const enc = Buffer.concat([c.update("secret data", "utf8"), c.final()]), tag = c.getAuthTag();
d = crypto.createDecipheriv("aes-256-gcm", k, iv);
d.setAuthTag(tag);
out.push(d.update(enc) + d.final("utf8"));
d = crypto.createDecipheriv("aes-256-gcm", k, iv);
d.setAuthTag(Buffer.alloc(16));
d.update(enc);
try { d.final(); out.push("tamper accepted"); } catch (e) { out.push(e.message); }
try { crypto.createCipheriv("aes-256-gcm", Buffer.alloc(16), iv); } catch (e) { out.push(e.message); }
globalThis.result = out.join("|");
`)
	want := "0388dace60b6a392f328c2b971b2fe78|ab6e47d42cec13bdf53a67b21257bddf|7649abac8119b246cee98e9b12e9197d|32|6bc1bee22e409f96e93d7e117393172a|874d6191b620e3261bef6864990db6ce|secret data|Unsupported state or unable to authenticate data|Invalid key length"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// Secret KeyObjects, as jsonwebtoken (Next's preview data) uses them: HS256
// over a KeyObject equals HMAC over the raw secret.
func TestSecretKeyObject(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const crypto = require("crypto");
const k = crypto.createSecretKey(Buffer.from("s3cret"));
let threw = false;
try { crypto.createPrivateKey("s3cret"); } catch { threw = true; }
globalThis.result = [k instanceof crypto.KeyObject, k.type, k.symmetricKeySize, threw,
  crypto.createHmac("sha256", k).update("x").digest("hex") === crypto.createHmac("sha256", "s3cret").update("x").digest("hex"),
  k.export().toString()].join(",");
`)
	if want := "true,secret,6,true,true,s3cret"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// node:fs/promises in every import form. unenv's fs imports
// node:fs/promises itself; resolving that to Oganesson's override (built on
// Oganesson's fs, built on unenv's fs) was a cycle that left the module
// empty for whichever side loaded first.
func TestFsPromisesImports(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
import fsp from "node:fs/promises";
import * as ns from "node:fs/promises";
import { appendFile } from "node:fs/promises";
import fs from "node:fs";
const fp = require("fs/promises");
globalThis.result = [typeof fsp.appendFile, typeof ns.appendFile, typeof appendFile, typeof fs.promises.appendFile, typeof fp.readFile].join(",");
`)
	if want := "function,function,function,function,function"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// util.styleText: ANSI codes only for a colorizable stream (Node's
// shouldColorize), and Node's format validation.
func TestStyleText(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const { styleText, inherits } = require("util");
const out = [JSON.stringify(styleText("red", "x")), JSON.stringify(styleText(["bold", "green"], "y", { validateStream: false }))];
process.env.FORCE_COLOR = "1";
out.push(JSON.stringify(styleText("dim", "z")));
delete process.env.FORCE_COLOR;
try { styleText("nope", "w"); } catch (e) { out.push(e.code); }
out.push(typeof inherits);
globalThis.result = out.join(" ");
`)
	if want := `"x" "\u001b[1m\u001b[32my\u001b[39m\u001b[22m" "\u001b[2mz\u001b[22m" ERR_INVALID_ARG_VALUE function`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// Built-ins have no default property, as in Node: interop helpers would
// take it for the module (mlly reads fs as "default" in m ? m.default : m,
// and unenv's default is its stubs).
func TestBuiltinsHaveNoDefault(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const mods = {
  fs: require("node:fs"), http: require("node:http"), util: require("node:util"), crypto: require("node:crypto"),
  zlib: require("node:zlib"), events: require("node:events"), module: require("node:module"),
  async_hooks: require("node:async_hooks"), worker_threads: require("node:worker_threads"), assert: require("node:assert"),
};
globalThis.result = Object.keys(mods).filter((n) => "default" in mods[n]).join(",") || "none";
`)
	if got != "none" {
		t.Fatalf("built-ins with a default: %s", got)
	}
}

// process.stdin is a non-TTY stream with events (Vite adds and removes an
// "end" listener on it).
func TestProcessStdin(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const f = () => {};
process.stdin.on("end", f);
const n = process.stdin.listenerCount("end");
process.stdin.off("end", f);
globalThis.result = [process.stdin.isTTY, process.stdin.fd, n, process.stdin.listenerCount("end"), process.stdin === process.stdin].join(",");
`)
	if want := "false,0,1,0,true"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// process.hrtime: [seconds, nanoseconds], differences against a previous
// reading, and hrtime.bigint().
func TestProcessHrtime(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const a = process.hrtime();
const d = process.hrtime(a);
const b = process.hrtime.bigint();
globalThis.result = [Array.isArray(a) && a.length === 2, d[0] >= 0 && d[1] >= 0 && d[1] < 1e9, typeof b, process.hrtime.bigint() >= b].join(",");
`)
	if want := "true,true,bigint,true"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// util.stripVTControlCharacters (Node 16.11+) removes ANSI escapes with
// Node's own pattern: SGR, cursor movement and OSC 8 hyperlinks.
func TestStripVTControlCharacters(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const { stripVTControlCharacters } = require("node:util");
globalThis.result = JSON.stringify([
  stripVTControlCharacters("\u001b[1m\u001b[32mok\u001b[39m\u001b[22m"),
  stripVTControlCharacters("a\u001b[2Kb\u001b[1Gc"),
  stripVTControlCharacters("\u001b]8;;https://x.test\u0007link\u001b]8;;\u0007"),
]);
`)
	if want := `["ok","abc","link"]`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// Logical assignment with an awaited right-hand side: Goja loses the
// assignment to a local, so internal/gojatarget has esbuild lower it (Vite's
// resolveConfig runs value ??= (await determineAgent()).isAgent).
func TestLogicalAssignmentWithAwait(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const P = () => Promise.resolve({ v: true });
async function a(x) { x ??= (await P()).v; return x; }
async function b(x) { x ||= (await P()).v; return x; }
async function c(x) { x &&= (await P()).v; return x; }
const o = { k: undefined };
async function d() { o.k ??= (await P()).v; return o.k; }
globalThis.result = Promise.all([a(undefined), a(false), b(0), c(1), c(0), d()]).then((v) => JSON.stringify(v));
`)
	if want := "[true,false,true,true,0,true]"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// base64url (Node 15+) in unenv's Buffer, via load-time rewrites.
func TestBufferBase64URL(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const { Buffer } = require("buffer");
const b = Buffer.from([0xfb, 0xff, 0xfe, 0x01]);
const s = b.toString("base64url");
const back = Buffer.from(s, "base64url");
const w = Buffer.alloc(4); w.write("-_-_", "base64url");
globalThis.result = [s, b.toString("base64"), back.equals(b), Buffer.isEncoding("base64url"), Buffer.byteLength(s, "base64url"), w.toString("hex")].join(" ");
`)
	if want := "-__-AQ +//+AQ== true true 4 fbffbf00"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// util.parseEnv (dotenv grammar): quotes, escapes, comments, export.
func TestParseEnv(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const { parseEnv } = require("util");
const env = parseEnv("A=1\n# comment\nexport B = two words # trailing\nC=\"x\\ny\"\nD='single'\nE=\nF=a=b\r\n");
globalThis.result = JSON.stringify(env);
`)
	if want := `{"A":"1","B":"two words","C":"x\ny","D":"single","E":"","F":"a=b"}`; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// node:stream is Node's module (readable-stream): the legacy Stream
// constructor that pre-class code inherits from (send, Express), with
// working Readable / Writable / Transform / pipeline.
func TestStreamIsNodes(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const Stream = require("stream");
const util = require("util");
const out = [typeof Stream, Stream.Stream === Stream, typeof Stream.Readable, typeof Stream.PassThrough];
function Legacy() { Stream.call(this); }
util.inherits(Legacy, Stream);
out.push(new Legacy() instanceof Stream, typeof new Legacy().pipe);
const chunks = [];
const upper = new Stream.Transform({ transform(c, _e, cb) { cb(null, String(c).toUpperCase()); } });
const sink = new Stream.Writable({ write(c, _e, cb) { chunks.push(String(c)); cb(); } });
globalThis.result = new Promise((resolve, reject) => {
  Stream.pipeline(Stream.Readable.from(["a", "b"]), upper, sink, (err) => {
    if (err) return reject(err);
    out.push(chunks.join(""));
    require("stream/promises").finished(Stream.Readable.from([]).resume()).then(() => resolve(out.join(",")), reject);
  });
});
`)
	if want := "function,true,function,function,true,function,AB"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// http.createServer(listener).listen() in a request's VM: serve hands a
// web Request to the listener as IncomingMessage / ServerResponse, and the
// response comes back as a web Response.
func TestHTTPServerServe(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const http = require("http");
const seen = [];
const server = http.createServer((req, res) => {
  let body = "";
  req.setEncoding("utf8");
  req.on("data", (c) => (body += c));
  req.on("end", () => {
    seen.push(req.method, req.url, req.headers["x-in"], body, req.socket.remoteAddress, typeof req.socket.destroy);
    res.statusCode = 201;
    res.setHeader("X-Out", "1");
    res.setHeader("Set-Cookie", ["a=1", "b=2"]);
    res.write("echo:");
    res.end(body);
  });
});
let listened = false;
server.listen(3000, () => (listened = true));
globalThis.result = (async () => {
  const r = await http.serve(new Request("http://localhost/p?q=1", { method: "POST", headers: { "x-in": "v" }, body: "abc" }), Promise.resolve());
  return [listened, ...seen, r.status, r.headers.get("x-out"), r.headers.get("set-cookie"), await r.text()].join("|");
})();
`)
	if want := "true|POST|/p?q=1|v|abc|127.0.0.1|function|201|1|a=1, b=2|echo:abc"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// A program that ends without listening fails the request (no hang).
func TestHTTPServeWithoutListen(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const http = require("http");
globalThis.result = http.serve(new Request("http://localhost/"), Promise.resolve()).then(() => "served", (e) => e.message);
`)
	if want := "og: the server program ended without calling listen()"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// zlib's stream factories (compression middleware) over Go's codecs,
// brotli included (andybalholm/brotli), which @fastify/compress assumes.
func TestZlibStreams(t *testing.T) {
	got := runWith(t, nodeshim.Options{}, `
const zlib = require("zlib");
const parts = [];
const gz = zlib.createGzip();
gz.on("data", (c) => parts.push(c));
globalThis.result = new Promise((resolve, reject) => {
  gz.on("error", reject);
  gz.on("end", () => {
    const all = Buffer.concat(parts);
    const br = zlib.brotliCompressSync("brotli text", { params: { [zlib.constants.BROTLI_PARAM_QUALITY]: 4 } });
    resolve([all[0], all[1], zlib.gunzipSync(all).toString(), zlib.brotliDecompressSync(br).toString()].join(","));
  });
  gz.write("hello ");
  gz.end("gzip");
});
`, func(e *engine.Engine) error { return nodeshim.InstallZlib(e) })
	if want := "31,139,hello gzip,brotli text"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// Node built-ins and language pieces servers rely on: require("assert") is
// callable (Fastify's router), process.versions.node is set (Hono's node
// adapter), and Annex B's __defineGetter__ family exists (Koa, via
// delegates).
func TestNodeServerBasics(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const assert = require("assert");
const strict = require("assert/strict");
const out = [typeof assert, typeof assert.strictEqual, typeof strict];
try { assert(false, "boom"); } catch (e) { out.push(e.message); }
out.push(typeof process.versions.node, process.versions.node === process.version.slice(1));
const o = {};
o.__defineGetter__("x", function () { return 42; });
o.__defineSetter__("y", function (v) { this.seen = v; });
o.y = 7;
out.push(o.x, o.seen, typeof Object.prototype.__lookupGetter__.call(o, "x"), Object.keys(Object.prototype).length);
globalThis.result = out.join(",");
`)
	if want := "function,function,function,boom,string,true,42,7,function,0"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// zlib streams stream: flush() emits what was written so far, decodable
// before the stream ends (compression's res.flush()), and unzip detects
// gzip and zlib input.
func TestZlibStreamsIncremental(t *testing.T) {
	got := runWith(t, nodeshim.Options{}, `
const zlib = require("zlib");
globalThis.result = (async () => {
  const out = [];
  const gz = zlib.createGzip();
  const chunks = [];
  gz.on("data", (c) => chunks.push(c));
  gz.write("first part ");
  await new Promise((r) => gz.flush(r));
  await new Promise((r) => setTimeout(r, 0));
  // The sync-flushed prefix decodes, before the stream ends, through a
  // streaming gunzip.
  const prefix = Buffer.concat(chunks);
  const gunzip = zlib.createGunzip();
  const early = [];
  gunzip.on("data", (c) => early.push(c));
  gunzip.write(prefix);
  await new Promise((r) => setTimeout(r, 0));
  out.push(prefix.length > 10, Buffer.concat(early).toString());
  gunzip.destroy();
  const done = new Promise((r) => gz.on("end", r));
  gz.end("second part");
  await done;
  out.push(zlib.gunzipSync(Buffer.concat(chunks)).toString());
  for (const data of [zlib.gzipSync("via gzip"), zlib.deflateSync("via zlib")]) {
    const u = zlib.createUnzip();
    const parts = [];
    u.on("data", (c) => parts.push(c));
    const end = new Promise((r, j) => { u.on("end", r); u.on("error", j); });
    u.end(data);
    await end;
    out.push(Buffer.concat(parts).toString());
  }
  return out.join("|");
})();
`, func(e *engine.Engine) error { return nodeshim.InstallZlib(e) })
	if want := "true|first part |first part second part|via gzip|via zlib"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// node:path is Node's lib/path.js: posix keeps backslashes as ordinary
// characters (Rollup's createFilter escapes "-" in the project path to
// "\\-"; pathe turned that into a separator, so no SolidStart file matched
// and "use server" functions shipped to the browser), win32 is real, and
// errors carry Node's messages. The expectations are Node 24's output.
func TestPathIsNodes(t *testing.T) {
	got := run(t, nodeshim.Options{}, `const path = require("path");
const { posix, win32 } = path;
const out = [];
const t = (f) => { try { out.push(JSON.stringify(f())); } catch (e) { out.push(e.code + ": " + e.message); } };
t(() => posix.join("/a\\-b", "src/**/*.ts"));
t(() => [win32.sep, win32.delimiter, posix.sep, posix.delimiter]);
t(() => win32.join("C:\\a", "..", "b/c"));
t(() => win32.resolve("C:\\x", "y", "..\\z"));
t(() => win32.normalize("C:/a//b\\..\\c"));
t(() => win32.parse("C:\\dir\\file.txt"));
t(() => win32.relative("C:\\a\\b", "C:\\a\\c\\d"));
t(() => win32.isAbsolute("\\\\server\\share"));
t(() => win32.toNamespacedPath("C:\\a"));
t(() => posix.normalize("a//b/../c/./d/"));
t(() => posix.relative("/a/b/c", "/a/d"));
t(() => posix.parse("/x/y.tar.gz"));
t(() => posix.format({ dir: "/x", name: "y", ext: "gz" }));
t(() => [posix.basename("/a/b.js", ".js"), posix.basename("a\\b"), posix.extname(".bashrc"), posix.dirname("/a/b/")]);
t(() => [path === posix, require("path/posix") === posix, require("path/win32") === win32, path.win32.posix === posix]);
t(() => posix.resolve("/r", "x/../y", "./z"));
t(() => posix.join(1));
t(() => posix.join(null));
t(() => posix.basename({}));
t(() => posix.format(null));
t(() => posix.join("a", "averyveryverylongstringofcharacters", Symbol.iterator && 5n));
globalThis.result = out.join("\n");
`)
	want := `"/a\\-b/src/**/*.ts"
["\\",";","/",":"]
"C:\\b\\c"
"C:\\x\\z"
"C:\\a\\c"
{"root":"C:\\","dir":"C:\\dir","base":"file.txt","ext":".txt","name":"file"}
"..\\c\\d"
true
"\\\\?\\C:\\a"
"a/c/d/"
"../../d"
{"root":"/","dir":"/x","base":"y.tar.gz","ext":".gz","name":"y.tar"}
"/x/y.gz"
["b","a\\b","","/a"]
[true,true,true,true]
"/r/y/z"
ERR_INVALID_ARG_TYPE: The "path" argument must be of type string. Received type number (1)
ERR_INVALID_ARG_TYPE: The "path" argument must be of type string. Received null
ERR_INVALID_ARG_TYPE: The "path" argument must be of type string. Received an instance of Object
ERR_INVALID_ARG_TYPE: The "pathObject" argument must be of type object. Received null
ERR_INVALID_ARG_TYPE: The "path" argument must be of type string. Received type bigint (5n)`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// Readable.toWeb / fromWeb, Writable.toWeb / fromWeb and Duplex.toWeb are
// Node's own adapters (readable-stream leaves them out; srvx reads every
// request body through Readable.toWeb). The expectations are Node 24's.
func TestWebStreamAdapters(t *testing.T) {
	got := run(t, nodeshim.Options{}, `const { Readable, Writable, Duplex } = require("stream");
globalThis.result = (async () => {
  const out = [];
  // Readable.toWeb: a request body as srvx reads it.
  const web = Readable.toWeb(Readable.from([Buffer.from("he"), Buffer.from("llo")]));
  out.push(web instanceof ReadableStream, await new Response(web).text());
  // Readable.fromWeb, in object mode and with an encoding.
  const chunks = [];
  for await (const c of Readable.fromWeb(new Response("abc").body, { encoding: "utf8" })) chunks.push(c);
  out.push(chunks.join(""));
  const objs = [];
  for await (const o of Readable.fromWeb(new ReadableStream({ start(c) { c.enqueue({ a: 1 }); c.close(); } }), { objectMode: true })) objs.push(JSON.stringify(o));
  out.push(objs.join());
  // Writable.toWeb and fromWeb.
  const got = [];
  const ws = Writable.toWeb(new Writable({ write(c, e, cb) { got.push(String(c)); cb(); } }));
  const w = ws.getWriter();
  await w.write(new TextEncoder().encode("x"));
  await w.write("y");
  await w.close();
  out.push(got.join(""));
  const sink = [];
  const nodeW = Writable.fromWeb(new WritableStream({ write(c) { sink.push(new TextDecoder().decode(c)); } }));
  await new Promise((res) => nodeW.end("zz", res));
  out.push(sink.join(""));
  // Duplex.toWeb.
  const d = new Duplex({ read() { this.push("r"); this.push(null); }, write(c, e, cb) { cb(); } });
  const pair = Duplex.toWeb(d);
  out.push(typeof pair.readable.getReader, typeof pair.writable.getWriter, new TextDecoder().decode((await pair.readable.getReader().read()).value));
  // Errors are Node's.
  try { Readable.toWeb({}); } catch (e) { out.push(e.code + ": " + e.message); }
  try { Readable.fromWeb({}); } catch (e) { out.push(e.code + ": " + e.message); }
  try { Readable.toWeb(Readable.from([]), { type: "nope" }); } catch (e) { out.push(e.code + ": " + e.message); }
  return out.join("\n");
})();
`)
	want := `true
hello
abc
{"a":1}
xy
zz
function
function
r
ERR_INVALID_ARG_TYPE: The "streamReadable" argument must be an stream.Readable. Received an instance of Object
ERR_INVALID_ARG_TYPE: The "readableStream" argument must be an instance of ReadableStream. Received an instance of Object
ERR_INVALID_ARG_VALUE: The property 'options.type' must be one of: 'bytes', undefined. Received 'nope'`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

// A request is a readable stream until its body is read, as in Node: srvx
// (Nitro 3, SolidStart) reads every POST body through Readable.toWeb(req),
// which yields a cancelled, empty stream for a stream that is not
// readable. unenv's IncomingMessage shadowed the Readable getters with
// fields (readable = false). The expectations are Node 24's.
func TestIncomingMessageReadable(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const http = require("http");
const { Readable } = require("stream");
const seen = [];
const server = http.createServer(async (req, res) => {
  seen.push(req.readable, req.closed, req.errored, req.complete, req.destroyed);
  const text = await new Response(Readable.toWeb(req)).text();
  seen.push(text, req.readable, req.complete, req.readableEnded);
  res.end("ok");
});
server.listen(3000);
globalThis.result = (async () => {
  const r = await http.serve(new Request("http://localhost/", { method: "POST", body: "abc" }), Promise.resolve());
  await r.text();
  return seen.join("|");
})();
`)
	if want := "true|false||false|false|abc|false|true|true"; got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// serve signals og.startupDone (the end of the program's startup output,
// runtime/app) after listening and before dispatching the request.
func TestHTTPServeSignalsStartupDone(t *testing.T) {
	got := run(t, nodeshim.Options{}, `
const http = require("http");
const order = [];
Object.defineProperty(globalThis, Symbol.for("og.startupDone"), { value: () => order.push("startupDone") });
const server = http.createServer((req, res) => { order.push("request"); res.end("ok"); });
server.listen(3000, () => order.push("listening"));
globalThis.result = http.serve(new Request("http://localhost/"), Promise.resolve()).then((r) => r.text()).then(() => order.join(","));
`)
	if want := "listening,startupDone,request"; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// path.matchesGlob is Node's: internal/fs/glob's matchGlobPattern over
// Node's bundled minimatch (vendor/minimatch), with Node's options (no
// negation or comments, win32 separators either way). The expectations are
// Node 24's output, but for the last case: Node ignores case on macOS and
// Windows, and og reports Linux, where "A.JS" does not match "*.js".
func TestPathMatchesGlob(t *testing.T) {
	got := run(t, nodeshim.Options{}, `const path = require("path");
const cases = [
  ["posix", "a/b/c.js", "a/**/*.js"], ["posix", "a/b/c.js", "a/*.js"], ["posix", "a/.x/c.js", "a/**/c.js"],
  ["posix", ".hidden", "*"], ["posix", "src/x.ts", "src/*.{ts,tsx}"], ["posix", "src/x.md", "src/*.{ts,tsx}"],
  ["posix", "a/b", "a/?"], ["posix", "a/bc", "a/?"], ["posix", "x1.js", "x[0-9].js"], ["posix", "!a", "!a"],
  ["posix", "#c", "#c"], ["posix", "/abs/p.js", "/abs/**"], ["posix", "a\\b", "a\\b"],
  ["win32", "a\\b\\c.js", "a/**/*.js"], ["win32", "a\\b\\c.js", "a\\**\\*.js"], ["win32", "C:\\x\\y.txt", "C:\\x\\*.txt"],
  ["posix", "A.JS", "*.js"],
];
const out = [cases.map(([p, a, g]) => path[p].matchesGlob(a, g)).join(",")];
try { path.matchesGlob(1, "x"); } catch (e) { out.push(e.code); }
globalThis.result = out.join(" ");
`)
	if want := "true,false,false,false,true,false,true,false,true,true,true,true,false,true,true,true,false ERR_INVALID_ARG_TYPE"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestNodeSurfaceForCLIs: what Prisma's CLI needed of the Node surface,
// against Node 24's output for the same script: process as an
// EventEmitter, its ids, execPath and argv0, process.binding("constants" /
// "buffer"), Module._nodeModulePaths, vm.runInThisContext, util.types
// predicates, and http's header validators.
func TestNodeSurfaceForCLIs(t *testing.T) {
	got := run(t, nodeshim.Options{}, nodeSurfaceScenario)
	want := `process emitter: [true,1,true,0,["first","1+2","3"],false]
process ids: ["number","number","string","string",true]
binding constants: ["number","object",["crypto","fs","internal","os","trace","zlib"]]
binding buffer: "number"
binding other: Error No such module: og_nope
nodeModulePaths: ["/a/b/node_modules/c/node_modules","/a/b/node_modules","/a/node_modules","/node_modules"]
nodeModulePaths root: ["/node_modules"]
vm: [42,"number"]
util.types: [true,false,true,true,true,true,false,true,true,true,true,true,true,true,true,true,true]
header name ok: undefined
header name bad: ERR_INVALID_HTTP_TOKEN Header name must be a valid HTTP token ["x og"]
header value undefined: ERR_HTTP_INVALID_HEADER_VALUE Invalid value "undefined" for header "x-og"
header value bad: ERR_INVALID_CHAR Invalid character in header content ["x-og"]
header value ok: undefined`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

const nodeSurfaceScenario = `const out = [];
const t = (label, f) => { try { out.push(label + ": " + JSON.stringify(f())); } catch (e) { out.push(label + ": " + (e.code || e.name) + " " + e.message); } };
const events = [];
const h = (...a) => events.push(a.join("+"));
t("process emitter", () => {
  process.addListener("og-test", h);
  process.prependOnceListener("og-test", () => events.push("first"));
  const r = [process.emit("og-test", 1, 2), process.listenerCount("og-test"), process.emit("og-test", 3)];
  process.removeListener("og-test", h);
  return [...r, process.listenerCount("og-test"), events, process.emit("og-test")];
});
t("process ids", () => [typeof process.pid, typeof process.ppid, typeof process.execPath, typeof process.argv0, process.pid > 0]);
t("binding constants", () => { const c = process.binding("constants"); return [typeof c.fs.O_CREAT, typeof c.os.signals, Object.keys(c).sort()]; });
t("binding buffer", () => typeof process.binding("buffer").kStringMaxLength);
t("binding other", () => process.binding("og_nope"));
t("nodeModulePaths", () => require("module")._nodeModulePaths("/a/b/node_modules/c"));
t("nodeModulePaths root", () => require("module")._nodeModulePaths("/"));
t("vm", () => { const vm = require("vm"); globalThis.ogVm = 41; return [vm.runInThisContext("ogVm + 1"), new vm.Script("typeof ogVm").runInThisContext()]; });
t("util.types", () => {
  const u = require("util").types;
  return [u.isBoxedPrimitive(Object(1)), u.isBoxedPrimitive(1), u.isNumberObject(Object(1)), u.isStringObject(Object("s")), u.isBigIntObject(Object(1n)),
    u.isTypedArray(new Uint8Array(1)), u.isUint8Array(new Uint16Array(1)), u.isFloat64Array(new Float64Array(1)), u.isAnyArrayBuffer(new ArrayBuffer(1)),
    u.isArrayBufferView(new DataView(new ArrayBuffer(1))), u.isAsyncFunction(async () => {}), u.isGeneratorFunction(function* () {}),
    u.isGeneratorObject((function* () {})()), u.isMapIterator(new Map().keys()), u.isSetIterator(new Set().values()), u.isNativeError(new TypeError()),
    (function () { return u.isArgumentsObject(arguments); })()];
});
const http = require("http");
t("header name ok", () => http.validateHeaderName("x-og"));
t("header name bad", () => http.validateHeaderName("x og"));
t("header value undefined", () => http.validateHeaderValue("x-og", undefined));
t("header value bad", () => http.validateHeaderValue("x-og", "a\nb"));
t("header value ok", () => http.validateHeaderValue("x-og", "fine"));
globalThis.result = out.join("\n");
`

// TestDiagnosticsChannel: node:diagnostics_channel is Node's own
// (lib/diagnostics_channel.js), not unenv's stub (whose TracingChannel
// throws, which ioredis hits): channels, tracing channels over sync,
// promise and callback functions, stores bound to AsyncLocalStorage, and
// a throwing subscriber reported as uncaught. The expected output is
// Node 24.15's.
func TestDiagnosticsChannel(t *testing.T) {
	got := run(t, nodeshim.Options{}, `const dc = require("diagnostics_channel");
const { AsyncLocalStorage } = require("async_hooks");
globalThis.result = (async () => {
  const out = [];
  const ch = dc.channel("og:test");
  out.push("has " + ch.hasSubscribers + " " + dc.hasSubscribers("og:test"));
  const sub = (msg, name) => out.push("msg " + name + " " + JSON.stringify(msg));
  dc.subscribe("og:test", sub);
  out.push("has " + ch.hasSubscribers + " " + (dc.channel("og:test") === ch));
  ch.publish({ a: 1 });
  dc.unsubscribe("og:test", sub);
  ch.publish({ a: 2 });
  out.push("has " + ch.hasSubscribers);

  const tc = dc.tracingChannel("og:cmd");
  out.push("tc has " + tc.hasSubscribers);
  const events = (prefix) => ({
    start: (m) => out.push(prefix + " start " + m.cmd),
    end: (m) => out.push(prefix + " end " + m.cmd + " " + ("result" in m ? m.result : "-")),
    asyncStart: (m) => out.push(prefix + " asyncStart " + m.cmd + " " + ("result" in m ? m.result : "-") + " " + (m.error ? m.error.message : "-")),
    asyncEnd: (m) => out.push(prefix + " asyncEnd " + m.cmd),
    error: (m) => out.push(prefix + " error " + m.cmd + " " + m.error.message),
  });
  const h = events("t");
  tc.subscribe(h);
  out.push("tc has " + tc.hasSubscribers);
  out.push("sync " + tc.traceSync((x) => x * 2, { cmd: "double" }, null, 21));
  try {
    tc.traceSync(() => { throw new Error("sync boom"); }, { cmd: "boom" });
  } catch (e) {
    out.push("caught " + e.message);
  }
  out.push("promise " + (await tc.tracePromise(async (x) => x + 1, { cmd: "inc" }, null, 1)));
  try {
    await tc.tracePromise(async () => { throw new Error("async boom"); }, { cmd: "fail" });
  } catch (e) {
    out.push("rejected " + e.message);
  }
  await new Promise((resolve) => tc.traceCallback((cb) => setTimeout(() => cb(null, "cbval"), 1), 0, { cmd: "cb" }, null, (err, v) => { out.push("callback " + v); resolve(); }));
  tc.unsubscribe(h);
  out.push("tc has " + tc.hasSubscribers);

  // bindStore: the store holds the transformed message while the trace runs.
  const als = new AsyncLocalStorage();
  const start = dc.channel("og:store");
  start.bindStore(als, (m) => "store:" + m.id);
  out.push("runStores " + start.runStores({ id: 7 }, () => als.getStore()));
  out.push("after " + als.getStore());

  // A subscriber that throws does not stop publish; the error is uncaught.
  const errs = [];
  process.once("uncaughtException", (e) => errs.push(e.message));
  dc.subscribe("og:throw", () => { throw new Error("subscriber boom"); });
  dc.subscribe("og:throw", () => out.push("second subscriber ran"));
  dc.channel("og:throw").publish({});
  await new Promise((r) => setTimeout(r, 10));
  out.push("uncaught " + errs.join());
  return out.join("\n");
})();
`)
	want := `has false false
has true true
msg og:test {"a":1}
has false
tc has false
tc has true
t start double
t end double 42
sync 42
t start boom
t error boom sync boom
t end boom -
caught sync boom
t start inc
t end inc -
t asyncStart inc 2 -
t asyncEnd inc
promise 2
t start fail
t end fail -
t error fail async boom
t asyncStart fail - async boom
t asyncEnd fail
rejected async boom
t start cb
t end cb -
t asyncStart cb cbval -
callback cbval
t asyncEnd cb
tc has false
runStores store:7
after undefined
second subscriber ran
uncaught subscriber boom`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestVMContexts: vm.runInNewContext / runInContext / createContext run
// code with the sandbox's properties in scope, returning its completion
// value (the eval package's use, through vanilla-extract). The expected
// output is Node 24's.
func TestVMContexts(t *testing.T) {
	got := run(t, nodeshim.Options{}, `const vm = require("vm");
const out = [];
const sb = { module: { exports: {} }, x: 2 };
out.push(vm.runInNewContext("module.exports.v = x * 21; module.exports.v + 1", sb), sb.module.exports.v);
const ctx = vm.createContext({ n: 1 });
out.push(vm.isContext(ctx), vm.isContext({}), vm.runInContext("n += 1; n", ctx), ctx.n);
out.push(new vm.Script("typeof JSON + \":\" + y").runInNewContext({ y: "why" }));
globalThis.result = out.join(",");`)
	if want := "43,42,true,false,2,2,object:why"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestUtilCallbackify: util.callbackify (and _extend, toUSVString,
// aborted), which unenv leaves unimplemented, as Node's (@svgr/webpack's
// loader callbackifies its transform). The expected output is Node 24's.
func TestUtilCallbackify(t *testing.T) {
	got := run(t, nodeshim.Options{}, `const util = require("util");
globalThis.result = new Promise((resolve) => {
  const out = [];
  const f = util.callbackify(async function add(a, b) { return a + b; });
  const g = util.callbackify(async () => { throw null; });
  f(1, 2, (e, v) => {
    out.push(e, v, f.name, f.length);
    g((e) => {
      out.push(e.code, e.reason);
      out.push(JSON.stringify(util._extend({ a: 1 }, { b: 2 })), util.toUSVString("a\ud800b"));
      const ac = new AbortController();
      util.aborted(ac.signal, {}).then(() => { out.push("aborted"); resolve(out.join(",")); });
      ac.abort();
    });
  });
});`)
	if want := ",3,addCallbackified,3,ERR_FALSY_VALUE_REJECTION,,{\"a\":1,\"b\":2},a�b,aborted"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestPrepareStackTrace: with Error.prepareStackTrace set, a new error's
// stack is what it returns for V8 CallSites (callsites, parent-module:
// Lingui's and Compiled's Babel configs reach them); the wrapped Error keeps
// instanceof, subclasses, constructor and calls without new. The expected
// output is Node 24's.
func TestPrepareStackTrace(t *testing.T) {
	got := run(t, nodeshim.Options{}, `const out = [];
const orig = Error.prepareStackTrace;
Error.prepareStackTrace = (_, s) => s;
function where() {
  const st = new Error().stack;
  return [Array.isArray(st), typeof st[0].getFileName(), st[0].getFunctionName()];
}
out.push(...where());
Error.prepareStackTrace = orig;
class MyErr extends Error { constructor(m) { super(m); this.name = "MyErr"; } }
const me = new MyErr("m");
out.push(typeof new Error("x").stack, new TypeError("z") instanceof Error, new Error("a").constructor === Error, me instanceof MyErr, me instanceof Error, me.message, Error("noNew") instanceof Error, Object.prototype.toString.call(new Error("t")));
Error.prepareStackTrace = (e, s) => "custom:" + e.message + ":" + (s.length > 0);
out.push(new Error("q").stack);
Error.prepareStackTrace = () => { throw new TypeError("formatter failed"); };
let created;
try { created = new Error("t") instanceof Error; } catch { created = false; }
out.push(created);
Error.prepareStackTrace = undefined;
globalThis.result = out.join(",");
`)
	if want := "true,string,where,string,true,true,true,true,m,true,[object Error],custom:q:true,true"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestProcessRelease: process.release is Node's for the reported version
// (Compiled's isNodeEnvironment reads release.name).
func TestProcessRelease(t *testing.T) {
	got := run(t, nodeshim.Options{}, `const r = process.release;
globalThis.result = [r.name, r.sourceUrl === "https://nodejs.org/download/release/" + process.version + "/node-" + process.version + ".tar.gz", typeof r.headersUrl].join(",");`)
	if want := "node,true,string"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestTLSSocketOverStream: a TLSSocket over a stream that is not a
// net.Socket can be made, exposing Node's JSStreamSocket through
// _handle._parentWrap (http2-wrapper reads it when it loads, through
// Panda CSS's CLI); connecting over one is refused. The first two values
// are Node 24's.
func TestTLSSocketOverStream(t *testing.T) {
	got := runWith(t, nodeshim.Options{}, `const tls = require("tls"), stream = require("stream"), net = require("net");
const C = (new tls.TLSSocket(new stream.PassThrough()))._handle._parentWrap.constructor;
let refused;
try { tls.connect({ socket: new stream.PassThrough() }); refused = false; } catch (e) { refused = /needs a net.Socket/.test(e.message); }
globalThis.result = [typeof C, C.prototype instanceof net.Socket, refused].join(",");`, func(e *engine.Engine) error {
		return nodeshim.InstallNet(e, nodeshim.NewSockets())
	})
	if want := "function,true,true"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestProcessUncaught: uncaught exceptions and unhandled rejections reach
// process's "uncaughtException" / "unhandledRejection" listeners, with
// Node's origins (a rejection without an unhandledRejection listener is an
// uncaughtException of origin "unhandledRejection"). The expected output
// is Node 24's.
func TestProcessUncaught(t *testing.T) {
	got := runEngine(t, nodeshim.Options{}, engine.Options{UnhandledRejections: true}, `const out = [];
globalThis.result = new Promise((resolve) => {
  const onEx = (e, origin) => out.push("uncaughtException:" + e.message + ":" + origin);
  process.on("uncaughtException", onEx);
  setTimeout(() => { throw new Error("from timer"); }, 0);
  setTimeout(() => {
    Promise.reject(new Error("rejected first"));
    setTimeout(() => {
      const onRej = (r, p) => out.push("unhandledRejection:" + r.message + ":" + (p instanceof Promise));
      process.on("unhandledRejection", onRej);
      Promise.reject(new Error("rejected second"));
      setTimeout(() => {
        process.off("unhandledRejection", onRej);
        process.off("uncaughtException", onEx);
        resolve(out.join(","));
      }, 5);
    }, 5);
  }, 5);
});
`, nil)
	if want := "uncaughtException:from timer:uncaughtException,uncaughtException:rejected first:unhandledRejection,unhandledRejection:rejected second:true"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

// TestHTTPCommon: node:_http_common's HTTPParser (http-parser-js) parses
// as Node's http_parser binding does for what msw's interceptors read: a
// request (head, body, end) and a response head, with Node's callback slot
// numbers (kOnExecute aside: a slot key whose value is the library's). The
// expected output is Node 24's for the same script.
func TestHTTPCommon(t *testing.T) {
	got := run(t, nodeshim.Options{}, `const { HTTPParser, methods, CRLF } = require("_http_common");
const out = [];
const req = new HTTPParser();
req.initialize(HTTPParser.REQUEST, {});
req[HTTPParser.kOnHeadersComplete] = (vMaj, vMin, headers, method, url, status, statusMessage, upgrade, keepAlive) => {
  out.push(["req-head", vMaj, vMin, methods[method], url, headers.join("|"), keepAlive]);
};
req[HTTPParser.kOnBody] = (b, start, len) => out.push(["req-body", (start === undefined ? b : b.subarray(start, start + len)).toString()]);
req[HTTPParser.kOnMessageComplete] = () => out.push(["req-end"]);
req.execute(Buffer.from("POST /api/user?id=1 HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\nContent-Length: 13\r\n\r\n{\"name\":\"og\"}"));
const res = new HTTPParser();
res.initialize(HTTPParser.RESPONSE, {});
res[HTTPParser.kOnHeadersComplete] = (vMaj, vMin, headers, method, url, status, statusMessage) => out.push(["res-head", status, statusMessage, headers.join("|")]);
res.execute(Buffer.from("HTTP/1.1 201 Created\r\nContent-Length: 5\r\n\r\nhello"));
out.push(["consts", HTTPParser.kOnMessageBegin, HTTPParser.kOnHeaders, HTTPParser.kOnTimeout, JSON.stringify(CRLF)]);
globalThis.result = JSON.stringify(out);
`)
	if want := `[["req-head",1,1,"POST","/api/user?id=1","Host|example.com|Content-Type|application/json|Content-Length|13",true],["req-body","{\"name\":\"og\"}"],["req-end"],["res-head",201,"Created","Content-Length|5"],["consts",0,1,6,"\"\\r\\n\""]]`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestTLSRootCertificates: node:tls exposes the embedded Mozilla roots (via
// net.go / runtime/tlsroots), where unenv's base leaves rootCertificates empty.
func TestTLSRootCertificates(t *testing.T) {
	got := runWith(t, nodeshim.Options{}, `
const tls = require("tls");
const roots = tls.rootCertificates;
globalThis.result = [
  Array.isArray(roots),
  roots.length > 100,
  roots[0].startsWith("-----BEGIN CERTIFICATE-----"),
  roots.every((p) => p.trimEnd().endsWith("-----END CERTIFICATE-----")),
].join(",");
`, func(e *engine.Engine) error { return nodeshim.InstallNet(e, nodeshim.NewSockets()) })
	want := "true,true,true,true"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
