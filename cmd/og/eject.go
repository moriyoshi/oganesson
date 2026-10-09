package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/moriyoshi/oganesson/internal/nativegen"
	"github.com/moriyoshi/oganesson/runtime/notices"
	"golang.org/x/mod/semver"
)

// og build -eject writes the built app as a standalone Go module: the
// build output under app/ (go:embed), and a main package that serves it with
// runtime/app, the same server og builds into app binaries. Users own the
// module: they add Go routes or middleware in wrap.go and build it with their
// own toolchain (any GOOS/GOARCH, flags, signing).

var ejectFiles = map[string]*template.Template{
	"go.mod": template.Must(template.New("").Parse(`module {{.Module}}

go 1.27.0

require github.com/moriyoshi/oganesson {{.Version}}
{{with .Natives}}
// The app's native Go modules (og:*), registered in natives_gen.go.
require {{.ModulePath}} v0.0.0-00010101000000-000000000000

replace {{.ModulePath}} => {{.Dir}}
{{range $.UserReplaces}}// propagated from the native modules' go.mod (Go ignores a dependency's replaces)
replace {{.}}
{{end}}{{end}}{{if .Source}}
replace (
	github.com/moriyoshi/oganesson => {{.Source}}
	// Oganesson's own replace for TypeScript-Go does not apply to dependents.
	github.com/microsoft/TypeScript/tsc => {{.Source}}/third_party/typescript/tsc
)
{{else}}
// Until github.com/moriyoshi/oganesson is published, point at a checkout
// (with scripts/sync-typescript.sh run in it):
//
// replace (
// 	github.com/moriyoshi/oganesson => /path/to/oganesson
// 	github.com/microsoft/TypeScript/tsc => /path/to/oganesson/third_party/typescript/tsc
// )
{{end}}`)),
	"main.go": template.Must(template.New("").Parse(`// Command {{.Name}} serves the {{.Name}} app, built by Oganesson
// (og build -eject). The app itself is in app/, embedded; rebuild it with
// og build -eject to update it. Go code in front of it goes in wrap.go;
// the app's native Go modules (og:*) are registered in natives_gen.go.
package main

import (
	"context"
	"embed"
	"flag"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/moriyoshi/oganesson/runtime/app"
	"github.com/moriyoshi/oganesson/runtime/notices"
)

//go:embed all:app
var built embed.FS

func main() {
	addr := flag.String("addr", app.DefaultAddr(), "listen address (or set PORT)")
	licenses := flag.Bool("licenses", false, "print the licenses of this program and the software it includes")
	flag.Parse()
	fsys, err := fs.Sub(built, "app")
	if err != nil {
		log.Fatal(err)
	}
	if *licenses {
		// Oganesson's notices and the app's npm packages'. Add the licenses
		// of Go modules you add yourself.
		if err := notices.Write(os.Stdout, fsys); err != nil {
			log.Fatal(err)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Serve(ctx, fsys, *addr, wrap); err != nil {
		log.Fatal(err)
	}
}
`)),
	"wrap.go": template.Must(template.New("").Parse(`package main

import "net/http"

// wrap puts Go code in front of the app: extra routes, middleware,
// instrumentation. Requests it does not handle reach the app.
func wrap(app http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", app)
	return mux
}
`)),
	"README.md": template.Must(template.New("").Parse(`# {{.Name}}

The {{.Name}} app, ejected from Oganesson (` + "`og build -eject`" + `).

- ` + "`app/`" + ` is the built app (server bundle, browser assets, prerendered pages).
  It is generated: rebuild it with ` + "`og build -eject`" + ` from the app's project
  directory{{if .Readable}} (this copy was built with -readable: unminified browser
  bundles and stylesheets){{end}}.
- ` + "`main.go`" + ` serves it with Oganesson's runtime; ` + "`wrap.go`" + ` is yours.

Build and run:

    go mod tidy
    CGO_ENABLED=0 go build -trimpath -tags nodynamic -o {{.Name}} .
    ./{{.Name}} -addr :3000

` + "`-tags nodynamic`" + ` keeps the image optimizer's WebP encoder pure Go (otherwise
it loads a system libwebp when one is installed). Cross-compile with
` + "`GOOS`/`GOARCH`" + ` as usual.

` + "`./{{.Name}} -licenses`" + ` prints the licenses of Oganesson, the software it
includes, and the npm packages bundled into the app (also in
` + "`THIRD_PARTY_NOTICES.txt`" + `, refreshed on every eject). Go modules you add
yourself are not listed there.
`)),
}

