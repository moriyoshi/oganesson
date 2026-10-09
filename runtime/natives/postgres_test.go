//go:build aot || wazero

package natives_test

import (
	"testing"

	"github.com/moriyoshi/oganesson/runtime/natives"
)

// TestPostgresJS drives the native postgres.js shim directly (what Drizzle's
// postgres-js driver does: client.unsafe(q, params) in object mode and
// .values() for positional rows, client.begin for transactions), asserting
// postgres.js-shaped results against PGlite. The shim reuses pg's Go service
// (Symbol.for("og.pg")); this pins its JS behaviour:
//   - object rows from unsafe(), positional rows from .values();
//   - int8 (count) comes back as the string "2";
//   - a failing begin() rolls back and rejects with the SQLSTATE as .code;
//   - a tagged template substitutes $1 placeholders.
func TestPostgresJS(t *testing.T) {
	svc := natives.NewServices()
	defer svc.Postgres.Close()
	url := startPG(t)
	got := run(t, svc, map[string]any{"PGURL": url}, `
		import postgres from "postgres";
		globalThis.result = (async () => {
			const out = {};
			const sql = postgres(PGURL, { max: 1, onnotice: () => {} });
			await sql.unsafe("DROP TABLE IF EXISTS og_items");
			await sql.unsafe("CREATE TABLE og_items (id SERIAL PRIMARY KEY, name TEXT NOT NULL, done BOOLEAN NOT NULL DEFAULT FALSE)");
			await sql.unsafe("INSERT INTO og_items (name, done) VALUES ($1,$2),($3,$4)", ["alpha", false, "beta", true]);
			out.rows = await sql.unsafe("SELECT id, name, done FROM og_items ORDER BY id");
			out.values = await sql.unsafe("SELECT id, name FROM og_items ORDER BY id").values();
			out.types = [typeof out.rows[0].id, typeof out.rows[0].name, typeof out.rows[0].done].join(",");
			// A committed transaction on a pinned connection.
			await sql.begin(async (tx) => {
				await tx.unsafe("UPDATE og_items SET done = true WHERE name = $1", ["alpha"]);
			});
			// A failing transaction rolls back and rejects with the SQLSTATE.
			try {
				await sql.begin(async (tx) => {
					await tx.unsafe("DELETE FROM og_items");
					await tx.unsafe("SELECT * FROM og_missing");
				});
				out.rollback = "no error";
			} catch (e) {
				out.rollback = "failed:" + e.code;
			}
			out.count = (await sql.unsafe("SELECT count(*) AS n FROM og_items"))[0].n;
			out.tagged = (await sql`+"`SELECT ${\"hi\"}::text AS t`"+`)[0].t;
			await sql.end();
			return JSON.stringify(out);
		})();
	`)
	want := `{"rows":[{"id":1,"name":"alpha","done":false},{"id":2,"name":"beta","done":true}],"values":[[1,"alpha"],[2,"beta"]],"types":"number,string,boolean","rollback":"failed:42P01","count":"2","tagged":"hi"}`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
