package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/notices"
)

// Every module linked into this binary (og's own dependencies, as the test
// binary links cmd/og) must have a section in the embedded notices; a stale
// THIRD_PARTY_NOTICES.txt after a dependency change fails here.
func TestNoticesCoverLinkedModules(t *testing.T) {
	bi := selfInfo()
	if bi == nil || len(bi.Deps) == 0 {
		t.Skip("no module build info in the test binary")
	}
	for _, dep := range bi.Deps {
		heading := "\n" + dep.Path + " " + dep.Version + "\n"
		if dep.Replace != nil {
			heading = "\n" + dep.Path + " " // a replaced module is listed by its source revision
		}
		if !strings.Contains(notices.Text, heading) {
			t.Errorf("notices lack %s %s; run go generate ./runtime/notices", dep.Path, dep.Version)
		}
	}
	for _, want := range []string{
		"\nOganesson (github.com/moriyoshi/oganesson)\n\n--- LICENSE\n\nMIT License\n",
		"\nGo (the Go standard library and runtime)\n",
		"NOTICES AND INFORMATION",                                 // TypeScript's NOTICE.txt (Apache-2.0 section 4(d))
		"\nunenv (vendored JavaScript)\n",                         // a vendored JS tree
		" (vendored Go: require/)\n",                              // goja_nodejs, vendored Go (not a module)
		"\nUnicode emoji data (runtime/webapi/emoji_tables.go)\n", // generated from Unicode data
	} {
		if !strings.Contains(notices.Text, want) {
			t.Errorf("notices lack %q", want)
		}
	}
}

// Every vendored JavaScript package that scripts/collect-notices.sh attributes
// must have a section in the embedded notices, so a stale
// THIRD_PARTY_NOTICES.txt after scripts/vendor-js.sh adds or removes a package
// fails here. TestNoticesCoverLinkedModules guards the linked Go modules; this
// guards the vendored trees, whose staleness build info cannot reveal. The
// discovery below mirrors collect-notices.sh exactly.
func TestNoticesCoverVendoredJS(t *testing.T) {
	root := repoRoot(t)
	// The same four trees collect-notices.sh scans.
	trees := []string{
		"runtime/webapi/js/vendor",
		"runtime/nodeshim/vendor",
		"internal/jshost/js/vendor",
		"internal/frameworks/vite/js/vendor",
	}
	// Packages that ship no license file get a bare header plus a declared
	// license, not a "(vendored JavaScript)" section; they must be special-
	// cased in collect-notices.sh too, so the sets stay in lockstep.
	specialNoLicense := map[string]string{
		"napi-wasm": "\nnapi-wasm\n\nLicensed under the MIT License",
	}

	var names []string
	for _, tree := range trees {
		treeAbs := filepath.Join(root, filepath.FromSlash(tree))
		if _, err := os.Stat(treeAbs); err != nil {
			t.Errorf("vendored tree %s missing: %v", tree, err)
			continue
		}
		treeSlash := filepath.ToSlash(treeAbs)
		seen := map[string]bool{}
		err := filepath.WalkDir(treeAbs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || (d.Name() != "package.json" && d.Name() != "VERSION") {
				return nil
			}
			slash := filepath.ToSlash(p)
			// find -not -path '*/node_modules/*/node_modules/*': skip a
			// manifest nested two node_modules deep.
			if i := strings.Index(slash, "/node_modules/"); i >= 0 &&
				strings.Contains(slash[i+len("/node_modules/"):], "/node_modules/") {
				return nil
			}
			dir := filepath.ToSlash(filepath.Dir(p))
			// Skip build-output subdirectories (dist/lib/esm/cjs), as the
			// script's case statement does.
			switch {
			case strings.HasSuffix(dir, "/dist"), strings.Contains(dir, "/dist/"),
				strings.HasSuffix(dir, "/lib"), strings.Contains(dir, "/lib/"),
				strings.HasSuffix(dir, "/esm"), strings.HasSuffix(dir, "/cjs"):
				return nil
			}
			name := strings.TrimPrefix(dir, treeSlash+"/")
			name = strings.TrimPrefix(name, "node_modules/")
			if name == "" || seen[name] {
				return nil
			}
			seen[name] = true
			names = append(names, name)
			return nil
		})
		if err != nil {
			t.Errorf("walk %s: %v", tree, err)
		}
	}
	// A broken walk that finds nothing must fail, not pass vacuously.
	if len(names) == 0 {
		t.Fatal("discovered no vendored JS packages; the discovery logic is broken")
	}
	for _, name := range names {
		if want, ok := specialNoLicense[name]; ok {
			if !strings.Contains(notices.Text, want) {
				t.Errorf("notices lack special-cased vendored package %q", name)
			}
			continue
		}
		if !strings.Contains(notices.Text, "\n"+name+" (vendored JavaScript)\n") {
			t.Errorf("notices lack vendored JS package %q; run go generate ./runtime/notices", name)
		}
	}
}

// repoRoot returns the module root (two levels above this test's source file),
// or skips when the source tree is not present (e.g. a prebuilt test binary).
func repoRoot(t *testing.T) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("no caller info; cannot locate the repo root")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skipf("repo root not found at %s: %v", root, err)
	}
	return root
}

func TestAppFlags(t *testing.T) {
	if addr, licenses := appFlags("app", []string{"-addr", ":8080"}); addr != ":8080" || licenses {
		t.Errorf("appFlags(-addr :8080) = %q, %v", addr, licenses)
	}
	if _, licenses := appFlags("app", []string{"-licenses"}); !licenses {
		t.Error("appFlags(-licenses) did not ask for the licenses")
	}
}
