// Package natives substitutes npm packages that load native addons, which
// neither og install nor Goja can run:
//
//   - bcrypt: server code gets bcryptjs (the pure-JS implementation with
//     the same API), which og install adds next to bcrypt.
//   - sharp: a built-in module (js/sharp.js) implementing sharp's common
//     pipeline on Go image code (runtime/imageopt).
//   - better-sqlite3: a built-in module (js/better-sqlite3.js) on SQLite
//     in Go (modernc.org/sqlite), shared across request engines.
//
// Registry is the single list; the installer, the server build and the
// runtime all read it.
package natives

import (
	_ "embed"
	"strconv"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// Substitute describes one native package.
type Substitute struct {
	Package string // the native package, e.g. "bcrypt"
	// Install is a package og install adds next to Package, with Range
	// ("" for Package's own version).
	Install, Range string
	// Alias is the package server code gets instead, resolved normally.
	Alias string
	// Module is a built-in module server code gets instead.
	Module string
}

// Registry lists the substitutes.
var Registry = []Substitute{
	{Package: "bcrypt", Install: "bcryptjs", Range: "^3.0.2", Alias: "bcryptjs"},
	{Package: "sharp", Module: "og:sharp"},
	{Package: "better-sqlite3", Module: "og:better-sqlite3"},
	// pg (node-postgres): a built-in module (js/pg.js) on PostgreSQL in Go
	// (jackc/pgx), its pool shared across request engines.
	{Package: "pg", Module: "og:pg"},
	// postgres (postgres.js): a built-in module (js/postgres.js) on the same
	// Go PostgreSQL service pg uses (Symbol.for("og.pg")); only the JS surface
	// (tagged templates) differs.
	{Package: "postgres", Module: "og:postgres"},
	// mysql2: built-in modules (js/mysql2.js callback API, js/mysql2-promise.js
	// promise API) on MySQL in Go (go-sql-driver), its pool shared across
	// request engines. The mysql2/promise subpath is served by Plugin directly.
	{Package: "mysql2", Module: "og:mysql2"},
	// @libsql/client (local file:/:memory: only): a built-in module
	// (js/libsql-client.js) on the same Go SQLite service better-sqlite3 uses
	// (Symbol.for("og.sqlite")); drizzle-orm/libsql drives it.
	{Package: "@libsql/client", Module: "og:libsql-client"},
	// Build-time only: Next's PostCSS (Tailwind v4) loads lightningcss,
	// whose WebAssembly build internal/jshost uses instead.
	{Package: "lightningcss", Install: "lightningcss-wasm"},
	// Build-time only: Rollup 4 (e.g. workbox-build, which vite-plugin-pwa
	// runs) loads its native binding; its official WebAssembly build has
	// the same native.js API.
	{Package: "rollup", Install: "@rollup/wasm-node"},
}

func lookup(pkg string) (Substitute, bool) {
	for _, s := range Registry {
		if s.Package == pkg {
			return s, true
		}
	}
	return Substitute{}, false
}

// InstallFor returns the package og install adds for pkg@version.
func InstallFor(pkg, version string) (name, rng string, ok bool) {
	s, ok := lookup(pkg)
	if !ok || s.Install == "" {
		return "", "", false
	}
	if s.Range == "" {
		return s.Install, version, true
	}
	return s.Install, s.Range, true
}

// Alias rewrites an import specifier of server code to its substitute
// package ("bcrypt" -> "bcryptjs"); subpaths are left alone.
func Alias(spec string) (string, bool) {
	s, ok := lookup(spec)
	if !ok || s.Alias == "" {
		return spec, false
	}
	return s.Alias, true
}

//go:embed js/sharp.js
var sharpSource string

//go:embed js/better-sqlite3.js
var sqliteSource string

//go:embed js/pg.js
var pgSource string

//go:embed js/postgres.js
var postgresSource string

//go:embed js/mysql2.js
var mysql2Source string

//go:embed js/mysql2-promise.js
var mysql2PromiseSource string

//go:embed js/libsql-client.js
var libsqlClientSource string

// Plugin serves the built-in substitutes for server builds: imports of the
// native packages resolve to them.
func Plugin() api.Plugin {
	sources := map[string]string{"sharp": sharpSource, "better-sqlite3": sqliteSource, "pg": pgSource, "postgres": postgresSource, "mysql2": mysql2Source, "mysql2/promise": mysql2PromiseSource, "libsql-client": libsqlClientSource}
	return api.Plugin{Name: "og-natives", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `^(sharp|better-sqlite3|pg|postgres|mysql2|mysql2/promise|@libsql/client|og:sharp|og:better-sqlite3|og:pg|og:postgres|og:mysql2|og:mysql2/promise|og:libsql-client)$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			name := a.Path
			if s, ok := lookup(name); ok {
				name = s.Module
			}
			// require() gets the packages' CommonJS shape: module.exports
			// is the default export (sharp's function, better-sqlite3's
			// Database), as the real packages export them (TypeORM
			// requires better-sqlite3 and constructs it).
			if a.Kind == api.ResolveJSRequireCall && a.Namespace != "og-natives" {
				return api.OnResolveResult{Path: name + "#cjs", Namespace: "og-natives"}, nil
			}
			return api.OnResolveResult{Path: name, Namespace: "og-natives"}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "og-natives"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			if name, ok := strings.CutSuffix(a.Path, "#cjs"); ok {
				src := "const m = require(" + strconv.Quote(name) + ");\n" +
					"module.exports = m.default;\n" +
					"for (const k of Object.keys(m)) if (k !== \"default\") module.exports[k] = m[k];\n"
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
			}
			src := sources[strings.TrimPrefix(a.Path, "og:")]
			return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
		})
	}}
}

// ModuleSource is the source of a built-in substitute ("sharp",
// "better-sqlite3"): an ES module.
func ModuleSource(pkg string) (string, bool) {
	switch pkg {
	case "sharp":
		return sharpSource, true
	case "better-sqlite3":
		return sqliteSource, true
	case "pg":
		return pgSource, true
	case "postgres":
		return postgresSource, true
	case "mysql2":
		return mysql2Source, true
	case "mysql2/promise":
		return mysql2PromiseSource, true
	case "@libsql/client":
		return libsqlClientSource, true
	}
	return "", false
}

// Services are the process-wide state of the substitutes.
type Services struct {
	SQLite   *SQLite
	Postgres *Postgres
	MySQL    *MySQL
}

// NewServices returns fresh services.
func NewServices() *Services {
	return &Services{SQLite: NewSQLite(), Postgres: NewPostgres(), MySQL: NewMySQL()}
}

// Install exposes the substitutes' Go side to JS in e
// (Symbol.for("og.sharp"), Symbol.for("og.sqlite")).
func Install(e *engine.Engine, svc *Services) error {
	vm := e.VM
	sharpHost, err := installSharp(vm)
	if err != nil {
		return err
	}
	sqliteHost, closeAll := installSQLite(vm, svc.SQLite)
	e.OnClose(closeAll)
	pgHost, pgCleanup := installPG(e, svc.Postgres)
	e.OnClose(pgCleanup)
	mysqlHost, mysqlCleanup := installMySQL(e, svc.MySQL)
	e.OnClose(mysqlCleanup)
	define, err := vm.RunString(`(function (sharp, sqlite, pg, mysql) {
  Object.defineProperty(globalThis, Symbol.for("og.sharp"), { value: sharp });
  Object.defineProperty(globalThis, Symbol.for("og.sqlite"), { value: sqlite });
  Object.defineProperty(globalThis, Symbol.for("og.pg"), { value: pg });
  Object.defineProperty(globalThis, Symbol.for("og.mysql"), { value: mysql });
})`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(define)
	_, err = fn(sobek.Undefined(), sharpHost, sqliteHost, pgHost, mysqlHost)
	return err
}
