package next

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/runtime/app"
)

// Packaged paths (under Meta.Files) for the app-startup migrate deploy.
const (
	prismaMigrationsDst = "prisma/migrations"
	prismaWasmDst       = "prisma/.og/schema_engine_bg.wasm"
	prismaMigratorDst   = "prisma/.og/migrate.js"
	prismaSchemaDst     = "prisma/.og/schema.prisma" // packaged schema for db push
)

// prismaAdapter maps a datasource provider to the driver adapter the migrator
// bundles and how it builds the factory from the connection URL.
type prismaAdapter struct {
	pkg, imp, factory string
}

var prismaAdapters = map[string]prismaAdapter{
	"sqlite": {"@prisma/adapter-better-sqlite3", "PrismaBetterSqlite3", "new __Adapter({ url })"},
	// pg: default pool (not max:1) — the schema engine holds an advisory-lock
	// connection while migrating on another, so it needs more than one. (This
	// is why single-session PGlite cannot serve it; a real Postgres can.)
	"postgresql": {"@prisma/adapter-pg", "PrismaPg", "new __Adapter({ connectionString: url })"},
	"postgres":   {"@prisma/adapter-pg", "PrismaPg", "new __Adapter({ connectionString: url })"},
}

// prismaMigrate prepares a Prisma project's app-startup schema sync: `migrate
// deploy` (apply the packaged migrations) when prisma/migrations exists, or
// `db push` (apply the schema directly) when the build script runs `prisma db
// push` (b.prismaPush). It packages the inputs + schema_engine_bg.wasm + a
// bundled migrator program and returns the Meta.PrismaMigrate describing them.
// Returns (nil, nil, nil) when the project does neither.
func (b *builder) prismaMigrate(ctx context.Context, log func(string, ...any)) (*app.PrismaMigrate, map[string][]byte, error) {
	migRoot := filepath.Join(b.dir, "prisma", "migrations")
	dirs, _ := migrationDirs(migRoot)
	push := b.prismaPush != nil // an explicit `prisma db push` wins over migrations
	if !push && len(dirs) == 0 {
		return nil, nil, nil
	}
	schema, url, urlEnv, err := loadPrismaConfig(ctx, b.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("prisma.config: %w", err)
	}
	provider, err := prismaProvider(filepath.Join(b.dir, schema))
	if err != nil {
		return nil, nil, err
	}
	ad, ok := prismaAdapters[provider]
	if !ok {
		return nil, nil, fmt.Errorf("prisma migrate: unsupported datasource provider %q", provider)
	}
	if url == "" && urlEnv == "" {
		return nil, nil, fmt.Errorf("prisma migrate: no datasource for %s; set datasource.url in prisma.config.ts (a literal, or env(\"VAR\"))", provider)
	}
	// The schema engine ships with @prisma/schema-engine-wasm (added to the
	// project implicitly by og install when Prisma is used).
	wasm, err := os.ReadFile(filepath.Join(b.dir, "node_modules", "@prisma", "schema-engine-wasm", "schema_engine_bg.wasm"))
	if err != nil {
		return nil, nil, fmt.Errorf("prisma migrate: @prisma/schema-engine-wasm not installed (og install adds it for Prisma projects): %w", err)
	}
	code, err := b.bundleMigrator(ad)
	if err != nil {
		return nil, nil, fmt.Errorf("prisma migrate: bundling migrator: %w", err)
	}

	files := map[string][]byte{prismaWasmDst: wasm, prismaMigratorDst: []byte(code)}
	pm := &app.PrismaMigrate{Provider: provider, URL: url, URLEnv: urlEnv, Program: prismaMigratorDst, Wasm: prismaWasmDst}

	if push {
		// db push needs the schema itself (schemaPush diffs it against the live
		// database); no migration files.
		sb, err := os.ReadFile(filepath.Join(b.dir, schema))
		if err != nil {
			return nil, nil, fmt.Errorf("prisma db push: reading schema %s: %w", schema, err)
		}
		files[prismaSchemaDst] = sb
		pm.Push = true
		pm.AcceptDataLoss = b.prismaPush.acceptDataLoss
		pm.Schema = prismaSchemaDst
		log("prisma db push: provider %s (acceptDataLoss=%t)", provider, pm.AcceptDataLoss)
		return pm, files, nil
	}

	for _, d := range dirs {
		if sql, err := os.ReadFile(filepath.Join(migRoot, d, "migration.sql")); err == nil {
			files[prismaMigrationsDst+"/"+d+"/migration.sql"] = sql
		}
	}
	if lock, err := os.ReadFile(filepath.Join(migRoot, "migration_lock.toml")); err == nil {
		files[prismaMigrationsDst+"/migration_lock.toml"] = lock
	}
	pm.Dir = prismaMigrationsDst
	log("prisma migrate deploy: %d migration(s), provider %s", len(dirs), provider)
	return pm, files, nil
}

