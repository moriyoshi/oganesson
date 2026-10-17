package next

import (
	"testing"
)

// TestPrismaMySQL: Prisma 7's generated client over @prisma/adapter-mariadb,
// which runs on og's native mariadb driver shim (runtime/natives/js/mariadb.js,
// over the shared Go MySQL service) against go-mysql-server. A `prisma generate
// --config prisma-mysql.config.ts` in the fixture's build script produces the
// client (generated/prisma-mysql).
//
// The expectation is byte-identical to TestPrismaPostgres' (same Widget model
// and operations): Prisma abstracts the database, so a fidelity bug in the shim
// — a BIGINT count that stays a string, a TINYINT(1) that does not coerce to a
// boolean, a mishandled transaction rollback — makes the JSON diverge from what
// Prisma answers over pg (and what next start answers over mysql2).
func TestPrismaMySQL(t *testing.T) {
	url, _ := startMySQL(t)
	t.Setenv("PRISMA_MYSQL_URL", url)
	srv := serveORMFixture(t)
	want := `{"rows":[{"id":1,"name":"alpha","active":false,"qty":3},{"id":2,"name":"beta","active":true,"qty":7}],"active":[{"name":"beta"}],"sum":10,"rollback":"abort","count":2}`
	if status, body := getBody(t, srv.URL+"/api/prisma-mysql"); status != 200 || body != want {
		t.Errorf("GET /api/prisma-mysql: %d\n%s\nwant %s", status, body, want)
	}
}
