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

// goTool is the Go toolchain og compiles an app runtime with: the -go flag,
// else $OG_GO, else "go" found on PATH. An explicit value may be a path or a
// name; exec.LookPath resolves and validates either.
func goTool(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("OG_GO"); v != "" {
		return v
	}
	return "go"
}

// buildNativeRuntime builds, into out, the executable an app with the
// project's native modules is packaged with, for goos/goarch, with the
// gotool Go toolchain.
func buildNativeRuntime(ctx context.Context, gotool string, proj *nativegen.Project, source, goos, goarch, out string) error {
	gobin, err := exec.LookPath(gotool)
	if err != nil {
		return fmt.Errorf("the project has native Go modules (%s/): building its binary needs the Go toolchain (%s)", nativegen.Dir, gotool)
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
	return goBuildModule(ctx, gobin, dir, source, files, goos, goarch, out)
}

// goBuildModule writes the generated module files into dir and builds it into
// out for goos/goarch with the local Go toolchain. The flags match
// scripts/build-og.sh (CGO off, -tags nodynamic, stripped with -ldflags
// "-s -w") since the result stands in for an og built that way; pclntab stays,
// so stack traces keep file:line. When source is an Oganesson checkout, its
// go.sum spares go mod tidy most lookups.
func goBuildModule(ctx context.Context, gobin, dir, source string, files map[string][]byte, goos, goarch, out string) error {
	if source != "" {
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
	return run("build", "-trimpath", "-tags", "nodynamic", "-ldflags", "-s -w", "-o", abs, ".")
}

// slimGoMod is the go.mod of a slim runtime module: it requires only
// Oganesson, so the built binary links the serve path (runtime/app) and none
// of og's build-time toolchain (the TypeScript compiler, the bundler). Like
// nativeGoMod it re-declares the TypeScript submodule replacement when
// building against a checkout, because Go ignores a dependency's own replace
// directives.
var slimGoMod = template.Must(template.New("").Parse(`module ogapp

go 1.27.0

require github.com/moriyoshi/oganesson {{.Version}}
{{- if .Source}}

replace (
	github.com/moriyoshi/oganesson => {{.Source}}
	github.com/microsoft/TypeScript/tsc => {{.Source}}/third_party/typescript/tsc
)
{{- end}}
`))

// slimRuntimeReason reports why a slim serve-only runtime cannot be built for
// this og, or "" when one can. og build -o packages an ordinary (no native
// module) app with a slim runtime when possible, and otherwise copies the
// whole og; the reason is surfaced so the larger binary is not a surprise. The
// conditions mirror buildNativeRuntime: the Go toolchain must be on PATH, and
// an unpublished og (a pseudo-version) resolves its own module only from a
// checkout (-og-source / OG_SOURCE).
func slimRuntimeReason(source, gotool string) string {
	if _, err := exec.LookPath(gotool); err != nil {
		return fmt.Sprintf("the Go toolchain (%s) is not available", gotool)
	}
	if source == "" && strings.HasPrefix(ogModuleVersion(), "v0.0.0-") {
		return "this og is an unpublished build and -og-source/OG_SOURCE is unset"
	}
	return ""
}

// buildSlimRuntime builds, into out, a serve-only runtime for goos/goarch: the
// same main (nativeMain) a native app uses, but with no project module and no
// glue, so it links runtime/app and not og's TypeScript compiler or bundler.
// og build -o packages an ordinary app with this instead of copying the whole
// og, which is far larger. Callers gate on slimRuntimeReason first, so the Go
// toolchain and a resolvable og module are already assured.
func buildSlimRuntime(ctx context.Context, gotool, source, goos, goarch, out string) error {
	gobin, err := exec.LookPath(gotool)
	if err != nil {
		return fmt.Errorf("building a slim runtime needs the Go toolchain (%s)", gotool)
	}
	dir, err := os.MkdirTemp("", "og-slim-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	var mod bytes.Buffer
	if err := slimGoMod.Execute(&mod, map[string]any{"Version": ogModuleVersion(), "Source": source}); err != nil {
		return err
	}
	files := map[string][]byte{"go.mod": mod.Bytes(), "main.go": []byte(nativeMain)}
	return goBuildModule(ctx, gobin, dir, source, files, goos, goarch, out)
}
