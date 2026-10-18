package install

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Registry state shared by the lockfile tests. Without a lockfile the
// highest matches win: a@1.2.0, shared@2.1.0 at the top and shared@1.5.0
// under a. Every lockfile below pins a@1.0.0, shared@2.0.0 and a's
// shared@1.0.0 instead.
var lockRegistry = []fakePkg{
	{name: "shared", version: "1.0.0"},
	{name: "shared", version: "1.5.0"},
	{name: "shared", version: "2.0.0"},
	{name: "shared", version: "2.1.0"},
	{name: "a", version: "1.0.0", deps: map[string]string{"shared": "^1.0.0"}},
	{name: "a", version: "1.2.0", deps: map[string]string{"shared": "^1.0.0"}},
	{name: "devtool", version: "1.0.0"},
	{name: "native-arm64", version: "1.0.0", cpu: []string{"arm64"}},
	{name: "thing-wasm32-wasi", version: "1.0.0", cpu: []string{"wasm32"}},
	{name: "b", version: "1.0.0", optional: map[string]string{"native-arm64": "1.0.0", "thing-wasm32-wasi": "1.0.0"}},
}

func sri(t *testing.T, name, version string) string {
	t.Helper()
	for _, p := range lockRegistry {
		if p.name == name && p.version == version {
			sum := sha512.Sum512(tarball(t, p))
			return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
		}
	}
	t.Fatalf("no fake package %s@%s", name, version)
	return ""
}

func lockProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["package.json"] = `{"name":"root","dependencies":{"a":"^1.0.0","shared":"^2.0.0","b":"1.0.0"},"devDependencies":{"devtool":"^1.0.0"}}`
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func checkPinned(t *testing.T, dir string, dev bool) {
	t.Helper()
	nm := filepath.Join(dir, "node_modules")
	want := map[string]string{
		"a": "1.0.0", "shared": "2.0.0", "a/node_modules/shared": "1.0.0", "b": "1.0.0",
		"thing-wasm32-wasi": "1.0.0", // optional WebAssembly build: kept
		"native-arm64":      "",      // optional native binary: skipped
		"devtool":           "",
	}
	if dev {
		want["devtool"] = "1.0.0"
	}
	for p, v := range want {
		if got := installedVersion(t, filepath.Join(nm, filepath.FromSlash(p))); got != v {
			t.Errorf("%s: installed %q, want %q", p, got, v)
		}
	}
}

