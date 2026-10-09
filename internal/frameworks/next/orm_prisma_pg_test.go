//go:build aot || wazero

package next

import (
	"testing"
)

// TestPrismaPostgres: Prisma 7's generated client over @prisma/adapter-pg,
// which runs on og's native pg shim (runtime/natives) against PostgreSQL 18
// (PGlite). A second `prisma generate --schema prisma/schema-pg.prisma` in the
// fixture's build script produces the client (generated/prisma-pg). Scalar
// columns only; the expectation is what Prisma answers over the same adapter on
// next start.
//
// Getting this green against single-session PGlite took four fixes, each hidden
// behind the previous one (see JOURNAL 2026-10-14):
//   - app.Prerender leaked a warm pg connection: it builds a full app (its own
//     native services) and executing the force-dynamic route once to detect it
//     is dynamic opens a connection it never released. That held PGlite's one
//     session, so the serving app's connection timed out. Prerender now closes
//     its services (runtime/app/prerender.go).
//   - Concurrent get(dsn) for a new pool both dialled; the second connection to
//     the single backend timed out. pg.go now single-flights pool creation.
//   - pgx's default statement cache desynced from PGlite, which drops prepared
//     statements on the route's transaction rollback: `prepared statement
//     "stmtcache_…" already exists`. pg.go uses QueryExecModeCacheDescribe.
//   - The pg shim's pg.types.builtins was empty, so adapter-pg's
//     ScalarColumnType.INT4 (= builtins.INT4) was undefined and every column
//     fell through to "unsupported native type". js/pg.js now fills builtins.
func TestPrismaPostgres(t *testing.T) {
	t.Setenv("PRISMA_PG_URL", startPostgres(t))
	srv := serveORMFixture(t)
	want := `{"rows":[{"id":1,"name":"alpha","active":false,"qty":3},{"id":2,"name":"beta","active":true,"qty":7}],"active":[{"name":"beta"}],"sum":10,"rollback":"abort","count":2}`
	if status, body := getBody(t, srv.URL+"/api/prisma-pg"); status != 200 || body != want {
		t.Errorf("GET /api/prisma-pg: %d\n%s\nwant %s", status, body, want)
	}
}
