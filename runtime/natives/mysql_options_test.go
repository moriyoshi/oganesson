package natives_test

import (
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/natives"
)

// TestMySQLConnectionOptionsDSN pins the mysql:// URL the mysql2 shim builds
// (js/mysql2.js dsn/withOptions) from discrete connection options. No database
// is contacted: a Pool's constructor only computes its DSN (_dsn).
// multipleStatements -> multiStatements, connectTimeout (ms) -> a Go-duration
// timeout, charset passes through; a value already in a uri wins.
func TestMySQLConnectionOptionsDSN(t *testing.T) {
	svc := natives.NewServices()
	defer svc.MySQL.Close()
	got := run(t, svc, nil, `
		import mysql from "mysql2";
		const p = mysql.createPool({
			host: "127.0.0.1",
			port: 3306,
			database: "og",
			multipleStatements: true,
			connectTimeout: 7000,
			charset: "utf8mb4",
		});
		globalThis.result = p._dsn;
	`)
	for _, want := range []string{
		"multiStatements=true",
		"timeout=7000ms",
		"charset=utf8mb4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("DSN %q lacks %q", got, want)
		}
	}

	// A value already in the uri is kept, not duplicated.
	got2 := run(t, svc, nil, `
		import mysql from "mysql2";
		globalThis.result = mysql.createPool({ uri: "mysql://root@127.0.0.1:3306/og?multiStatements=false", multipleStatements: true })._dsn;
	`)
	if strings.Count(got2, "multiStatements=") != 1 || !strings.Contains(got2, "multiStatements=false") {
		t.Errorf("DSN %q should keep the uri's multiStatements, not append another", got2)
	}
}

