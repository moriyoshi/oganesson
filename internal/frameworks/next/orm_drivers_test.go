package next

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/server"
	"github.com/dolthub/go-mysql-server/sql"

	"github.com/moriyoshi/oganesson/runtime/app"
)

// countingListener counts the connections it accepts.
type countingListener struct {
	net.Listener
	n atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}

// startMySQL serves an in-memory MySQL-compatible database
// (dolthub/go-mysql-server) named "og" on a free local port and returns
// its URL (user root, no password) and its accept counter.
func startMySQL(t *testing.T) (string, *countingListener) {
	t.Helper()
	db := memory.NewDatabase("og")
	db.EnablePrimaryKeyIndexes()
	pro := memory.NewDBProvider(db)
	engine := sqle.NewDefault(pro)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cl := &countingListener{Listener: ln}
	s, err := server.NewServer(server.Config{Protocol: "tcp", Address: ln.Addr().String(), Listener: cl}, engine, sql.NewContext, memory.NewSessionBuilder(pro), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Start() }()
	t.Cleanup(func() { _ = s.Close() })
	return "mysql://root@" + ln.Addr().String() + "/og", cl
}

// serveORMFixture builds testdata/apps/next-orm and serves it like a
// packaged binary.
func serveORMFixture(t *testing.T) *httptest.Server {
	t.Helper()
	dir := appDir("next-orm")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-orm)")
	}
	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := packageFixture(out)
	if err != nil {
		t.Fatal(err)
	}
	h, meta, err := app.Handler(fsys, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Release the warm pg pools before the test's PGlite servers close; an
	// open pooled connection would otherwise block pglite's Close.
	if meta.Close != nil {
		t.Cleanup(meta.Close)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, strings.TrimSpace(string(b))
}

// TestORMDriverMySQL: mysql2 over og's node:net (Drizzle's mysql2 driver
// and Kysely's MysqlDialect, pools included) against go-mysql-server. The
// expectation is what `next start` on Node 24 answers against the same
// server.
//
// The second request opens no connection: the first one's sessions, ended
// with COM_QUIT by the drivers' pools, were reset and parked by og's pool
// (netpool_mysql.go) and are taken over.
func TestORMDriverMySQL(t *testing.T) {
	url, accepts := startMySQL(t)
	t.Setenv("MYSQL_URL", url)
	srv := serveORMFixture(t)
	want := `{"drizzle":[{"id":1,"name":"alpha","done":false},{"id":2,"name":"beta","done":true}],"rollback":"failed","count":2,"kysely":[{"name":"alpha","done":1},{"name":"beta","done":1}]}`
	var opened []int64
	for i := 0; i < 2; i++ {
		if status, body := getBody(t, srv.URL+"/api/drivers/mysql2"); status != 200 || body != want {
			t.Errorf("GET /api/drivers/mysql2 (%d): %d\n%s\nwant %s", i+1, status, body, want)
		}
		time.Sleep(200 * time.Millisecond) // the sessions are reset and parked after the response
		opened = append(opened, accepts.n.Load())
	}
	if opened[0] == 0 || opened[1] != opened[0] {
		t.Errorf("connections accepted after each request: %v (the second request should reuse the first's)", opened)
	}
}

// TestMissingOptionalModule: a require() of a package that is not
// installed bundles as a module that throws Node's MODULE_NOT_FOUND when
// required (Next leaves pg external; Node loads it from node_modules and its
// optional require fails only when reached): pg's lazy pg.native catches
// exactly that code and answers null, as under `next start`.
func TestMissingOptionalModule(t *testing.T) {
	srv := serveORMFixture(t)
	if status, body := getBody(t, srv.URL+"/api/drivers/pg-native"); status != 200 || body != `{"native":null}` {
		t.Errorf("GET /api/drivers/pg-native: %d %s", status, body)
	}
}

// TestTracedFiles: the project files a Next app reads at run time are
// packaged as `output: "standalone"` would ship them (Next's file tracing
// over og's server bundle, plus outputFileTracingIncludes: the same four
// files next build put in .next/standalone for this project), mounted at
// the runtime's cwd under an in-memory layer that takes writes (shared by
// requests), with SQLite database files in it too. The answers are what
// `next start` on Node 24 gives for the same requests on a fresh copy of
// the project.
func TestTracedFiles(t *testing.T) {
	t.Setenv("MIGRATE_DB_FILE", "")
	os.Unsetenv("MIGRATE_DB_FILE")
	dir := appDir("next-orm")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-orm)")
	}
	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range out.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	if got := strings.Join(names, " "); got != "content/hello.md drizzle/0000_init.sql drizzle/meta/_journal.json package.json" {
		t.Errorf("traced files: %s", got)
	}
	fsys, err := packageFixture(out)
	if err != nil {
		t.Fatal(err)
	}
	h, _, err := app.Handler(fsys, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	for _, c := range []struct{ method, path, want string }{
		{"GET", "/api/content", `{"exists":true,"text":"# Hello\n\nRead at request time.\n"}`},
		{"GET", "/api/files", `{"uploads":null,"note":null,"hello":"# Hello","db":null}`},
		{"GET", "/api/migrate", `[{"id":1,"name":"from a migration"}]`},
		{"POST", "/api/files", `{"ok":true,"err":"ENOENT"}`},
		{"GET", "/api/files", `{"uploads":["b.txt"],"note":"one+two","hello":"# Changed","db":"SQLite format 3"}`},
		{"GET", "/api/content", `{"exists":true,"text":"# Changed\n"}`},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if got := strings.TrimSpace(string(b)); got != c.want {
			t.Errorf("%s %s:\n%s\nwant %s", c.method, c.path, got, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "migrate.db")); err == nil {
		t.Error("migrate.db was written to the disk")
	}
}

// TestTypeORM: TypeORM over the better-sqlite3 substitute, from a Next
// route: entities whose column types come from decorator metadata
// (tsconfig's emitDecoratorMetadata, transpiled by TypeScript-Go as tsc
// would; esbuild does not emit it), TypeORM resolved as on next start's
// Node runtime (not its browser build, which disables the drivers), its
// computed driver require() made static (patches.go), and its DATETIME
// columns read as stored (TypeORM makes the Dates). The answer is what
// `next start` on Node 24 gives (TypeORM in serverExternalPackages, as
// Next's docs advise).
func TestTypeORM(t *testing.T) {
	srv := serveORMFixture(t)
	want := `{"author":{"name":"Ursula","bio":null,"books":[["A Wizard of Earthsea",183,true,true],["The Dispossessed",387,false,true]]},"long":[{"title":"The Dispossessed","author":"Ursula"}],"rollback":"abort","count":2,"columns":["id:Number","title:String","pages:Number","available:Boolean","createdAt:datetime","author:Number"]}`
	if status, body := getBody(t, srv.URL+"/api/typeorm"); status != 200 || body != want {
		t.Errorf("GET /api/typeorm: %d\n%s\nwant %s", status, body, want)
	}
}

// TestPrisma: Prisma 7 from a Next route. og's build runs the build
// script's `prisma generate` on its build host (codegen.go: Prisma's own
// CLI; no network, no native engine), and the route uses the generated
// TypeScript client (its WebAssembly query compiler) over the better-sqlite3
// driver adapter and og's substitute: nested writes, relations, counts,
// aggregates, an interactive transaction rolled back, a unique violation
// as P2002, raw SQL. The answer is what `next start` on Node 24 gives after
// `npm run build`.
func TestPrisma(t *testing.T) {
	dir := appDir("next-orm")
	if err := os.RemoveAll(filepath.Join(dir, "generated")); err != nil { // the build makes it again
		t.Fatal(err)
	}
	srv := serveORMFixture(t)
	if _, err := os.Stat(filepath.Join(dir, "generated/prisma/client.ts")); err != nil {
		t.Errorf("prisma generate did not run: %v", err)
	}
	want := `{"alice":{"name":"Alice","posts":[["Hello",true,3],["Draft",false,0]],"created":true},"published":[{"title":"Hello","author":{"email":"alice@example.com"}}],"counts":[{"email":"alice@example.com","_count":{"posts":2}},{"email":"bob@example.com","_count":{"posts":0}}],"agg":{"_sum":{"views":3},"_count":2},"rollback":"abort","unique":"P2002","posts":2}`
	if status, body := getBody(t, srv.URL+"/api/prisma"); status != 200 || body != want {
		t.Errorf("GET /api/prisma: %d\n%.1500s\nwant %s", status, body, want)
	}
}
