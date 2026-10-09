package next

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/app"
)

// TestORMsOnSQLite: Drizzle (the SQL-like builder, the relational query
// API, RETURNING, transactions and their rollback) and Kysely (SqliteDialect)
// over better-sqlite3, which og substitutes (runtime/natives), in
// testdata/apps/next-orm. Each request opens the database file in its own
// VM; the file carries the data between them. The expectations are what
// `next start` on Node 24 answers to the same requests (2026-10-08).
func TestORMsOnSQLite(t *testing.T) {
	dir := appDir("next-orm")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-orm)")
	}
	t.Setenv("DB_FILE", filepath.Join(t.TempDir(), "notes.db"))
	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
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
	t.Cleanup(srv.Close)
	do := func(method, path, body string) string {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return strings.TrimSpace(string(b))
	}
	for _, c := range []struct{ method, body, want string }{
		{"POST", `{"title":"first","body":"hello"}`, `{"id":1,"title":"first","done":false}`},
		{"POST", `{"title":"second","done":true}`, `{"id":2,"title":"second","done":true}`},
		{"GET", "", `{"rows":[{"id":1,"title":"first","done":0,"len":5},{"id":2,"title":"second","done":1,"len":6}],"stats":{"n":2,"last":2}}`},
		{"PATCH", `{"id":1}`, `{"id":1,"done":true}`},
		{"DELETE", "", `{"rolledBack":true,"error":true}`},
	} {
		if got := do(c.method, "/api/notes", c.body); got != c.want {
			t.Errorf("%s /api/notes %s:\ngot  %s\nwant %s", c.method, c.body, got, c.want)
		}
	}
	page := do("GET", "/", "")
	want := `<main><h1>notes</h1><p id="pending">0<!-- --> open</p><ul id="drizzle"><li data-done="true" data-created="date">second</li><li data-done="true" data-created="date">first<!-- -->: hello</li></ul><p id="kysely">first, second</p>`
	if !strings.Contains(page, want) {
		t.Errorf("GET /:\n%.2000s", page)
	}
}
