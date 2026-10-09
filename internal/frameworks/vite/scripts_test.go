package vite

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestBuildInvocations: the build script's `vite build` commands, through
// npm / pnpm / yarn run chains and environment prefixes; other commands
// (tsc) are skipped.
func TestBuildInvocations(t *testing.T) {
	pkg := packageJSON{Scripts: map[string]string{
		"build":        "tsc -b && npm run build:client && pnpm build:server; yarn run build:extra",
		"build:client": "vite build --outDir dist/client",
		"build:server": "cross-env NODE_ENV=production vite build --ssr src/entry-server.jsx --outDir 'dist/server'",
		"build:extra":  "npx vite build -c other.config.ts --mode staging",
	}}
	got := buildInvocations(pkg)
	want := []Invocation{
		{Args: []string{"--outDir", "dist/client"}},
		{Args: []string{"--ssr", "src/entry-server.jsx", "--outDir", "dist/server"}},
		{Args: []string{"-c", "other.config.ts", "--mode", "staging"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got := buildInvocations(packageJSON{Scripts: map[string]string{"build": "next build"}}); got != nil {
		t.Errorf("no vite build: %+v", got)
	}
}

// TestCLIConfig: `vite build` flags as Vite's CLI passes them to
// createBuilder.
func TestCLIConfig(t *testing.T) {
	root, mode, config, inline := cliConfig([]string{"app", "--ssr", "src/entry.js", "--outDir=dist/server", "--sourcemap", "--minify", "false", "-m", "staging", "--base", "/x/", "-c", "v.config.ts", "--emptyOutDir"})
	if root != "app" || mode != "staging" || config != "v.config.ts" {
		t.Errorf("root %q mode %q config %q", root, mode, config)
	}
	want := map[string]any{
		"base":  "/x/",
		"build": map[string]any{"ssr": "src/entry.js", "outDir": "dist/server", "sourcemap": true, "minify": false, "emptyOutDir": true},
	}
	if !reflect.DeepEqual(inline, want) {
		t.Errorf("inline %#v", inline)
	}
	if _, _, _, inline := cliConfig([]string{"--ssr"}); !reflect.DeepEqual(inline, map[string]any{"build": map[string]any{"ssr": true}}) {
		t.Errorf("--ssr alone: %#v", inline)
	}
}

// TestServerProgram: the node program of the preview / start / serve
// script, resolved as node resolves it, with its environment.
func TestServerProgram(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "server.js", "")
	pkg := packageJSON{Scripts: map[string]string{"preview": "cross-env NODE_ENV=production PORT=4000 node --enable-source-maps server"}}
	p := serverProgram(dir, pkg)
	if p == nil || p.File != dir+"/server.js" || !reflect.DeepEqual(p.Env, map[string]string{"NODE_ENV": "production", "PORT": "4000"}) {
		t.Fatalf("got %+v", p)
	}
	if p := serverProgram(dir, packageJSON{Scripts: map[string]string{"preview": "vite preview"}}); p != nil {
		t.Errorf("vite preview is not a server program: %+v", p)
	}
	if p := serverProgram(dir, packageJSON{Scripts: map[string]string{"start": "node server.js --port 1 a"}}); p == nil || !reflect.DeepEqual(p.Args, []string{"--port", "1", "a"}) {
		t.Errorf("program arguments: %+v", p)
	}
	// A dependency's bin, by name (string bins are named after the
	// package) and with its arguments.
	for _, f := range []struct{ name, content string }{
		{"node_modules/@scope/serve/package.json", `{"name":"@scope/serve","bin":{"scope-serve":"bin.cjs"}}`},
		{"node_modules/@scope/serve/bin.cjs", ""},
		{"node_modules/single/package.json", `{"name":"single","bin":"cli.js"}`},
		{"node_modules/single/cli.js", ""},
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, f.name)), 0o755)
		writeFile(t, dir, f.name, f.content)
	}
	deps := map[string]string{"@scope/serve": "1", "single": "1"}
	p = serverProgram(dir, packageJSON{Dependencies: deps, Scripts: map[string]string{"start": "PORT=1 scope-serve ./build/server/index.js"}})
	if p == nil || p.File != filepath.Join(dir, "node_modules/@scope/serve/bin.cjs") || !reflect.DeepEqual(p.Args, []string{"./build/server/index.js"}) || p.Env["PORT"] != "1" {
		t.Errorf("scoped bin: %+v", p)
	}
	if p := serverProgram(dir, packageJSON{Dependencies: deps, Scripts: map[string]string{"serve": "single"}}); p == nil || p.File != filepath.Join(dir, "node_modules/single/cli.js") {
		t.Errorf("string bin: %+v", p)
	}
	if p := serverProgram(dir, packageJSON{DevDependencies: deps, Scripts: map[string]string{"serve": "single"}}); p != nil {
		t.Errorf("a devDependency's bin is not a server program: %+v", p)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFindServerProgramPrefersNode: SolidStart's template lists vite under
// dependencies and has both "preview": "vite preview" and "start": "node
// .output/server/index.mjs"; the node program is the server, and vite's
// bin (og implements vite preview itself) never is.
func TestFindServerProgramPrefersNode(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, s string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("node_modules/vite/package.json", `{"bin":{"vite":"bin/vite.js"}}`)
	write("node_modules/vite/bin/vite.js", "")
	write(".output/server/index.mjs", "")
	pkg := packageJSON{
		Scripts:      map[string]string{"preview": "vite preview", "start": "node .output/server/index.mjs"},
		Dependencies: map[string]string{"vite": "8.3.3"},
	}
	p := findServerProgram(dir, pkg, true)
	if p == nil || p.File != filepath.Join(dir, ".output/server/index.mjs") {
		t.Fatalf("got %+v, want the node program", p)
	}
	// Any bin listed earlier loses to a node program too.
	write("node_modules/srv/package.json", `{"bin":"cli.js"}`)
	write("node_modules/srv/cli.js", "")
	pkg.Dependencies["srv"] = "1.0.0"
	pkg.Scripts["preview"] = "srv dist"
	if p := findServerProgram(dir, pkg, true); p == nil || p.File != filepath.Join(dir, ".output/server/index.mjs") {
		t.Fatalf("got %+v, want the node program over a bin", p)
	}
	delete(pkg.Dependencies, "srv")
	pkg.Scripts["preview"] = "vite preview"
	delete(pkg.Scripts, "start")
	if p := findServerProgram(dir, pkg, true); p != nil {
		t.Fatalf("vite preview became a server program: %+v", p)
	}
}

// TestQwikBuildInvocations: `qwik build` runs prebuild.*, build.client,
// build.server (build.preview for `qwik build preview`), build.static,
// postbuild.*; `qwik check-client` adds nothing after build.client.
func TestQwikBuildInvocations(t *testing.T) {
	pkg := packageJSON{Scripts: map[string]string{
		"build":           "qwik build",
		"build.client":    "vite build",
		"build.preview":   "vite build --ssr src/entry.preview.tsx",
		"build.server":    "qwik check-client src dist && vite build -c adapters/node-server/vite.config.ts",
		"build.static":    "vite build -c adapters/static/vite.config.ts",
		"build.types":     "tsc --incremental --noEmit",
		"prebuild.assets": "vite build -c prebuild.config.ts",
	}}
	want := []Invocation{
		{Args: []string{"-c", "prebuild.config.ts"}},
		{Args: []string{}},
		{Args: []string{"-c", "adapters/node-server/vite.config.ts"}},
		{Args: []string{"-c", "adapters/static/vite.config.ts"}},
	}
	if got := buildInvocations(pkg); !reflect.DeepEqual(got, want) {
		t.Errorf("qwik build: got %+v, want %+v", got, want)
	}
	pkg.Scripts["build"] = "qwik build preview"
	want[2] = Invocation{Args: []string{"--ssr", "src/entry.preview.tsx"}}
	if got := buildInvocations(pkg); !reflect.DeepEqual(got, want) {
		t.Errorf("qwik build preview: got %+v, want %+v", got, want)
	}
}