// migrationDirs lists the migration subdirectories (those with a migration.sql),
// sorted lexically — the order Prisma applies them in.
func migrationDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "migration.sql")); err == nil {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

var prismaProviderRE = regexp.MustCompile(`(?s)datasource\s+\w+\s*\{[^}]*?provider\s*=\s*"([^"]+)"`)

// prismaProvider reads the datasource provider from a .prisma schema file.
func prismaProvider(schemaFile string) (string, error) {
	b, err := os.ReadFile(schemaFile)
	if err != nil {
		return "", fmt.Errorf("prisma migrate: reading schema %s: %w", schemaFile, err)
	}
	m := prismaProviderRE.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("prisma migrate: no datasource provider in %s", schemaFile)
	}
	return string(m[1]), nil
}

// loadPrismaConfig evaluates the project's prisma.config.* on the build host and
// returns the schema path (default prisma/schema.prisma) and the static
// datasource URL, if the config gives one (sqlite does; the pg/adapter case does
// not, and the URL is supplied at runtime from the environment — a follow-up).
func loadPrismaConfig(ctx context.Context, dir string) (schema, url, urlEnv string, err error) {
	schema = "prisma/schema.prisma"
	cfgFile := findPrismaConfig(dir)
	if cfgFile == "" {
		return schema, "", "", nil
	}
	// The config is dynamically imported so the process.env Proxy below is
	// installed before it evaluates. The Proxy returns a sentinel for EVERY
	// variable read during the config's evaluation — even ones set in the
	// environment — so a datasource.url built from Prisma's env("X") helper
	// (which reads process.env[X] and throws when falsy) comes out as
	// "__OG_ENV__X__": a runtime env reference (the pg/adapter case) whose
	// variable name we record as urlEnv to resolve at startup, rather than
	// baking in whatever X happens to be at build time. (This eval is only to
	// read the migrate datasource; `prisma generate` loads the config itself
	// with the real environment, so its env() calls are unaffected.)
	entry := fmt.Sprintf(`
const __realEnv = process.env;
const __proxy = new Proxy(__realEnv, { get(t, k) { return typeof k === "string" ? ("__OG_ENV__" + k + "__") : t[k]; } });
Object.defineProperty(process, "env", { value: __proxy, configurable: true });
let user;
try { user = await import(%s); } finally { Object.defineProperty(process, "env", { value: __realEnv, configurable: true }); }
let c = ("default" in user ? user.default : user);
if (typeof c === "function") c = await c();
c = (await c) ?? {};
let __u = (c.datasource && c.datasource.url) ?? null, __ue = null;
if (typeof __u === "string") { const __m = __u.match(/^__OG_ENV__(.+)__$/); if (__m) { __ue = __m[1]; __u = null; } }
globalThis.__ogPrismaCfg = JSON.stringify({ schema: c.schema ?? null, url: __u, urlEnv: __ue });
`, jsonStr(cfgFile))
	h := &jshost.Host{Dir: dir, Out: io.Discard, TopLevelAwait: true}
	code, err := h.Bundle("", entry, "")
	if err != nil {
		return "", "", "", err
	}
	v, err := h.Eval(ctx, code, `globalThis.__ogPrismaCfg`)
	if err != nil {
		return "", "", "", err
	}
	s, _ := v.(string)
	var got struct {
		Schema *string `json:"schema"`
		URL    *string `json:"url"`
		URLEnv *string `json:"urlEnv"`
	}
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		return "", "", "", fmt.Errorf("prisma.config result: %w", err)
	}
	if got.Schema != nil && *got.Schema != "" {
		schema = *got.Schema
	}
	if got.URL != nil {
		url = *got.URL
	}
	if got.URLEnv != nil {
		urlEnv = *got.URLEnv
	}
	return schema, url, urlEnv, nil
}

