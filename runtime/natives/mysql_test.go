package natives_test

import (
	"net"
	"testing"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/go-mysql-server/sql"

	"github.com/moriyoshi/oganesson/runtime/natives"
)

// startMySQL serves an in-memory MySQL-compatible database
// (dolthub/go-mysql-server) named "og" on a free local port and returns its URL
// (user root, no password). Mirrors internal/frameworks/next's startMySQL.
func startMySQL(t *testing.T) string {
	t.Helper()
	db := memory.NewDatabase("og")
	db.EnablePrimaryKeyIndexes()
	pro := memory.NewDBProvider(db)
	eng := sqle.NewDefault(pro)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s, err := server.NewServer(server.Config{Protocol: "tcp", Address: ln.Addr().String(), Listener: ln}, eng, sql.NewContext, memory.NewSessionBuilder(pro), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Start() }()
	t.Cleanup(func() { _ = s.Close() })
	return "mysql://root@" + ln.Addr().String() + "/og"
}

// TestMySQL2 drives the mysql2 substitute (both the promise and the callback
// API) against go-mysql-server: DDL, parameterized inserts, object and
// rowsAsArray reads, a committed and a failing (rolled-back) transaction, and
// the field metadata. The notable fidelity point is that count(*) comes back as
// the number 2 (mysql2 returns BIGINT that fits as a JS number), unlike pg's
// int8 which is a string.
func TestMySQL2(t *testing.T) {
	svc := natives.NewServices()
	defer svc.MySQL.Close()
	got := run(t, svc, map[string]any{"URL": startMySQL(t)}, `
		import mysql from "mysql2/promise";
		import { createPool } from "mysql2";
		globalThis.result = (async () => {
			const pool = mysql.createPool({ uri: URL, connectionLimit: 2 });
			await pool.query("DROP TABLE IF EXISTS t");
			await pool.query("CREATE TABLE t (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(64) NOT NULL, done BOOLEAN NOT NULL DEFAULT FALSE)");
			await pool.query("INSERT INTO t (name, done) VALUES (?, ?)", ["alpha", 0]);
			await pool.query("INSERT INTO t (name, done) VALUES (?, ?)", ["beta", 1]);
			const [rows, fields] = await pool.query("SELECT id, name, done FROM t ORDER BY id");
			const [arr] = await pool.query({ sql: "SELECT id, name FROM t ORDER BY id", rowsAsArray: true });
			const [cnt] = await pool.query("SELECT count(*) AS n FROM t");
			// A committed transaction on a pinned connection.
			const conn = await pool.getConnection();
			await conn.beginTransaction();
			await conn.query("UPDATE t SET done = 1 WHERE name = ?", ["alpha"]);
			await conn.commit();
			conn.release();
			// A failing transaction rolls back (the DELETE is undone).
			let rollback = "ok";
			const c2 = await pool.getConnection();
			try {
				await c2.beginTransaction();
				await c2.query("DELETE FROM t");
				await c2.query("SELECT * FROM og_missing");
				await c2.commit();
			} catch (e) {
				await c2.rollback();
				rollback = "failed";
			}
			c2.release();
			const [after] = await pool.query("SELECT count(*) AS n FROM t");
			// The callback API (Kysely's path): getConnection(cb), query(sql, cb).
			const kysely = await new Promise((res, rej) => {
				const cb = createPool({ uri: URL, connectionLimit: 1 });
				cb.getConnection((err, c) => {
					if (err) return rej(err);
					c.query("SELECT name, done FROM t ORDER BY id", (e, r) => {
						c.release();
						e ? rej(e) : res(r);
					});
				});
			});
			await pool.end();
			return JSON.stringify({
				rows,
				arr,
				fields: fields.map((f) => f.name).join(","),
				count: cnt[0].n,
				countType: typeof cnt[0].n,
				rollback,
				after: after[0].n,
				kysely,
			});
		})();
	`)
	want := `{"rows":[{"id":1,"name":"alpha","done":0},{"id":2,"name":"beta","done":1}],` +
		`"arr":[[1,"alpha"],[2,"beta"]],"fields":"id,name,done","count":2,"countType":"number",` +
		`"rollback":"failed","after":2,"kysely":[{"name":"alpha","done":1},{"name":"beta","done":1}]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
