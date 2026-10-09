package jshost

import (
	"context"
	"github.com/evanw/esbuild/pkg/api"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWorkerThreads: node:worker_threads at build time: a Worker runs its
// file in its own engine with workerData; messages (structured data) cross
// both ways; terminate() ends it with an exit event.
func TestWorkerThreads(t *testing.T) {
	dir := t.TempDir()
	worker := `const { parentPort, workerData, isMainThread, threadId } = require("node:worker_threads");
parentPort.on("message", (m) => {
  parentPort.postMessage({ echo: m, data: workerData, isMainThread, threadId: typeof threadId });
});
`
	if err := os.WriteFile(filepath.Join(dir, "worker.cjs"), []byte(worker), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import { Worker, isMainThread } from "node:worker_threads";
globalThis.__ogResult = (async () => {
  const w = new Worker(`+jsonString(filepath.Join(dir, "worker.cjs"))+`, { workerData: { n: 7 } });
  const events = [];
  w.on("online", () => events.push("online"));
  const reply = await new Promise((resolve, reject) => {
    w.once("message", resolve);
    w.once("error", reject);
    const shared = { s: 1 };
    const cyclic = { name: "c", list: [shared, shared] };
    cyclic.self = cyclic;
    w.postMessage({ bytes: new Uint8Array([1, 2, 3]), map: new Map([["k", 1]]), big: 10n, nothing: undefined, cyclic });
  });
  const code = await w.terminate();
  // structured clone keeps cycles and shared references
  const c = reply.echo.cyclic;
  const identity = c.self === c && c.list[0] === c.list[1] && c.list[0].s === 1;
  return [isMainThread, events.join(), Array.from(reply.echo.bytes).join("."), reply.echo.bytes instanceof Uint8Array, reply.echo.map.get("k"), String(reply.echo.big), "nothing" in reply.echo, reply.data.n, reply.isMainThread, reply.threadId, code, identity].join(",");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "true,online,1.2.3,true,1,10,true,7,false,number,0,true"; v != want {
		t.Fatalf("got %v, want %s", v, want)
	}
}

// TestWorkerEnvAndExit: a Worker's env option is its process.env, and
// process.exit() in the worker ends it with that code (SvelteKit's forked
// tasks post a result, then exit; the parent waits for the exit event).
func TestWorkerEnvAndExit(t *testing.T) {
	dir := t.TempDir()
	worker := `const { parentPort } = require("node:worker_threads");
parentPort.postMessage({ fork: process.env.FORK, inherited: process.env.OG_TEST_INHERITED ?? null });
setImmediate(() => process.exit(3));
`
	if err := os.WriteFile(filepath.Join(dir, "worker.cjs"), []byte(worker), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir, Env: map[string]string{"OG_TEST_INHERITED": "yes"}}
	code, err := h.Bundle("", `
import { Worker } from "node:worker_threads";
globalThis.__ogResult = (async () => {
  const run = (options) => new Promise((resolve, reject) => {
    const w = new Worker(`+jsonString(filepath.Join(dir, "worker.cjs"))+`, options);
    let msg;
    w.on("message", (m) => (msg = m));
    w.on("error", reject);
    w.on("exit", (code) => resolve([msg.fork, msg.inherited, code].join("/")));
  });
  return [await run({ env: { ...process.env, FORK: 1 } }), await run({ env: { FORK: "only" } }), await run({})].join(",");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "1/yes/3,only//3,/yes/3"; v != want {
		t.Fatalf("got %v, want %s", v, want)
	}
}

// TestWorkerUnhandledRejection: a promise rejected without a handler in a
// worker is its error event, and the worker exits, as in Node (SvelteKit's
// forked tasks fail this way).
func TestWorkerUnhandledRejection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker.mjs"), []byte(`const { parentPort } = await import("node:worker_threads");
parentPort.on("message", async () => { throw new Error("failed in worker"); });
parentPort.postMessage("ready");
`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import { Worker } from "node:worker_threads";
globalThis.__ogResult = new Promise((resolve) => {
  const w = new Worker(`+jsonString(filepath.Join(dir, "worker.mjs"))+`);
  const events = [];
  w.on("message", (m) => { events.push(m); w.postMessage("go"); });
  w.on("error", (e) => events.push("error: " + e.message.split("\n")[0]));
  w.on("exit", (code) => resolve([...events, "exit " + code].join(", ")));
});
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ready, error: Error: failed in worker, exit 1"; v != want {
		t.Fatalf("got %v, want %s", v, want)
	}
}

// TestTopLevelAwait: with TopLevelAwait, a bundle may contain modules that
// await at their top level (SvelteKit's optional imports); EvalWith waits
// for the whole evaluation, failures included.
func TestTopLevelAwait(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.mjs"), []byte(`export let ts;
try { ts = (await import("not-installed-typescript")).default; } catch { ts = "absent"; }
export const late = await new Promise((r) => setTimeout(() => r("late"), 5));
`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir, TopLevelAwait: true}
	code, err := h.Bundle("", `import { ts, late } from "./lib.mjs"; globalThis.__ogResult = ts + "," + late;`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if v != "absent,late" {
		t.Fatalf("got %v", v)
	}
	code, err = h.Bundle("", `await null; throw new Error("after await");`, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Eval(context.Background(), code, "1"); err == nil || !strings.Contains(err.Error(), "after await") {
		t.Fatalf("err %v", err)
	}
}

// TestTopLevelAwaitExternals: in a TopLevelAwait bundle, imports of
// modules left external (a package the plugins leave to run time) become
// require() calls, with default and namespace interop as the IIFE format.
func TestTopLevelAwaitExternals(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"node_modules/ext-cjs/index.js":     `module.exports = { named: "n", fn() { return "cjs"; } };`,
		"node_modules/ext-cjs/package.json": `{"name":"ext-cjs"}`,
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &Host{Dir: dir, TopLevelAwait: true, Plugins: []api.Plugin{{
		Name: "external",
		Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: `^ext-cjs$`}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "ext-cjs", External: true}, nil
			})
		},
	}}}
	code, err := h.Bundle("", `import dflt, { named } from "ext-cjs"; import * as ns from "ext-cjs";
await null;
globalThis.__ogResult = [dflt.fn(), named, ns.default.named, ns.named].join(",");`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if v != "cjs,n,n,n" {
		t.Fatalf("got %v", v)
	}
}

// TestUnrefWorkerReply: awaiting a message from an unreferenced worker
// that replies later settles, as in Node (Qwik City's static generation
// unrefs its workers and awaits their close replies); the engine declared
// the promise unsettleable instead.
func TestUnrefWorkerReply(t *testing.T) {
	dir := t.TempDir()
	worker := `const { parentPort } = require("node:worker_threads");
parentPort.on("message", (m) => setTimeout(() => { parentPort.postMessage("reply:" + m); parentPort.close(); }, 50));
`
	if err := os.WriteFile(filepath.Join(dir, "worker.cjs"), []byte(worker), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import { Worker } from "node:worker_threads";
globalThis.__ogResult = (async () => {
  const w = new Worker(`+jsonString(filepath.Join(dir, "worker.cjs"))+`);
  w.unref();
  w.postMessage("close");
  return await new Promise((r) => w.once("message", r));
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "reply:close"; v != want {
		t.Fatalf("got %v, want %s", v, want)
	}
}

// TestWorkerLateListener: messages posted before the worker adds its
// parentPort "message" listener wait for it, in order, as with Node's
// MessagePort (Qwik City's workers listen after an await); og dropped them.
// Node 24 gives "late:early1,late:early2".
func TestWorkerLateListener(t *testing.T) {
	dir := t.TempDir()
	worker := `const { parentPort } = require("node:worker_threads");
let n = 0;
setTimeout(() => parentPort.on("message", (m) => { parentPort.postMessage("late:" + m); if (++n === 2) parentPort.close(); }), 100);
`
	if err := os.WriteFile(filepath.Join(dir, "worker.cjs"), []byte(worker), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import { Worker } from "node:worker_threads";
globalThis.__ogResult = (async () => {
  const w = new Worker(`+jsonString(filepath.Join(dir, "worker.cjs"))+`);
  w.postMessage("early1");
  w.postMessage("early2");
  const got = [];
  await new Promise((r) => w.on("message", (m) => { got.push(m); if (got.length === 2) r(); }));
  return got.join(",");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "late:early1,late:early2"; v != want {
		t.Fatalf("got %v, want %s", v, want)
	}
}

// TestWorkerExitsWhenIdle: a worker ends (code 0) when its loop is idle and
// its parentPort has no "message" listener, as in Node; og kept it running
// until terminated, so the parent's loop never drained. terminate() after
// the exit resolves. Node 24 gives "done,0,undefined".
func TestWorkerExitsWhenIdle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "worker.cjs"), []byte(`require("node:worker_threads").parentPort.postMessage("done");`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import { Worker } from "node:worker_threads";
globalThis.__ogResult = (async () => {
  const w = new Worker(`+jsonString(filepath.Join(dir, "worker.cjs"))+`);
  const got = await new Promise((r) => w.on("message", r));
  const code = await new Promise((r) => w.on("exit", r));
  const t = await w.terminate();
  return [got, code, String(t)].join(",");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	v, err := h.Eval(ctx, code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "done,0,undefined"; v != want {
		t.Fatalf("got %v, want %s", v, want)
	}
}

// TestWorkerSideEffectFreePackage: a worker file in a package that
// declares "sideEffects": false still runs (Node ignores the field for a
// worker's entry; esbuild dropped the bare import).
func TestWorkerSideEffectFreePackage(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "node_modules", "pure")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"pure","sideEffects":false}`), 0o644)
	_ = os.WriteFile(filepath.Join(pkg, "worker.cjs"), []byte(`require("node:worker_threads").parentPort.postMessage("ran");`), 0o644)
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import { Worker } from "node:worker_threads";
globalThis.__ogResult = new Promise((r) => new Worker(`+jsonString(filepath.Join(pkg, "worker.cjs"))+`).on("message", r));
`, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	v, err := h.Eval(ctx, code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if v != "ran" {
		t.Fatalf("got %v, want ran", v)
	}
}

// TestRunProgram: a package's CLI run as `node file args...`: a hashbang
// line (a comment to Node), process.argv, and both ends of a run: the loop
// going idle (process.exitCode, after a timer) and process.exit(code).
func TestRunProgram(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	idle := write("idle.js", "#!/usr/bin/env node\nconst fs = require('fs');\nsetTimeout(() => { fs.writeFileSync(process.argv[2], process.argv.slice(3).join(',')); process.exitCode = 2; }, 20);\n")
	exit := write("exit.js", "#!/usr/bin/env node\nprocess.exit(Number(process.argv[2]));\nrequire('fs').writeFileSync('never', 'x');\n")
	h := &Host{Dir: dir}
	code, err := h.RunProgram(context.Background(), idle, []string{filepath.Join(dir, "out.txt"), "a", "b"})
	if err != nil || code != 2 {
		t.Fatalf("idle: %d %v", code, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out.txt")); string(b) != "a,b" {
		t.Errorf("idle wrote %q", b)
	}
	code, err = h.RunProgram(context.Background(), exit, []string{"3"})
	if err != nil || code != 3 {
		t.Fatalf("exit: %d %v", code, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "never")); err == nil {
		t.Error("code after process.exit ran")
	}
}
