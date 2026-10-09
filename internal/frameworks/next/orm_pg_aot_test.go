//go:build aot || wazero

package next

import (
	"net"
	"testing"
	"time"

	pglite "github.com/moriyoshi/pglite-go"
)

// startPostgres serves PostgreSQL 18 embedded in the test process
// (moriyoshi/pglite-go's pure-Go builds: -tags wazero, or -tags aot with
// the AOT companion, orm_pg_companion_test.go) on a free local
// port, one client session at a time (ServeConn), and returns its URL. The
// PGlite data bundle is fetched into the user cache the first time
// (Download); without it the test skips.
func startPostgres(t *testing.T) string {
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

// TestORMDriverPostgres: pg (Drizzle's node-postgres driver, Kysely's
// PostgresDialect) and postgres.js (Drizzle's postgres-js driver) over og's
// node:net against PostgreSQL 18 (PGlite). The expectations are what
// `next start` on Node 24 answers against the same server.
//
// pg runs twice against a server that serves one session at a time: the
// second request can only get one by taking over the session the first
// left in og's pool (netpool.go), which the drivers' own pools, closed at
// the end of each request, would have ended. postgres.js, whose startup
// differs, has a server of its own (a parked pg session would hold the
// only one).
func TestORMDriverPostgres(t *testing.T) {
	t.Setenv("PG_URL", startPostgres(t))
	t.Setenv("PGJS_URL", startPostgres(t))
	srv := serveORMFixture(t)
	want := map[string]string{
		"pg":       `{"drizzle":[{"id":1,"name":"alpha","done":false},{"id":2,"name":"beta","done":true}],"rollback":"failed","count":"2","kysely":[{"name":"alpha","done":true},{"name":"beta","done":true}]}`,
		"postgres": `{"drizzle":[{"id":1,"name":"alpha","done":false},{"id":2,"name":"beta","done":true}],"rollback":"failed","count":"2"}`,
	}
	for _, driver := range []string{"pg", "pg", "postgres"} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			if status, body := getBody(t, srv.URL+"/api/drivers/"+driver); status != 200 || body != want[driver] {
				t.Errorf("GET /api/drivers/%s: %d\n%s\nwant %s", driver, status, body, want[driver])
			}
		}()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Fatalf("%s: no answer in 60 s", driver)
		}
	}
}
