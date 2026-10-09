package next

import (
	"path/filepath"
	"testing"
)

// TestORMDriverLibSQL: Drizzle's libsql driver over @libsql/client, which og
// substitutes onto its SQLite service (runtime/natives, the same one
// better-sqlite3 uses), against a local file database. libsql has no Kysely
// dialect, so only Drizzle runs. The expectation is SQLite semantics (what
// next start answers to the same requests): count is a number, and the boolean
// column maps 0/1 to false/true.
func TestORMDriverLibSQL(t *testing.T) {
	t.Setenv("LIBSQL_URL", "file:"+filepath.Join(t.TempDir(), "libsql.db"))
	srv := serveORMFixture(t)
	want := `{"drizzle":[{"id":1,"name":"alpha","done":false},{"id":2,"name":"beta","done":true}],"rollback":"failed","count":2}`
	if status, body := getBody(t, srv.URL+"/api/drivers/libsql"); status != 200 || body != want {
		t.Errorf("GET /api/drivers/libsql: %d\n%s\nwant %s", status, body, want)
	}
}
