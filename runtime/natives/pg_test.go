//go:build aot || wazero

package natives_test

import (
	"net"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/natives"
	pglite "github.com/moriyoshi/pglite-go"
)

// startPG serves PostgreSQL 18 embedded in the test process (pglite-go's
// pure-Go wazero build, or the AOT companion) on a free local port, and
// returns its URL. The data bundle is fetched once (Download); without it the
// test skips. Mirrors startPostgres in internal/frameworks/next.
func startPG(t *testing.T) string {
	t.Helper()
	db, err := pglite.Open(pglite.Config{Download: true, Ephemeral: true, Database: "template1"})
	if err != nil {
		t.Skipf("pglite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				if err := db.ServeConn(c); err != nil {
					t.Logf("pglite ServeConn: %v", err)
				}
			}()
		}
	}()
	return "postgres://postgres@" + ln.Addr().String() + "/template1"
}

// TestPG drives the native pg shim directly (what Drizzle/Kysely do: pool
// queries, a pinned client for a transaction, a failing transaction that rolls
// back), asserting node-postgres-shaped results against PGlite. The ORM glue is
// covered by TestORMDriverPostgres; this pins the shim's own behaviour:
//   - int8 (count) comes back as the string "2", id as a number, done boolean;
//   - field dataTypeIDs are the Postgres OIDs (int4 23, text 25, bool 16);
//   - a bad statement rejects with the SQLSTATE as .code (42P01);
//   - rowMode "array" returns positional rows (Drizzle's path).
func TestPG(t *testing.T) {
	svc := natives.NewServices()
	defer svc.Postgres.Close()
	url := startPG(t)
	got := run(t, svc, map[string]any{"PGURL": url}, `
		import pg from "pg";
		globalThis.result = (async () => {
			const out = {};
			const pool = new pg.Pool({ connectionString: PGURL, max: 1 });
			const run = (q, params) => pool.query(q, params);
			await run("DROP TABLE IF EXISTS og_items");
			await run("CREATE TABLE og_items (id SERIAL PRIMARY KEY, name TEXT NOT NULL, done BOOLEAN NOT NULL DEFAULT FALSE)");
			await run("INSERT INTO og_items (name, done) VALUES ($1,$2),($3,$4)", ["alpha", false, "beta", true]);
			const sel = await run("SELECT id, name, done FROM og_items ORDER BY id");
			out.rows = sel.rows;
			out.fields = sel.fields.map((f) => f.name + ":" + f.dataTypeID).join(",");
			out.types = [typeof sel.rows[0].id, typeof sel.rows[0].name, typeof sel.rows[0].done].join(",");
			const c = await pool.connect();
			await c.query("BEGIN");
			const up = await c.query("UPDATE og_items SET done = true WHERE name = $1", ["alpha"]);
			out.updated = up.rowCount;
			await c.query("COMMIT");
			c.release();
			const c2 = await pool.connect();
			try {
				await c2.query("BEGIN");
				await c2.query("DELETE FROM og_items");
				await c2.query("SELECT * FROM og_missing");
				await c2.query("COMMIT");
				out.rollback = "no error";
			} catch (e) {
				await c2.query("ROLLBACK");
				out.rollback = "failed:" + e.code;
			} finally {
				c2.release();
			}
			const cnt = await run("SELECT count(*) AS n FROM og_items");
			out.count = cnt.rows[0].n;
			const arr = await pool.query({ text: "SELECT id, name FROM og_items ORDER BY id", rowMode: "array" });
			out.array = JSON.stringify(arr.rows);
			await pool.end();
			return JSON.stringify(out);
		})();
	`)
	want := `{"rows":[{"id":1,"name":"alpha","done":false},{"id":2,"name":"beta","done":true}],"fields":"id:23,name:25,done:16","types":"number,string,boolean","updated":1,"rollback":"failed:42P01","count":"2","array":"[[1,\"alpha\"],[2,\"beta\"]]"}`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}

	// The shared Go pool stays warm after pool.end(): a second request engine,
	// sharing svc, reuses it (PGlite serves one session at a time).
	second := run(t, svc, map[string]any{"PGURL": url}, `
		import pg from "pg";
		globalThis.result = (async () => {
			const pool = new pg.Pool({ connectionString: PGURL, max: 1 });
			const r = await pool.query("SELECT count(*) AS n FROM og_items");
			await pool.end();
			return r.rows[0].n;
		})();
	`)
	if second != "2" {
		t.Errorf("second engine (warm pool) read %q, want \"2\"", second)
	}
}
