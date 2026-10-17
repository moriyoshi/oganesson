package natives_test

import (
	"encoding/json"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/natives"
)

// TestMariaDB drives the mariadb driver shim (js/mariadb.js) the way
// @prisma/adapter-mariadb does, against go-mysql-server: createPool, DDL, a
// parameterized insert, a rowsAsArray SELECT read through result.meta (the
// column defs the adapter maps to Prisma column types), a committed
// transaction on a pinned connection, and a driver error carrying errno +
// sqlMessage (which the adapter's isDriverError predicate requires). The
// fidelity points vs mysql2: a BIGINT column (and count(*)) comes back as a
// string (Prisma Int64), a signed INT as a number (Int32), and DECIMAL as a
// string (Numeric).
func TestMariaDB(t *testing.T) {
	svc := natives.NewServices()
	defer svc.MySQL.Close()
	got := run(t, svc, map[string]any{"URL": startMySQL(t)}, `
		import { createPool } from "mariadb";
		// The adapter always queries with these options; the shim produces the
		// equivalent shape regardless, so passing them must not change anything.
		const opts = { rowsAsArray: true, dateStrings: true, autoJsonMap: false, jsonStrings: true, bitOneIsBoolean: false };
		const q = (client, sql, values) => client.query({ ...opts, sql }, values);
		globalThis.result = (async () => {
			const pool = createPool(URL + "?prepareCacheLength=0");
			await q(pool, "DROP TABLE IF EXISTS t");
			await q(pool, "CREATE TABLE t (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL, big BIGINT NOT NULL, amt DECIMAL(10,2) NOT NULL, UNIQUE KEY uq_name (name))");
			const ins = await pool.execute({ ...opts, sql: "INSERT INTO t (name, big, amt) VALUES (?, ?, ?)" }, ["alpha", "9007199254740993", "12.50"]);
			await pool.execute({ ...opts, sql: "INSERT INTO t (name, big, amt) VALUES (?, ?, ?)" }, ["beta", "2", "0.00"]);

			const rows = await q(pool, "SELECT id, name, big, amt FROM t ORDER BY id");
			const meta = rows.meta.map((f) => ({ name: f.name(), type: f.type, unsigned: !!(f.flags & 32), binary: f.collation.index === 63 }));

			const cnt = await q(pool, "SELECT count(*) AS n FROM t");

			// A committed transaction on a pinned connection.
			const conn = await pool.getConnection();
			await conn.query({ sql: "BEGIN" });
			await conn.execute({ ...opts, sql: "UPDATE t SET amt = ? WHERE name = ?" }, ["99.99", "beta"]);
			await conn.query({ sql: "COMMIT" });
			await conn.release();
			const afterBeta = await q(pool, "SELECT amt FROM t WHERE name = 'beta'");

			// A driver error must carry errno (number) and sqlMessage (string).
			let err = null;
			try {
				await q(pool, "SELECT * FROM og_missing");
			} catch (e) {
				err = { errno: typeof e.errno, hasMsg: typeof e.sqlMessage === "string" && e.sqlMessage.length > 0 };
			}
			await pool.end();
			return JSON.stringify({
				insAffected: ins.affectedRows,
				rows,
				rowTypes: rows.map((r) => r.map((v) => typeof v)),
				meta,
				count: cnt[0][0],
				countType: typeof cnt[0][0],
				afterBeta: afterBeta[0][0],
				err,
			});
		})();
	`)
	var g map[string]any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, got)
	}
	want := `{"insAffected":1,` +
		`"rows":[[1,"alpha","9007199254740993","12.50"],[2,"beta","2","0.00"]],` +
		`"rowTypes":[["number","string","string","string"],["number","string","string","string"]],` +
		`"meta":[{"name":"id","type":"INT","unsigned":false,"binary":false},` +
		`{"name":"name","type":"VARCHAR","unsigned":false,"binary":false},` +
		`{"name":"big","type":"BIGINT","unsigned":false,"binary":false},` +
		`{"name":"amt","type":"NEWDECIMAL","unsigned":false,"binary":false}],` +
		`"count":"2","countType":"string","afterBeta":"99.99",` +
		`"err":{"errno":"number","hasMsg":true}}`
	// Compare as canonical JSON so key order is irrelevant.
	var w map[string]any
	_ = json.Unmarshal([]byte(want), &w)
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("got  %s\nwant %s", gb, wb)
	}
}