// TestMySQLDateStrings drives the mysql2 dateStrings option end to end against
// go-mysql-server: by default DATE/DATETIME columns come back as JS Date; with
// dateStrings:true they are the MySQL literal strings; with an array only the
// listed types are strings (the rest stay Date).
func TestMySQLDateStrings(t *testing.T) {
	svc := natives.NewServices()
	defer svc.MySQL.Close()
	url := startMySQL(t)
	got := run(t, svc, map[string]any{"URL": url}, `
		import mysql from "mysql2/promise";
		globalThis.result = (async () => {
			const out = {};
			const setup = await mysql.createConnection({ uri: URL });
			await setup.query("CREATE TABLE evt (id INT PRIMARY KEY, d DATE, ts DATETIME)");
			await setup.query("INSERT INTO evt VALUES (1, '2024-01-02', '2024-01-02 03:04:05')");
			await setup.end();

			const def = await mysql.createConnection({ uri: URL });
			const [r1] = await def.query("SELECT d, ts FROM evt WHERE id = 1");
			out.default = (r1[0].d instanceof Date) + "," + (r1[0].ts instanceof Date);
			await def.end();

			const ds = await mysql.createConnection({ uri: URL, dateStrings: true });
			const [r2] = await ds.query("SELECT d, ts FROM evt WHERE id = 1");
			out.strings = [typeof r2[0].d, r2[0].d, typeof r2[0].ts, r2[0].ts].join("|");
			await ds.end();

			const ds2 = await mysql.createConnection({ uri: URL, dateStrings: ["DATE"] });
			const [r3] = await ds2.query("SELECT d, ts FROM evt WHERE id = 1");
			out.mixed = (typeof r3[0].d) + "," + (r3[0].ts instanceof Date);
			await ds2.end();
			return JSON.stringify(out);
		})();
	`)
	want := `{"default":"true,true","strings":"string|2024-01-02|string|2024-01-02 03:04:05","mixed":"string,true"}`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestMySQLBigNumbers drives mysql2's supportBigNumbers/bigNumberStrings against
// go-mysql-server: by default a BIGINT is a (lossy) number; with
// supportBigNumbers a value beyond JS's safe-integer range comes back as a
// string while a small one stays a number; with bigNumberStrings every BIGINT
// is a string.
func TestMySQLBigNumbers(t *testing.T) {
	svc := natives.NewServices()
	defer svc.MySQL.Close()
	url := startMySQL(t)
	got := run(t, svc, map[string]any{"URL": url}, `
		import mysql from "mysql2/promise";
		globalThis.result = (async () => {
			const out = {};
			const setup = await mysql.createConnection({ uri: URL });
			await setup.query("CREATE TABLE big (id INT PRIMARY KEY, n BIGINT)");
			// 9007199254740993 = 2^53 + 1 (beyond Number.MAX_SAFE_INTEGER); 42 fits.
			await setup.query("INSERT INTO big VALUES (1, 9007199254740993), (2, 42)");
			await setup.end();

			const def = await mysql.createConnection({ uri: URL });
			const [d] = await def.query("SELECT n FROM big ORDER BY id");
			out.default = typeof d[0].n + "," + typeof d[1].n;
			await def.end();

			const sbn = await mysql.createConnection({ uri: URL, supportBigNumbers: true });
			const [s] = await sbn.query("SELECT n FROM big ORDER BY id");
			out.support = [typeof s[0].n, s[0].n, typeof s[1].n, s[1].n].join("|");
			await sbn.end();

			const bns = await mysql.createConnection({ uri: URL, supportBigNumbers: true, bigNumberStrings: true });
			const [b] = await bns.query("SELECT n FROM big ORDER BY id");
			out.strings = [typeof b[0].n, b[0].n, typeof b[1].n, b[1].n].join("|");
			await bns.end();
			return JSON.stringify(out);
		})();
	`)
	want := `{"default":"number,number","support":"string|9007199254740993|number|42","strings":"string|9007199254740993|string|42"}`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestMySQLTypeCast drives mysql2's typeCast(field, next) callback against
// go-mysql-server: the field exposes mysql2's type-name string (INT -> LONG,
// TINYINT -> TINY, VARCHAR -> VAR_STRING), field.string() the raw value, and
// next() the default conversion. A callback coerces TINYINT to boolean,
// uppercases a VARCHAR, and defers the rest to next().
func TestMySQLTypeCast(t *testing.T) {
	svc := natives.NewServices()
	defer svc.MySQL.Close()
	url := startMySQL(t)
	got := run(t, svc, map[string]any{"URL": url}, `
		import mysql from "mysql2/promise";
		globalThis.result = (async () => {
			const out = {};
			const setup = await mysql.createConnection({ uri: URL });
			await setup.query("CREATE TABLE tc (id INT PRIMARY KEY, flag TINYINT, name VARCHAR(20))");
			await setup.query("INSERT INTO tc VALUES (1, 1, 'alpha'), (2, 0, 'beta')");
			await setup.end();

			const types = [];
			const conn = await mysql.createConnection({
				uri: URL,
				typeCast: (field, next) => {
					types.push(field.name + ":" + field.type);
					if (field.type === "TINY") return field.string() === "1";
					if (field.type === "VAR_STRING") return field.string().toUpperCase();
					return next();
				},
			});
			const [rows] = await conn.query("SELECT id, flag, name FROM tc ORDER BY id");
			await conn.end();
			out.row0 = [typeof rows[0].id, rows[0].id, typeof rows[0].flag, rows[0].flag, rows[0].name].join("|");
			out.row1flag = rows[1].flag;
			out.types = types.slice(0, 3).join(",");
			return JSON.stringify(out);
		})();
	`)
	want := `{"row0":"number|1|boolean|true|ALPHA","row1flag":false,"types":"id:LONG,flag:TINY,name:VAR_STRING"}`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestMySQLTimezone drives mysql2's timezone option against go-mysql-server: it
// decides which instant a DATETIME wall-clock names. "Z" interprets it as UTC
// (og's baseline), "+05:00" as that offset, so the resulting Date's UTC time
// shifts by five hours.
func TestMySQLTimezone(t *testing.T) {
	svc := natives.NewServices()
	defer svc.MySQL.Close()
	url := startMySQL(t)
	got := run(t, svc, map[string]any{"URL": url}, `
		import mysql from "mysql2/promise";
		globalThis.result = (async () => {
			const out = {};
			const setup = await mysql.createConnection({ uri: URL });
			await setup.query("CREATE TABLE evt (id INT PRIMARY KEY, ts DATETIME)");
			await setup.query("INSERT INTO evt VALUES (1, '2024-01-02 03:04:05')");
			await setup.end();

			const z = await mysql.createConnection({ uri: URL, timezone: "Z" });
			out.utc = (await z.query("SELECT ts FROM evt WHERE id = 1"))[0][0].ts.toISOString();
			await z.end();

			const p5 = await mysql.createConnection({ uri: URL, timezone: "+05:00" });
			out.plus5 = (await p5.query("SELECT ts FROM evt WHERE id = 1"))[0][0].ts.toISOString();
			await p5.end();
			return JSON.stringify(out);
		})();
	`)
	want := `{"utc":"2024-01-02T03:04:05.000Z","plus5":"2024-01-01T22:04:05.000Z"}`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
