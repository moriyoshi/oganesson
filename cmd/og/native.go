package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/moriyoshi/oganesson/internal/nativegen"
)

// A project's native Go modules (a Go module in its og/ directory, see
// runtime/native) are Go code the og executable does not have, so an app
// binary with them is not og plus the payload: og build -o generates a
// small main package (nativeMain, with the registrations nativegen
// generates), builds it with the local Go toolchain, and appends the
// payload to that instead.

// nativeGlueFile is the generated registration file, in a native runtime's
// module and in an ejected one.
const nativeGlueFile = "natives_gen.go"

var nativeGoMod = template.Must(template.New("").Parse(`module ogapp

go 1.27.0

require (
	github.com/moriyoshi/oganesson {{.Version}}
	{{.UserModule}} v0.0.0-00010101000000-000000000000
)

replace (
{{- if .Source}}
	github.com/moriyoshi/oganesson => {{.Source}}
	github.com/microsoft/TypeScript/tsc => {{.Source}}/third_party/typescript/tsc
{{- end}}
	{{.UserModule}} => {{.UserDir}}
{{- range .UserReplaces}}
	{{.}}
{{- end}}
)
`))

// ogTscModule is the TypeScript-Go submodule og's go.mod replaces; a project
// may not shadow it (nor og itself, nor its own module).
const ogTscModule = "github.com/microsoft/TypeScript/tsc"

// userReplaces renders the project's own go.mod replace directives for the
// generated module. Go ignores a dependency's replaces, so og propagates
// them; a filesystem replacement (no version) is resolved against the
// project's og/ directory so the relative path still points at the same tree
// from the temporary build directory. Directives that would shadow og, its
// TypeScript submodule or the project module itself are dropped (og writes
// those).
func userReplaces(proj *nativegen.Project) []string {
	var lines []string
	for _, r := range proj.Replaces {
		switch r.OldPath {
		case "github.com/moriyoshi/oganesson", ogTscModule, proj.ModulePath:
			continue
		}
		old := r.OldPath
		if r.OldVersion != "" {
			old += " " + r.OldVersion
		}
		var repl string
		if r.NewVersion == "" {
			// A filesystem path, relative to og/go.mod's directory.
			p := r.NewPath
			if !filepath.IsAbs(p) {
				p = filepath.Join(proj.Dir, filepath.FromSlash(p))
			}
			repl = p
		} else {
			repl = r.NewPath + " " + r.NewVersion
		}
		lines = append(lines, old+" => "+repl)
	}
	return lines
}

// nativeMain is the main package of an app binary with native modules: a
// packaged app's server (the payload appended to it, like og's
// servePayload), or, run by og build, the prerenderer of a built app.
const nativeMain = `// Command ogapp serves an Oganesson app with native Go modules; og build
// generated it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/moriyoshi/oganesson/pack"
	"github.com/moriyoshi/oganesson/runtime/app"
	"github.com/moriyoshi/oganesson/runtime/notices"
)

func main() {
	if app.PrerenderMain() {
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	fsys, err := pack.Open(self)
	if err != nil {
		return err
	}
	fset := flag.NewFlagSet(filepath.Base(self), flag.ExitOnError)
	addr := fset.String("addr", app.DefaultAddr(), "listen address (or set PORT)")
	licenses := fset.Bool("licenses", false, "print the licenses of this program and the software it includes")
	_ = fset.Parse(os.Args[1:])
	if *licenses {
		return notices.Write(os.Stdout, fsys)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Serve(ctx, fsys, *addr, nil)
}
`

// ogSourceDir is the Oganesson checkout generated Go modules build against:
// -og-source, else $OG_SOURCE ("" when neither is set).
func ogSourceDir(flagValue string) (string, error) {
	src := flagValue
	if src == "" {
		src = os.Getenv("OG_SOURCE")
	}
	if src == "" {
		return "", nil
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		return "", fmt.Errorf("%s is not an Oganesson checkout (-og-source or OG_SOURCE)", src)
	}
	return abs, nil
}

// buildNativeRuntime builds, into out, the executable an app with the
// project's native modules is packaged with, for goos/goarch, with the
// local Go toolchain.
func buildNativeRuntime(ctx context.Context, proj *nativegen.Project, source, goos, goarch, out string) error {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("the project has native Go modules (%s/): building its binary needs the Go toolchain on PATH", nativegen.Dir)
	}
	version := ogModuleVersion()
	if source == "" && strings.HasPrefix(version, "v0.0.0-") {
		// Not a published og: go cannot resolve the module itself.
		return fmt.Errorf("the project has native Go modules (%s/): building its binary needs an Oganesson checkout; pass -og-source <dir> or set OG_SOURCE", nativegen.Dir)
	}
	dir, err := os.MkdirTemp("", "og-native-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var mod bytes.Buffer
	if err := nativeGoMod.Execute(&mod, map[string]any{"Version": version, "Source": source, "UserModule": proj.ModulePath, "UserDir": proj.Dir, "UserReplaces": userReplaces(proj)}); err != nil {
		return err
	}
	glue, err := proj.Glue("main")
	if err != nil {
		return err
	}
	files := map[string][]byte{"go.mod": mod.Bytes(), "main.go": []byte(nativeMain), nativeGlueFile: glue}
	if source != "" {
		// Oganesson's checksums spare go mod tidy most lookups.
		if b, err := os.ReadFile(filepath.Join(source, "go.sum")); err == nil {
			files["go.sum"] = b
		}
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return err
		}
	}
	env := append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch, "GOWORK=off", "GOFLAGS=-mod=mod")
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, gobin, args...)
		cmd.Dir, cmd.Env = dir, env
		var stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stderr, &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
		}
		return nil
	}
	if err := run("mod", "tidy"); err != nil {
		return err
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	return run("build", "-trimpath", "-tags", "nodynamic", "-o", abs, ".")
}
