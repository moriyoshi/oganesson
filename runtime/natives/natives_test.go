package natives_test

import (
	"bytes"
	"context"
	"database/sql"
	"github.com/moriyoshi/oganesson/runtime/vfs"
	"image"
	"image/color"
	"image/png"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/natives"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// run bundles src (which must assign globalThis.result) against the
// natives plugin and evaluates it in a fresh engine, like one request.
func run(t *testing.T, svc *natives.Services, globals map[string]any, src string) string {
	t.Helper()
	opts := api.BuildOptions{
		Stdin:   &api.StdinOptions{Contents: src, Loader: api.LoaderJS},
		Bundle:  true,
		Write:   false,
		Plugins: []api.Plugin{natives.Plugin()},
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
	if err := natives.Install(e, svc); err != nil {
		t.Fatal(err)
	}
	for k, v := range globals {
		if b, ok := v.([]byte); ok {
			v = e.VM.NewArrayBuffer(b)
		}
		_ = e.VM.Set(k, v)
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

// halves is a 40x20 PNG: left half red, right half blue.
func halves(t *testing.T) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.NRGBA{255, 0, 0, 255}
			if x >= 20 {
				c = color.NRGBA{0, 0, 255, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSharp(t *testing.T) {
	svc := natives.NewServices()
	got := run(t, svc, map[string]any{"input": halves(t)}, `
		import sharp from "sharp";
		const dims = async (s) => { const { info } = await s.toBuffer({ resolveWithObject: true }); return info.width + "x" + info.height; };
		globalThis.result = (async () => {
			const m = await sharp(input).metadata();
			const out = [m.format, m.width, m.height, m.hasAlpha, m.channels];
			out.push(await dims(sharp(input).resize(10)));                               // keeps aspect
			out.push(await dims(sharp(input).resize(10, 10)));                           // cover: exact
			out.push(await dims(sharp(input).resize(10, 10, { fit: "inside" })));        // 10x5
			out.push(await dims(sharp(input).resize(10, 10, { fit: "contain" })));        // letterboxed 10x10
			out.push(await dims(sharp(input).resize(80, null, { withoutEnlargement: true }))); // unchanged
			out.push(await dims(sharp(input).rotate(90)));
			out.push(await dims(sharp(input).extract({ left: 5, top: 5, width: 10, height: 4 })));
			const g = await sharp(input).grayscale().png().toBuffer({ resolveWithObject: true });
			out.push(g.info.channels, g.info.format);
			const j = await sharp(input).jpeg({ quality: 50 }).toBuffer({ resolveWithObject: true });
			out.push(j.info.format, j.data[0] === 0xff && j.data[1] === 0xd8);
			// flop mirrors: the first pixel turns blue.
			const flopped = await sharp(input).flop().png().toBuffer();
			const fm = await sharp(flopped).metadata();
			out.push(fm.width);
			try { await sharp(input).avif().toBuffer(); out.push("no error"); } catch (e) { out.push(e.message.includes("not supported by Oganesson")); }
			try { await sharp(input).toFormat("tiff").toBuffer(); out.push("no error"); } catch (e) { out.push(e.message.includes("not supported")); }
			const viaCb = await new Promise((res) => sharp(input).resize(4).toBuffer((err, data, info) => res(err ? err.message : info.width)));
			out.push(viaCb);
			return out.join(",");
		})();
	`)
	if want := "png,40,20,true,4,10x5,10x10,10x5,10x10,40x20,20x40,10x4,1,png,jpeg,true,40,true,true,4"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// metadata() keys come in sharp's order (src/metadata.cc), autoOrient's
// too; they were a Go map's, so JSON of the metadata varied run to run.
// Repeated, as a random order can match once.
func TestSharpMetadataKeyOrder(t *testing.T) {
	svc := natives.NewServices()
	got := run(t, svc, map[string]any{"input": halves(t)}, `
		import sharp from "sharp";
		globalThis.result = (async () => {
			const seen = new Set();
			for (let i = 0; i < 20; i++) {
				const m = await sharp(input).metadata();
				seen.add(Object.keys(m).join(",") + " " + JSON.stringify(m.autoOrient));
			}
			return [...seen].join(" | ");
		})();
	`)
	if want := `format,size,width,height,space,channels,depth,density,isProgressive,isPalette,bitsPerSample,hasProfile,hasAlpha,autoOrient {"width":40,"height":20}`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSharpPixels(t *testing.T) {
	// Pixel-level checks in Go on what the JS pipeline produced.
	svc := natives.NewServices()
	for op, want := range map[string]color.NRGBA{
		"sharp(input).flop()":      {0, 0, 255, 255}, // left becomes blue
		"sharp(input)":             {255, 0, 0, 255},
		"sharp(input).rotate(180)": {0, 0, 255, 255},
		"sharp(input).resize(10, 10, { position: 'right' })": {0, 0, 255, 255}, // cover, right part kept
	} {
		out := run(t, svc, map[string]any{"input": halves(t)}, `
			import sharp from "sharp";
			globalThis.result = (async () => Array.from(await `+op+`.png().toBuffer()).join(","))();
		`)
		var b []byte
		for _, s := range strings.Split(out, ",") {
			var n int
			for _, c := range s {
				n = n*10 + int(c-'0')
			}
			b = append(b, byte(n))
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		// Resampling blends edges, so compare the dominant channel.
		c := color.NRGBAModel.Convert(img.At(img.Bounds().Min.X, img.Bounds().Min.Y)).(color.NRGBA)
		if (c.R > c.B) != (want.R > want.B) || absDiff(c.R, want.R) > 40 || absDiff(c.B, want.B) > 40 {
			t.Errorf("%s: first pixel %v, want about %v", op, c, want)
		}
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestBetterSqlite3(t *testing.T) {
	svc := natives.NewServices()
	defer svc.SQLite.Close()
	got := run(t, svc, nil, `
		import Database from "better-sqlite3";
		globalThis.result = (() => {
			const db = new Database(":memory:");
			db.exec("CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, score REAL, data BLOB)");
			const ins = db.prepare("INSERT INTO t (name, score, data) VALUES (?, ?, ?)");
			const r = ins.run("ann", 1.5, new Uint8Array([1, 2, 3]));
			db.prepare("INSERT INTO t (name, score) VALUES (@name, :score)").run({ name: "bob", score: 2 });
			const out = [r.changes, r.lastInsertRowid, ins.reader];
			const row = db.prepare("SELECT * FROM t WHERE name = ?").get("ann");
			out.push(row.id, row.name, row.score, Array.from(row.data).join("-"));
			out.push(db.prepare("SELECT name FROM t ORDER BY id").pluck().all().join("/"));
			out.push(JSON.stringify(db.prepare("SELECT id, name FROM t ORDER BY id").raw().all()));
			out.push(db.prepare("SELECT * FROM t WHERE id = 99").get() === undefined);
			out.push(typeof db.prepare("SELECT COUNT(*) AS n FROM t").safeIntegers().get().n);
			out.push(db.pragma("user_version", { simple: true }));
			// Transactions: commit, rollback on throw, nested savepoints.
			const add = db.transaction((name) => db.prepare("INSERT INTO t (name) VALUES (?)").run(name));
			add("carl");
			const failing = db.transaction(() => { add("dora"); throw new Error("boom"); });
			try { failing(); } catch (e) { out.push(e.message); }
			const outer = db.transaction(() => { add("eve"); try { db.transaction(() => { add("fay"); throw new Error("inner"); })(); } catch {} });
			outer();
			out.push(db.prepare("SELECT name FROM t ORDER BY id").pluck().all().join("/"));
			try { db.prepare("INSERT INTO t (id) VALUES (1)").run(); } catch (e) { out.push(e.name, e.code); }
			try { db.prepare("UPDATE t SET name = 'x'").get(); } catch (e) { out.push(e instanceof TypeError); }
			try { db.prepare("SELECT ?").run(true); } catch (e) { out.push(e instanceof TypeError); }
			db.close();
			try { db.prepare("SELECT 1"); } catch (e) { out.push(e.message); }
			return out.join(",");
		})();
	`)
	want := "1,1,false,1,ann,1.5,1-2-3,ann/bob,[[1,\"ann\"],[2,\"bob\"]],true,bigint,0,boom,ann/bob/carl/eve,SqliteError,SQLITE_CONSTRAINT_PRIMARYKEY,true,true,The database connection is not open"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestSqliteShared: a file database is shared by every request engine.
func TestSqliteShared(t *testing.T) {
	svc := natives.NewServices()
	defer svc.SQLite.Close()
	file := filepath.Join(t.TempDir(), "app.db")
	run(t, svc, map[string]any{"file": file}, `
		import Database from "better-sqlite3";
		const db = new Database(file);
		db.exec("CREATE TABLE IF NOT EXISTS visits (n INTEGER)");
		db.prepare("INSERT INTO visits VALUES (?)").run(42);
		globalThis.result = "ok";
	`)
	got := run(t, svc, map[string]any{"file": file}, `
		import Database from "better-sqlite3";
		globalThis.result = String(new Database(file, { readonly: true }).prepare("SELECT n FROM visits").pluck().get());
	`)
	if got != "42" {
		t.Errorf("second engine read %q", got)
	}
}

// TestSQLiteInOverlay: with the runtime's filesystem set (runtime/vfs's
// overlay), a database file lives in it, in memory: seeded from the
// packaged file, shared by engines, its current bytes readable through the
// overlay, the packaged file untouched, and a fresh database once the file
// is removed.
func TestSQLiteInOverlay(t *testing.T) {
	seedDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedDB.Exec("CREATE TABLE t (x INTEGER); INSERT INTO t VALUES (42)"); err != nil {
		t.Fatal(err)
	}
	conn, _ := seedDB.Conn(context.Background())
	var seed []byte
	_ = conn.Raw(func(dc any) error {
		b, err := dc.(interface{ Serialize() ([]byte, error) }).Serialize()
		seed = b
		return err
	})
	conn.Close()
	seedDB.Close()
	lower := fstest.MapFS{"data/seed.db": {Data: seed}}
	ov := vfs.NewOverlay(lower)
	svc := natives.NewServices()
	svc.SQLite.Files = ov
	first := run(t, svc, nil, `
		import Database from "better-sqlite3";
		const out = [];
		const db = new Database("data/seed.db");
		out.push(db.prepare("SELECT x FROM t").pluck().all().join("+"));
		db.prepare("INSERT INTO t VALUES (?)").run(43);
		db.close();
		const fresh = new Database("/scratch/new.db");
		fresh.exec("CREATE TABLE n (v TEXT); INSERT INTO n VALUES ('new')");
		fresh.close();
		try { new Database("missing.db", { fileMustExist: true }); out.push("opened"); } catch (e) { out.push("missing: " + e.code); }
		globalThis.result = out.join(",");
	`)
	if first != "42,missing: SQLITE_ERROR" {
		t.Errorf("first engine: %s", first)
	}
	second := run(t, svc, nil, `
		import Database from "better-sqlite3";
		const a = new Database("data/seed.db"), b = new Database("scratch/new.db");
		globalThis.result = [a.prepare("SELECT x FROM t ORDER BY x").pluck().all().join("+"), b.prepare("SELECT v FROM n").pluck().get()].join(",");
		a.close(); b.close();
	`)
	if second != "42+43,new" {
		t.Errorf("second engine: %s", second)
	}
	if b, err := ov.ReadFile("scratch/new.db"); err != nil || !bytes.HasPrefix(b, []byte("SQLite format 3\x00")) {
		t.Errorf("scratch/new.db through the overlay: %q %v", b[:min(16, len(b))], err)
	}
	if !bytes.Equal(lower["data/seed.db"].Data, seed) {
		t.Error("the packaged database changed")
	}
	if err := ov.Remove("scratch/new.db", false); err != nil {
		t.Fatal(err)
	}
	third := run(t, svc, nil, `
		import Database from "better-sqlite3";
		const b = new Database("scratch/new.db");
		globalThis.result = String(b.prepare("SELECT count(*) FROM sqlite_master").pluck().get());
		b.close();
	`)
	if third != "0" {
		t.Errorf("after removing the file: %s tables", third)
	}
}

// TestSQLiteDateText: text in DATE / DATETIME / TIMESTAMP columns comes
// back as stored, as better-sqlite3 gives it (TypeORM makes its Dates from
// it). modernc parses such text into time.Time; the substitute reads the
// stored text instead (storedText). The expectations are better-sqlite3's
// output on Node 24 for the same statements.
func TestSQLiteDateText(t *testing.T) {
	got := run(t, natives.NewServices(), nil, `
		import Database from "better-sqlite3";
		const db = new Database(":memory:");
		db.exec("CREATE TABLE d (a DATETIME, b DATE, c TIMESTAMP)");
		const ins = db.prepare("INSERT INTO d VALUES (?, ?, ?)");
		ins.run("2026-10-08 15:01:13", "2026-10-08", "2026-10-08T15:01:13.120Z");
		ins.run("2026-10-08 15:01:13.100", "x", 1700000000);
		ins.run(null, "2026-10-08T00:00", 1.5);
		globalThis.result = JSON.stringify(db.prepare("SELECT a, b, c, datetime(a) AS e FROM d").all()) + "\n" +
			JSON.stringify(db.prepare("SELECT a, b, c FROM d").raw().all());
	`)
	want := `[{"a":"2026-10-08 15:01:13","b":"2026-10-08","c":"2026-10-08T15:01:13.120Z","e":"2026-10-08 15:01:13"},{"a":"2026-10-08 15:01:13.100","b":"x","c":1700000000,"e":"2026-10-08 15:01:13"},{"a":null,"b":"2026-10-08T00:00","c":1.5,"e":null}]` + "\n" +
		`[["2026-10-08 15:01:13","2026-10-08","2026-10-08T15:01:13.120Z"],["2026-10-08 15:01:13.100","x",1700000000],[null,"2026-10-08T00:00",1.5]]`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestSQLiteErrors: errors carry better-sqlite3's code (SQLite's extended
// result code name, which Prisma's adapter maps to its error codes) and
// message (SQLite's own, without modernc's "errstr: ... (rc)" wrapping).
// The expectations are better-sqlite3's on Node 24.
func TestSQLiteErrors(t *testing.T) {
	got := run(t, natives.NewServices(), nil, `
		import Database from "better-sqlite3";
		const db = new Database(":memory:");
		db.exec("CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT UNIQUE, n INTEGER NOT NULL DEFAULT 0, CHECK (n >= 0))");
		db.prepare("INSERT INTO t (id, name) VALUES (1, ?)").run("ann");
		const out = [];
		for (const q of ["INSERT INTO t (id, name) VALUES (1, 'x')", "INSERT INTO t (id, name) VALUES (2, 'ann')", "INSERT INTO t (id, name, n) VALUES (3, 'y', NULL)", "INSERT INTO t (id, name, n) VALUES (4, 'z', -1)", "INSERT INTO nope VALUES (1)", "SELEC 1"]) {
			try { db.exec(q); } catch (e) { out.push(JSON.stringify([e.code, e.message])); }
		}
		globalThis.result = out.join("\n");
	`)
	want := strings.Join([]string{
		`["SQLITE_CONSTRAINT_PRIMARYKEY","UNIQUE constraint failed: t.id"]`,
		`["SQLITE_CONSTRAINT_UNIQUE","UNIQUE constraint failed: t.name"]`,
		`["SQLITE_CONSTRAINT_NOTNULL","NOT NULL constraint failed: t.n"]`,
		`["SQLITE_CONSTRAINT_CHECK","CHECK constraint failed: n >= 0"]`,
		`["SQLITE_ERROR","no such table: nope"]`,
		`["SQLITE_ERROR","near \"SELEC\": syntax error"]`,
	}, "\n")
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestSQLiteColumns: statement.columns() describes the prepared statement
// without running it, as better-sqlite3's does (it ran the query: Prisma's
// adapter asks for an INSERT ... RETURNING's columns first, and every row
// was inserted twice). The expectation is better-sqlite3's on Node 24.
func TestSQLiteColumns(t *testing.T) {
	got := run(t, natives.NewServices(), nil, `
		import Database from "better-sqlite3";
		const db = new Database(":memory:");
		db.exec("CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT, at DATETIME)");
		const st = db.prepare("INSERT INTO t (name) VALUES (?) RETURNING id, name, upper(name) AS u, at");
		globalThis.result = JSON.stringify(st.columns()) + "\n" + db.prepare("SELECT count(*) AS n FROM t").get().n;
	`)
	want := `[{"name":"id","column":"id","table":"t","database":"main","type":"INTEGER"},{"name":"name","column":"name","table":"t","database":"main","type":"TEXT"},{"name":"u","column":null,"table":null,"database":null,"type":null},{"name":"at","column":"at","table":"t","database":"main","type":"DATETIME"}]` + "\n0"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
