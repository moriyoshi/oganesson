package next

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/app"
)

// TestPrismaDbPush: a Prisma (sqlite) project with no migrations whose build
// script runs `prisma db push`. og records that and, at app startup, pushes the
// schema (schemaPush) — diffing it against the empty database and creating the
// Widget table through the WASM schema engine over the better-sqlite3 adapter.
// The /api/widgets route then inserts and reads a row; without the startup push
// the table would not exist.
func TestPrismaDbPush(t *testing.T) {
	dir := appDir("next-prisma-push")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed")
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules/@prisma/schema-engine-wasm")); err != nil {
		t.Skip("@prisma/schema-engine-wasm not installed (og install adds it)")
	}
	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if out.PrismaMigrate == nil || !out.PrismaMigrate.Push {
		t.Fatalf("build: PrismaMigrate = %+v; want Push=true", out.PrismaMigrate)
	}
	fsys, err := packageFixture(out)
	if err != nil {
		t.Fatal(err)
	}
	h, meta, err := app.Handler(fsys, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Close != nil {
		t.Cleanup(meta.Close)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	want := `{"widgets":[{"id":1,"name":"pushed"}]}`
	if status, body := getBody(t, srv.URL+"/api/widgets"); status != 200 || body != want {
		t.Errorf("GET /api/widgets: %d\n%s\nwant %s", status, body, want)
	}
}
