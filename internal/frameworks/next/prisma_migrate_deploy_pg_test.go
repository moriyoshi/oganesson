//go:build aot || wazero

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

// TestPrismaMigrateDeployPostgres: a Prisma (PostgreSQL) project whose migrate
// datasource is env("DATABASE_URL") in prisma.config.ts — no literal URL, since
// @prisma/adapter-pg supplies the client connection at runtime. The build
// captures the variable name (meta.PrismaMigrate.urlEnv), and og's app-startup
// `migrate deploy` resolves DATABASE_URL at startup and applies the migrations
// through the WASM schema engine over the native pg shim (against PGlite). The
// /api/widgets route then reads the migrated, seeded table.
func TestPrismaMigrateDeployPostgres(t *testing.T) {
	// Prisma (PostgreSQL) migrate deploy against single-session PGlite: the
	// startup migrator disables the advisory lock so one connection suffices,
	// the pg shim array-encodes the schema-list text[] parameter, and it honors
	// the query's getTypeParser so adapter-pg's custom temporal/numeric parsers
	// run (the engine reads _prisma_migrations as strings, not Date objects).
	dir := appDir("next-prisma-migrate-pg")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed")
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules/@prisma/schema-engine-wasm")); err != nil {
		t.Skip("@prisma/schema-engine-wasm not installed (og install adds it)")
	}
	t.Setenv("DATABASE_URL", startPostgres(t))

	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if out.PrismaMigrate == nil || out.PrismaMigrate.URLEnv != "DATABASE_URL" {
		t.Fatalf("build: PrismaMigrate = %+v; want URLEnv=DATABASE_URL", out.PrismaMigrate)
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