func TestLockfiles(t *testing.T) {
	srv := fakeRegistry(t, lockRegistry)
	url := func(name, version string) string { return srv.URL + "/tarballs/" + name + "-" + version + ".tgz" }
	npmEntry := func(name, version, extra string) string {
		return `{"version":"` + version + `","resolved":"` + url(name, version) + `","integrity":"` + sri(t, name, version) + `"` + extra + `}`
	}
	npm := `{"name":"root","lockfileVersion":3,"requires":true,"packages":{
  "":{"name":"root","dependencies":{"a":"^1.0.0","shared":"^2.0.0","b":"1.0.0"},"devDependencies":{"devtool":"^1.0.0"}},
  "node_modules/a":` + npmEntry("a", "1.0.0", `,"dependencies":{"shared":"^1.0.0"}`) + `,
  "node_modules/a/node_modules/shared":` + npmEntry("shared", "1.0.0", "") + `,
  "node_modules/shared":` + npmEntry("shared", "2.0.0", "") + `,
  "node_modules/b":` + npmEntry("b", "1.0.0", `,"optionalDependencies":{"native-arm64":"1.0.0","thing-wasm32-wasi":"1.0.0"}`) + `,
  "node_modules/native-arm64":` + npmEntry("native-arm64", "1.0.0", `,"optional":true,"cpu":["arm64"]`) + `,
  "node_modules/thing-wasm32-wasi":` + npmEntry("thing-wasm32-wasi", "1.0.0", `,"optional":true,"cpu":["wasm32"]`) + `,
  "node_modules/devtool":` + npmEntry("devtool", "1.0.0", `,"dev":true`) + `
}}`
	bunEntry := func(name, version, meta string) string {
		return `["` + name + `@` + version + `", "` + url(name, version) + `", ` + meta + `, "` + sri(t, name, version) + `"],`
	}
	bun := `{
  "lockfileVersion": 1,
  "workspaces": {
    "": {
      "name": "root",
      "dependencies": { "a": "^1.0.0", "shared": "^2.0.0", "b": "1.0.0", },
      "devDependencies": { "devtool": "^1.0.0", },
    },
  },
  "packages": {
    "a": ` + bunEntry("a", "1.0.0", `{ "dependencies": { "shared": "^1.0.0" } }`) + `
    "a/shared": ` + bunEntry("shared", "1.0.0", `{}`) + `
    "shared": ` + bunEntry("shared", "2.0.0", `{}`) + `
    "b": ` + bunEntry("b", "1.0.0", `{ "optionalDependencies": { "native-arm64": "1.0.0", "thing-wasm32-wasi": "1.0.0" } }`) + `
    "native-arm64": ` + bunEntry("native-arm64", "1.0.0", `{ "cpu": "arm64" }`) + `
    "thing-wasm32-wasi": ` + bunEntry("thing-wasm32-wasi", "1.0.0", `{ "cpu": "wasm32" }`) + `
    "devtool": ` + bunEntry("devtool", "1.0.0", `{}`) + `
  }
}`
	pnpm := `lockfileVersion: '9.0'
importers:
  .:
    dependencies:
      a:
        specifier: ^1.0.0
        version: 1.0.0
      b:
        specifier: 1.0.0
        version: 1.0.0
      shared:
        specifier: ^2.0.0
        version: 2.0.0
    devDependencies:
      devtool:
        specifier: ^1.0.0
        version: 1.0.0
packages:
  a@1.0.0:
    resolution: {integrity: ` + sri(t, "a", "1.0.0") + `}
  b@1.0.0:
    resolution: {integrity: ` + sri(t, "b", "1.0.0") + `}
  shared@1.0.0:
    resolution: {integrity: ` + sri(t, "shared", "1.0.0") + `}
  shared@2.0.0:
    resolution: {integrity: ` + sri(t, "shared", "2.0.0") + `}
  devtool@1.0.0:
    resolution: {integrity: ` + sri(t, "devtool", "1.0.0") + `}
  native-arm64@1.0.0:
    resolution: {integrity: ` + sri(t, "native-arm64", "1.0.0") + `}
  thing-wasm32-wasi@1.0.0:
    resolution: {integrity: ` + sri(t, "thing-wasm32-wasi", "1.0.0") + `}
snapshots:
  a@1.0.0:
    dependencies:
      shared: 1.0.0
  b@1.0.0:
    optionalDependencies:
      native-arm64: 1.0.0
      thing-wasm32-wasi: 1.0.0
  shared@1.0.0: {}
  shared@2.0.0: {}
  devtool@1.0.0: {}
  native-arm64@1.0.0: {}
  thing-wasm32-wasi@1.0.0: {}
`
	yarn1 := `# THIS IS AN AUTOGENERATED FILE. DO NOT EDIT THIS FILE DIRECTLY.
# yarn lockfile v1


a@^1.0.0:
  version "1.0.0"
  resolved "` + url("a", "1.0.0") + `"
  integrity ` + sri(t, "a", "1.0.0") + `
  dependencies:
    shared "^1.0.0"

b@1.0.0:
  version "1.0.0"
  integrity ` + sri(t, "b", "1.0.0") + `
  optionalDependencies:
    native-arm64 "1.0.0"
    thing-wasm32-wasi "1.0.0"

devtool@^1.0.0:
  version "1.0.0"
  integrity ` + sri(t, "devtool", "1.0.0") + `

native-arm64@1.0.0:
  version "1.0.0"
  integrity ` + sri(t, "native-arm64", "1.0.0") + `

"shared@^1.0.0", shared@^1.0.0-0:
  version "1.0.0"
  integrity ` + sri(t, "shared", "1.0.0") + `

shared@^2.0.0:
  version "2.0.0"
  integrity ` + sri(t, "shared", "2.0.0") + `

thing-wasm32-wasi@1.0.0:
  version "1.0.0"
  integrity ` + sri(t, "thing-wasm32-wasi", "1.0.0") + `
`
	berry := `__metadata:
  version: 8
  cacheKey: 10c0

"a@npm:^1.0.0":
  version: 1.0.0
  resolution: "a@npm:1.0.0"
  dependencies:
    shared: "npm:^1.0.0"
  checksum: 10c0/deadbeef
  languageName: node
  linkType: hard

"b@npm:1.0.0":
  version: 1.0.0
  resolution: "b@npm:1.0.0"
  languageName: node
  linkType: hard

"devtool@npm:^1.0.0":
  version: 1.0.0
  resolution: "devtool@npm:1.0.0"
  languageName: node
  linkType: hard

"shared@npm:^1.0.0":
  version: 1.0.0
  resolution: "shared@npm:1.0.0"
  languageName: node
  linkType: hard

"shared@npm:^2.0.0":
  version: 2.0.0
  resolution: "shared@npm:2.0.0"
  languageName: node
  linkType: hard

"thing-wasm32-wasi@npm:1.0.0":
  version: 1.0.0
  resolution: "thing-wasm32-wasi@npm:1.0.0"
  languageName: node
  linkType: hard

"root@workspace:.":
  version: 0.0.0-use.local
  resolution: "root@workspace:."
  languageName: unknown
  linkType: soft
`
	// Newer pnpm splits the lockfile across multiple YAML documents: metadata
	// first, then importers/packages/snapshots. A single Unmarshal reads only
	// the first, which would make every root dependency look absent.
	pnpmMultiDoc := "lockfileVersion: '9.0'\nsettings:\n  autoInstallPeers: true\n  excludeLinksFromLockfile: true\n---\n" + pnpm
	for name, files := range map[string]map[string]string{
		"package-lock.json":        {"package-lock.json": npm},
		"bun.lock":                 {"bun.lock": bun},
		"pnpm-lock.yaml":           {"pnpm-lock.yaml": pnpm},
		"pnpm-lock.yaml multi-doc": {"pnpm-lock.yaml": pnpmMultiDoc},
		"yarn.lock v1":             {"yarn.lock": yarn1},
		"yarn.lock berry":          {"yarn.lock": berry},
	} {
		for _, dev := range []bool{true, false} {
			t.Run(name+map[bool]string{true: " dev", false: " prod"}[dev], func(t *testing.T) {
				dir := lockProject(t, files)
				if err := Install(context.Background(), dir, Options{Registry: srv.URL, Dev: dev}); err != nil {
					t.Fatal(err)
				}
				checkPinned(t, dir, dev)
			})
		}
	}

	// The lockfile's integrity is authoritative.
	t.Run("integrity mismatch", func(t *testing.T) {
		bad := strings.Replace(npm, sri(t, "shared", "2.0.0"), sri(t, "shared", "2.1.0"), 1)
		dir := lockProject(t, map[string]string{"package-lock.json": bad})
		if err := Install(context.Background(), dir, Options{Registry: srv.URL, Dev: true}); err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
			t.Errorf("err = %v", err)
		}
	})
	// A dependency the lockfile lacks means it is out of date (npm ci).
	t.Run("out of date", func(t *testing.T) {
		for file, body := range map[string]string{"package-lock.json": npm, "yarn.lock": yarn1, "pnpm-lock.yaml": pnpm} {
			dir := lockProject(t, map[string]string{file: body})
			if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"root","dependencies":{"a":"^1.0.0","shared":"^2.0.0","b":"1.0.0","devtool":"^1.0.0","extra":"^1.0.0"}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := Install(context.Background(), dir, Options{Registry: srv.URL, Dev: true}); err == nil || !strings.Contains(err.Error(), "extra") {
				t.Errorf("%s: err = %v", file, err)
			}
		}
	})
}