// ogModuleVersion is the github.com/moriyoshi/oganesson version an ejected
// go.mod requires: the release version injected at build time
// (scripts/build-og.sh with OG_VERSION), else the module version Go stamped
// (set when og is built with go install from a published version), else a
// zero pseudo-version that only resolves through a replace. A version that
// go.mod would reject (non-canonical, or a VCS stamp with +dirty) is skipped.
func ogModuleVersion() string {
	if usableModuleVersion(version) {
		return version
	}
	if bi := selfInfo(); bi != nil && usableModuleVersion(bi.Main.Version) {
		return bi.Main.Version
	}
	return "v0.0.0-00010101000000-000000000000"
}

func usableModuleVersion(v string) bool {
	return semver.IsValid(v) && semver.Canonical(v) == v
}

var moduleNameRe = regexp.MustCompile(`[^a-z0-9._~-]+`)

// eject writes the module for the build output files into dir.
// natives, when the project has native Go modules, are registered in the
// generated natives_gen.go.
func eject(dir, projectDir string, files map[string][]byte, source string, readable bool, natives *nativegen.Project) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
			return fmt.Errorf("eject: %s is not empty and not an ejected module", dir)
		}
	}
	name := "app"
	if b, err := os.ReadFile(filepath.Join(projectDir, "package.json")); err == nil {
		var pkg struct{ Name string }
		if json.Unmarshal(b, &pkg) == nil && pkg.Name != "" {
			name = strings.Trim(moduleNameRe.ReplaceAllString(strings.ToLower(strings.TrimPrefix(pkg.Name, "@")), "-"), "-")
		}
	}
	if source != "" {
		abs, err := filepath.Abs(source)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
			return fmt.Errorf("eject: -og-source %s is not an Oganesson checkout", source)
		}
		source = abs
	}
	var replaces []string
	if natives != nil {
		replaces = userReplaces(natives)
	}
	data := map[string]any{"Name": name, "Module": name, "Version": ogModuleVersion(), "Source": source, "Readable": readable, "Natives": natives, "UserReplaces": replaces}
	// The app is regenerated; the Go files are the user's after the first eject.
	if err := os.RemoveAll(filepath.Join(dir, "app")); err != nil {
		return err
	}
	for rel, b := range files {
		p := filepath.Join(dir, "app", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	// Refreshed on every eject, like the app: og's runtime and the app's npm
	// packages, as -licenses prints them.
	var text bytes.Buffer
	if err := notices.Write(&text, mapFS(files)); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "THIRD_PARTY_NOTICES.txt"), text.Bytes(), 0o644); err != nil {
		return err
	}
	if err := ejectNatives(dir, natives); err != nil {
		return err
	}
	for file, tpl := range ejectFiles {
		p := filepath.Join(dir, file)
		if _, err := os.Stat(p); err == nil && file != "README.md" {
			continue // keep the user's edits
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			return err
		}
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ejectNatives regenerates natives_gen.go: the registrations of the
// project's native Go modules, or none (an earlier one is removed). A
// go.mod kept from an earlier eject must require the project's og/ module;
// og says so when it does not.
func ejectNatives(dir string, natives *nativegen.Project) error {
	p := filepath.Join(dir, nativeGlueFile)
	if natives == nil {
		if b, err := os.ReadFile(p); err == nil && bytes.HasPrefix(b, []byte("// Code generated by og build")) {
			return os.Remove(p)
		}
		return nil
	}
	glue, err := natives.Glue("main")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, glue, 0o644); err != nil {
		return err
	}
	if mod, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && !bytes.Contains(mod, []byte(natives.ModulePath)) {
		fmt.Fprintf(os.Stderr, "note: %s/go.mod does not require the native Go modules' module; add\n  require %s v0.0.0-00010101000000-000000000000\n  replace %s => %s\n", dir, natives.ModulePath, natives.ModulePath, natives.Dir)
	}
	return nil
}
