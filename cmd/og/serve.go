package main

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/moriyoshi/oganesson/pack"
	"github.com/moriyoshi/oganesson/runtime/app"
	"github.com/moriyoshi/oganesson/runtime/notices"
)

func runServe(ctx context.Context, args []string) error {
	fset := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fset.String("addr", defaultAddr(), "listen address")
	_ = fset.Parse(args)
	return serve(ctx, os.DirFS(builtDir(projectDir(fset))), *addr)
}

// builtDir is the built app to serve for dir: a project's .og/ when it has
// one, else dir itself (a build output directory). A project's own
// server.js (a custom server) does not make it a build output.
func builtDir(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ".og", app.MetaFile)); err == nil {
		return filepath.Join(dir, ".og")
	}
	if _, err := os.Stat(filepath.Join(dir, app.MetaFile)); err == nil {
		return dir
	}
	return filepath.Join(dir, ".og") // not built yet: the error names .og/
}

// servePayload runs the application embedded in this executable, if any.
func servePayload() (bool, error) {
	self, err := os.Executable()
	if err != nil {
		return false, nil
	}
	fsys, err := pack.Open(self)
	if errors.Is(err, pack.ErrNoPayload) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	addr, licenses := appFlags(filepath.Base(self), os.Args[1:])
	if licenses {
		return true, notices.Write(os.Stdout, fsys)
	}
	ctx, stop := signalContext()
	defer stop()
	return true, serve(ctx, fsys, addr)
}

// appFlags parses a packaged app binary's command line.
func appFlags(name string, args []string) (addr string, licenses bool) {
	fset := flag.NewFlagSet(name, flag.ExitOnError)
	a := fset.String("addr", defaultAddr(), "listen address (or set PORT)")
	l := fset.Bool("licenses", false, "print the licenses of this program and the software it includes")
	_ = fset.Parse(args)
	return *a, *l
}

func runLicenses(_ context.Context, args []string) error {
	fset := flag.NewFlagSet("licenses", flag.ExitOnError)
	_ = fset.Parse(args)
	return notices.Write(os.Stdout, nil)
}

func defaultAddr() string { return app.DefaultAddr() }

func serve(ctx context.Context, fsys fs.FS, addr string) error {
	return app.Serve(ctx, fsys, addr, nil)
}