// TestLockfileBundled covers what real lockfiles contain beyond the basics
// (found with npm, pnpm and Yarn 4 output for the next-hello fixture):
// bundled dependencies, which arrive inside their parent's tarball (npm
// records them as inBundle entries without integrity; pnpm omits them),
// and Yarn's builtin patch: entries (e.g. for typescript).
func TestLockfileBundled(t *testing.T) {
	pkgs := []fakePkg{
		{name: "inner", version: "1.0.0"},
		{name: "outer", version: "1.0.0", deps: map[string]string{"inner": "^1.0.0"}, bundled: []string{"inner"}},
	}
	srv := fakeRegistry(t, pkgs)
	sum := sha512.Sum512(tarball(t, pkgs[1]))
	outerSRI := "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	for name, files := range map[string]map[string]string{
		"package-lock.json": {"package-lock.json": `{"lockfileVersion":3,"packages":{
  "":{"dependencies":{"outer":"^1.0.0"}},
  "node_modules/outer":{"version":"1.0.0","resolved":"` + srv.URL + `/tarballs/outer-1.0.0.tgz","integrity":"` + outerSRI + `","bundleDependencies":["inner"],"dependencies":{"inner":"^1.0.0"}},
  "node_modules/outer/node_modules/inner":{"version":"9.9.9","inBundle":true}
}}`},
		"pnpm-lock.yaml": {"pnpm-lock.yaml": `lockfileVersion: '9.0'
importers:
  .:
    dependencies:
      outer:
        specifier: ^1.0.0
        version: 1.0.0
packages:
  outer@1.0.0:
    resolution: {integrity: ` + outerSRI + `}
    bundledDependencies: [inner]
snapshots:
  outer@1.0.0: {}
`},
		"no lockfile": {},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			files["package.json"] = `{"name":"root","dependencies":{"outer":"^1.0.0"}}`
			for f, body := range files {
				if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
				t.Fatal(err)
			}
			nm := filepath.Join(dir, "node_modules")
			// The bundled copy (9.9.9, from the tarball) and nothing hoisted.
			if v := installedVersion(t, filepath.Join(nm, "outer/node_modules/inner")); v != "9.9.9" {
				t.Errorf("bundled inner = %q", v)
			}
			if v := installedVersion(t, filepath.Join(nm, "inner")); v != "" {
				t.Errorf("inner also installed separately (%s)", v)
			}
		})
	}

	t.Run("yarn patch entries", func(t *testing.T) {
		y, err := parseYarnBerry([]byte(`__metadata:
  version: 8

"typescript@npm:^5":
  version: 5.9.3
  resolution: "typescript@npm:5.9.3"

"typescript@patch:typescript@npm%3A^5#optional!builtin<compat/typescript>":
  version: 5.9.3
  resolution: "typescript@patch:typescript@npm%3A5.9.3#optional!builtin<compat/typescript>::version=5.9.3&hash=b45daf"
`))
		if err != nil {
			t.Fatal(err)
		}
		if v, _, _, err := y.pin(nil, "typescript", "^5"); err != nil || v != "5.9.3" {
			t.Errorf("pin = %q, %v", v, err)
		}
	})
}
