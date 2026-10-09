package next

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func fixtureApp(t *testing.T) string {
	t.Helper()
	dir := appDir("next-hello/app")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("fixture missing")
	}
	return dir
}

func TestBuildTreeFixture(t *testing.T) {
	app := fixtureApp(t)
	paths, err := ScanAppPaths(app, DefaultPageExtensions)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/page", "/blog/[slug]/page", "/api/hello/route"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("paths = %v, missing %s", paths, want)
		}
	}
	all := append(paths, notFoundRouteEntry)
	for _, page := range []string{"/page", "/blog/[slug]/page", notFoundRouteEntry} {
		res, err := BuildTree(app, page, []string{page}, all, DefaultPageExtensions, "", false)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s:\n%s\n%v", page, res.Code, res.Declarations)
		if !strings.HasSuffix(res.RootLayout, "app/layout.tsx") {
			t.Errorf("root layout = %s", res.RootLayout)
		}
	}
}

// TestBuildTreeGlobalNotFound: with experimental.globalNotFound the root tree
// carries a global-not-found component (the user's app/global-not-found, else
// the builtin); without the flag it is absent. Reads only the app directory, so
// it needs no node_modules.
func TestBuildTreeGlobalNotFound(t *testing.T) {
	app := appDir("next-global-not-found/app")
	if _, err := os.Stat(app); err != nil {
		t.Skip("fixture missing")
	}
	all := []string{"/page", notFoundRouteEntry}

	// Enabled, with a user app/global-not-found: the tree wires it.
	res, err := BuildTree(app, "/page", []string{"/page"}, all, DefaultPageExtensions, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Code, "'global-not-found': [") {
		t.Errorf("tree lacks a global-not-found component:\n%s", res.Code)
	}
	userDeclared := slices.ContainsFunc(res.Declarations, func(d Declaration) bool {
		return strings.HasSuffix(d.Path, "app/global-not-found.tsx")
	})
	if !userDeclared {
		t.Errorf("the user global-not-found is not imported; decls=%v", res.Declarations)
	}

	// Disabled: no global-not-found anywhere in the tree.
	off, err := BuildTree(app, "/page", []string{"/page"}, all, DefaultPageExtensions, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(off.Code, "'global-not-found': [") {
		t.Errorf("global-not-found component present with the flag off:\n%s", off.Code)
	}

	// Enabled but no user file: the builtin default is wired instead.
	hello := appDir("next-hello/app")
	if _, err := os.Stat(hello); err != nil {
		return
	}
	bi, err := BuildTree(hello, "/page", []string{"/page"}, []string{"/page", notFoundRouteEntry}, DefaultPageExtensions, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bi.Code, "'global-not-found': [") ||
		!slices.ContainsFunc(bi.Declarations, func(d Declaration) bool {
			return strings.HasSuffix(d.Path, "builtin/global-not-found.js")
		}) {
		t.Errorf("builtin global-not-found not wired when enabled without a user file; decls=%v", bi.Declarations)
	}
}