func findPrismaConfig(dir string) string {
	for _, name := range []string{"prisma.config.ts", "prisma.config.mts", "prisma.config.js", "prisma.config.mjs"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// bundleMigrator bundles og's migrator entry (over the provider's driver
// adapter) into a self-contained script. At runtime it reads
// globalThis.__ogPrismaMigrate ({ dir, url, wasm }), instantiates the schema
// engine, and applies the packaged migrations through the adapter.
func (b *builder) bundleMigrator(ad prismaAdapter) (string, error) {
	entry := fmt.Sprintf(prismaMigratorEntry, jsonStr(ad.pkg), ad.imp, ad.factory)
	h := &jshost.Host{Dir: b.dir, Out: io.Discard}
	return h.Bundle("", entry, "")
}

// prismaMigratorEntry is the migrator program (as esbuild stdin). %s are the
// adapter package specifier, its export name, and the factory expression. It
// assigns a promise to globalThis.__ogMigrateDone, which the runtime awaits.
const prismaMigratorEntry = `
import { %[2]s as __Adapter } from %[1]s;
import * as __glue from "@prisma/schema-engine-wasm/schema_engine_bg";
import { SchemaEngine } from "@prisma/schema-engine-wasm/schema_engine_bg";
import { bindMigrationAwareSqlAdapterFactory } from "@prisma/driver-adapter-utils";
import { readFileSync, readdirSync } from "node:fs";

let __adapter;
globalThis.__ogMigrateDone = (async () => {
 try {
  const cfg = globalThis.__ogPrismaMigrate;
  const inst = new WebAssembly.Instance(new WebAssembly.Module(readFileSync(cfg.wasm)), { "./schema_engine_bg.js": __glue });
  __glue.__wbg_set_wasm(inst.exports);
  if (typeof inst.exports.__wbindgen_start === "function") inst.exports.__wbindgen_start();

  const url = cfg.url;
  const factory = %[3]s;
  __adapter = bindMigrationAwareSqlAdapterFactory(factory);
  const adapter = __adapter;
  const filters = { externalTables: [], externalEnums: [] };
  const engine = await SchemaEngine.new({ datamodels: undefined }, () => {}, adapter);

  let result;
  if (cfg.push) {
    // db push: diff the packaged schema against the live database and apply it.
    const schema = { files: [{ path: "schema.prisma", content: readFileSync(cfg.schema, "utf8") }] };
    const out = await engine.schemaPush({ force: !!cfg.acceptDataLoss, schema, filters });
    result = { push: true, executedSteps: out.executedSteps, warnings: out.warnings || [], unexecutable: out.unexecutable || [] };
  } else {
    // migrate deploy: apply the packaged migrations in name order. Each
    // subdirectory with a migration.sql is one migration.
    const migrationDirectories = [];
    for (const name of readdirSync(cfg.dir).sort()) {
      let sql;
      try {
        sql = readFileSync(cfg.dir + "/" + name + "/migration.sql", "utf8");
      } catch {
        continue; // not a migration directory (e.g. migration_lock.toml)
      }
      migrationDirectories.push({ path: name, migrationFile: { path: "migration.sql", content: { tag: "ok", value: sql } } });
    }
    let lock = null;
    try {
      lock = readFileSync(cfg.dir + "/migration_lock.toml", "utf8");
    } catch {}
    const migrationsList = { baseDir: cfg.dir, lockfile: { path: "migration_lock.toml", content: lock }, shadowDbInitScript: "", migrationDirectories };
    const out = await engine.applyMigrations({ migrationsList, filters });
    result = { applied: out.appliedMigrationNames || [] };
  }
  if (typeof engine.free === "function") engine.free();
  return result;
 } catch (e) {
  let detail = (e && (e.message)) || String(e);
  // The schema engine wraps a driver-adapter failure as "External error id#N";
  // the real error is in the adapter's error registry under that id. Fold it
  // into the thrown error so it reaches the Go side (the opaque wrapper alone
  // is useless).
  try {
    const m = /External error id#(\d+)/.exec(detail);
    if (m && __adapter && __adapter.errorRegistry) {
      const rec = __adapter.errorRegistry.consumeError(Number(m[1]));
      const real = rec && rec.error;
      if (real) detail += " | cause: " + ((real.message || real.stack) || JSON.stringify(real));
    }
  } catch (e2) {
    detail += " | (registry read failed: " + e2 + ")";
  }
  throw new Error("og prisma migrate: " + detail);
 }
})();
`
