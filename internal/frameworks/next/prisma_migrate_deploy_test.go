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

// TestPrismaMigrateDeploy: a Prisma (sqlite) project with prisma/migrations and
// no runtime DDL. og applies `migrate deploy` at app startup through the WASM
// schema engine over the better-sqlite3 adapter; the /api/widgets route then
// reads the migrated, seeded table through Prisma. If startup migration did not
// run, the table would not exist and the route would 500.
func TestPrismaMigrateDeploy(t *testing.T) {
	dir := appDir("next-prisma-migrate")
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
	if out.PrismaMigrate == nil {
		t.Fatal("build did not detect prisma migrate deploy")
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

	want := `{"widgets":[{"id":1,"name":"alpha"},{"id":2,"name":"beta"}]}`
	if status, body := getBody(t, srv.URL+"/api/widgets"); status != 200 || body != want {
		t.Errorf("GET /api/widgets: %d\n%s\nwant %s", status, body, want)
	}
}
