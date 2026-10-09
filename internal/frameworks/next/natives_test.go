package next

import (
	"io"
	"net/http"
	"testing"
)

// TestNativePackages: a route using bcrypt, sharp and better-sqlite3 runs on
// Oganesson's substitutes (runtime/natives).
func TestNativePackages(t *testing.T) {
	srv := serveFixture(t)
	resp, err := http.Get(srv.URL + "/api/natives")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if want := `{"bcrypt":true,"sharp":"webp 15x10","sqlite":["hello"]}`; resp.StatusCode != 200 || string(b) != want {
		t.Errorf("GET /api/natives: %d %s\nwant %s", resp.StatusCode, b, want)
	}
}

// TestNodeBufferConstants: node:buffer (Next's compiled buffer, patched)
// has Node's constants and kStringMaxLength.
func TestNodeBufferConstants(t *testing.T) {
	srv := serveFixture(t)
	resp, err := http.Get(srv.URL + "/api/buffer")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if want := `{"maxLength":true,"maxStringLength":536870888}`; resp.StatusCode != 200 || string(b) != want {
		t.Errorf("GET /api/buffer: %d %s\nwant %s", resp.StatusCode, b, want)
	}
}

// TestNodeModeDefaultImport: an ES module of a "type": "module" package
// gets a CommonJS module's module.exports as its default import, though
// the module is flagged __esModule (Node's rule; tslib's case). The
// expected JSON is Node 24's.
func TestNodeModeDefaultImport(t *testing.T) {
	srv := serveFixture(t)
	resp, err := http.Get(srv.URL + "/api/interop")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// And next/font/local there is compiled (geist's case).
	if want := `{"defaultHello":"hi","namespaceHello":"hi","fontVariable":true}`; resp.StatusCode != 200 || string(b) != want {
		t.Errorf("GET /api/interop: %d %s\nwant %s", resp.StatusCode, b, want)
	}
}

// TestMissingOptionalPackages: import() and require() of packages that are
// not installed throw when they run, with Node's codes, instead of failing
// the build.
func TestMissingOptionalPackages(t *testing.T) {
	srv := serveFixture(t)
	resp, err := http.Get(srv.URL + "/api/optional")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if want := `{"import":"ERR_MODULE_NOT_FOUND","require":"MODULE_NOT_FOUND"}`; resp.StatusCode != 200 || string(b) != want {
		t.Errorf("GET /api/optional: %d %s\nwant %s", resp.StatusCode, b, want)
	}
}
