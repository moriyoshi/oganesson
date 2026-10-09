package next

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrismaProvider(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"sqlite":     `datasource db {  provider = "sqlite"  }`,
		"postgresql": "datasource db {\n  provider = \"postgresql\"\n  url = env(\"X\")\n}\nmodel A { id Int @id }",
	}
	for want, src := range cases {
		f := filepath.Join(dir, want+".prisma")
		if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := prismaProvider(f)
		if err != nil || got != want {
			t.Errorf("prismaProvider(%q) = %q, %v; want %q", want, got, err, want)
		}
	}
	// No datasource: an error, not a silent empty provider.
	f := filepath.Join(dir, "none.prisma")
	_ = os.WriteFile(f, []byte("model A { id Int @id }"), 0o644)
	if _, err := prismaProvider(f); err == nil {
		t.Error("prismaProvider on a schema without a datasource: want error")
	}
}

func TestPrismaMigrationDirs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"20240102_b", "20240101_a"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
		_ = os.WriteFile(filepath.Join(root, d, "migration.sql"), []byte("SELECT 1;"), 0o644)
	}
	_ = os.MkdirAll(filepath.Join(root, "empty"), 0o755) // no migration.sql: skipped
	_ = os.WriteFile(filepath.Join(root, "migration_lock.toml"), []byte("provider = \"sqlite\"\n"), 0o644)
	dirs, err := migrationDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(dirs, ","); got != "20240101_a,20240102_b" {
		t.Errorf("migrationDirs = %q, want sorted init dirs", got)
	}
}

// loadPrismaConfig evaluates the fixture's prisma.config.ts (which imports
// prisma/config) and returns its schema + static sqlite datasource url.
func TestLoadPrismaConfig(t *testing.T) {
	dir := appDir("next-orm")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/prisma")); err != nil {
		t.Skip("fixture not installed")
	}
	schema, url, urlEnv, err := loadPrismaConfig(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if schema != "prisma/schema.prisma" {
		t.Errorf("schema = %q, want prisma/schema.prisma", schema)
	}
	if url != "file:./prisma.db" || urlEnv != "" {
		t.Errorf("url=%q urlEnv=%q, want file:./prisma.db and empty urlEnv", url, urlEnv)
	}
}

// A pg/adapter project's prisma.config.ts gives the migrate datasource as
// env("VAR") (the adapter supplies the client connection at runtime, so there
// is no literal url). loadPrismaConfig must capture the variable name, not the
// build-time value, so the runtime resolves it.
func TestLoadPrismaConfigEnv(t *testing.T) {
	fixture := appDir("next-orm")
	if _, err := os.Stat(filepath.Join(fixture, "node_modules/prisma")); err != nil {
		t.Skip("fixture not installed")
	}
	dir := t.TempDir()
	nm, _ := filepath.Abs(filepath.Join(fixture, "node_modules"))
	if err := os.Symlink(nm, filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	cfg := `import { defineConfig, env } from "prisma/config";
export default defineConfig({ schema: "prisma/schema.prisma", datasource: { url: env("DATABASE_URL") } });
`
	if err := os.WriteFile(filepath.Join(dir, "prisma.config.ts"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	// Even with DATABASE_URL set in the environment (as it is at build, so
	// prisma generate can load the config), loadPrismaConfig must capture the
	// variable name for runtime resolution, not bake in the build-time value.
	t.Setenv("DATABASE_URL", "postgres://build-time-should-not-be-used/")
	_, url, urlEnv, err := loadPrismaConfig(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if urlEnv != "DATABASE_URL" || url != "" {
		t.Errorf("url=%q urlEnv=%q, want empty url and urlEnv=DATABASE_URL", url, urlEnv)
	}
}

// bundleMigrator bundles og's migrator over the sqlite adapter into a
// self-contained script; it must resolve the app's adapter, the schema-engine
// glue, and driver-adapter-utils, and carry the applyMigrations call.
func TestBundleMigrator(t *testing.T) {
	dir := appDir("next-orm")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/@prisma/schema-engine-wasm")); err != nil {
		t.Skip("schema-engine-wasm not installed in fixture")
	}
	b := &builder{dir: dir}
	code, err := b.bundleMigrator(prismaAdapters["sqlite"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"__ogMigrateDone", "applyMigrations", "__ogPrismaMigrate"} {
		if !strings.Contains(code, want) {
			t.Errorf("bundled migrator missing %q", want)
		}
	}
	if len(code) < 1000 {
		t.Errorf("bundled migrator suspiciously small: %d bytes", len(code))
	}
}
