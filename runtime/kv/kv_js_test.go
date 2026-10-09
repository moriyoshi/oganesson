package kv_test

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/kv"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// run bundles src (which must assign globalThis.result) and evaluates it in
// a fresh engine attached to store, like one request.
func run(t *testing.T, store *kv.Store, src string) string {
	t.Helper()
	opts := api.BuildOptions{
		Stdin:   &api.StdinOptions{Contents: src, Loader: api.LoaderJS},
		Bundle:  true,
		Write:   false,
		Plugins: []api.Plugin{kv.Plugin()},
	}
	gojatarget.Apply(&opts)
	res := api.Build(opts)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors[0].Text)
	}
	e := engine.New(engine.Options{})
	defer e.Close()
	if _, err := webapi.Install(e, webapi.Options{Stdout: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if err := kv.Install(e, store); err != nil {
		t.Fatal(err)
	}
	if _, err := e.VM.RunString(string(res.OutputFiles[0].Contents)); err != nil {
		t.Fatal(err)
	}
	v, err := e.Await(context.Background(), e.VM.Get("result"))
	if err != nil {
		t.Fatal(err)
	}
	return v.String()
}

func TestSharedAcrossEngines(t *testing.T) {
	store := kv.NewMemory()
	run(t, store, `
		import { openKv } from "og:kv";
		globalThis.result = (async () => {
			const kv = await openKv();
			const obj = { name: "ann", tags: new Set(["a"]), at: new Date(0), big: 10n, bytes: new Uint8Array([1, 2]), m: new Map([[1, "x"]]), u: undefined, n: NaN };
			obj.self = obj;
			await kv.set(["users", "ann"], obj);
			await kv.set(["users", "bob"], "B");
			await kv.set(["users", 7], 7);
			await kv.set(["tmp"], "gone soon", { expireIn: 30 });
			return "ok";
		})();
	`)
	got := run(t, store, `
		import { openKv } from "og:kv";
		globalThis.result = (async () => {
			const kv = await openKv();
			const { value: u, versionstamp } = await kv.get(["users", "ann"]);
			const checks = [u.name, u.tags.has("a"), u.at.getTime(), typeof u.big, u.bytes[1], u.m.get(1), "u" in u, Number.isNaN(u.n), u.self === u, versionstamp.length];
			const keys = [];
			for await (const e of kv.list({ prefix: ["users"] })) keys.push(String(e.key[1]));
			const rev = [];
			for await (const e of kv.list({ prefix: ["users"] }, { reverse: true, limit: 2 })) rev.push(String(e.key[1]));
			const missing = await kv.get(["nope"]);
			return [...checks, keys.join(","), rev.join(","), missing.value, missing.versionstamp].join("|");
		})();
	`)
	// Deno key order: string parts before number parts.
	want := "ann|true|0|bigint|2|x|true|true|true|20|ann,bob,7|7,bob|null|null"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	time.Sleep(50 * time.Millisecond)
	if got := run(t, store, `
		import { openKv } from "og:kv";
		globalThis.result = openKv().then((kv) => kv.get(["tmp"])).then((e) => String(e.value));
	`); got != "null" {
		t.Errorf("expired value = %s", got)
	}
}

func TestAtomicFromJS(t *testing.T) {
	store := kv.NewMemory()
	got := run(t, store, `
		import { openKv, KvU64 } from "og:kv";
		globalThis.result = (async () => {
			const kv = await openKv();
			const first = await kv.atomic().check({ key: ["lock"], versionstamp: null }).set(["lock"], "mine").commit();
			const second = await kv.atomic().check({ key: ["lock"], versionstamp: null }).set(["lock"], "theirs").commit();
			const { versionstamp } = await kv.get(["lock"]);
			const third = await kv.atomic().check({ key: ["lock"], versionstamp }).delete(["lock"]).commit();
			await kv.atomic().sum(["hits"], new KvU64(2n)).sum(["hits"], 3n).commit();
			const hits = (await kv.get(["hits"])).value;
			let typeErr = "none";
			try { await kv.atomic().sum(["lock2"], 1n).set(["s"], "x").sum(["s"], 1n).commit(); } catch (e) { typeErr = e.constructor.name; }
			return [first.ok, second.ok, third.ok, hits instanceof KvU64, hits.value, typeErr].join("|");
		})();
	`)
	if want := "true|false|true|true|5|TypeError"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	// Many concurrent "requests" incrementing one counter lose nothing.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run(t, store, `
				import { openKv } from "og:kv";
				globalThis.result = openKv().then((kv) => kv.atomic().sum(["counter"], 1n).commit()).then((r) => String(r.ok));
			`)
		}()
	}
	wg.Wait()
	if got := run(t, store, `
		import { openKv } from "og:kv";
		globalThis.result = openKv().then((kv) => kv.get(["counter"])).then((e) => e.value.toString());
	`); got != "20" {
		t.Errorf("counter = %s", got)
	}
}

func TestCursorPagination(t *testing.T) {
	store := kv.NewMemory()
	got := run(t, store, `
		import { openKv } from "og:kv";
		globalThis.result = (async () => {
			const kv = await openKv();
			for (let i = 0; i < 5; i++) await kv.set(["page", i], i);
			const it = kv.list({ prefix: ["page"] }, { limit: 2 });
			const first = [];
			for await (const e of it) first.push(e.value);
			const rest = [];
			for await (const e of kv.list({ prefix: ["page"] }, { cursor: it.cursor })) rest.push(e.value);
			return first.join(",") + "|" + rest.join(",");
		})();
	`)
	if want := "0,1|2,3,4"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
