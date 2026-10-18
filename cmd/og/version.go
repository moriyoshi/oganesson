package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
)

// ogVersion is the human-readable version og reports: the release version
// injected at build time (-X main.version, from scripts/build-og.sh), else
// the module version Go stamped (e.g. a pseudo-version, or "(devel)" for an
// unstamped local build), else "unknown".
func ogVersion() string {
	if version != "" {
		return version
	}
	if bi := selfInfo(); bi != nil && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "unknown"
}

func runVersion(_ context.Context, args []string) error {
	fset := flag.NewFlagSet("version", flag.ExitOnError)
	_ = fset.Parse(args)
	return writeVersion(os.Stdout)
}

func writeVersion(w io.Writer) error {
	_, err := fmt.Fprintf(w, "og %s %s/%s %s\n", ogVersion(), runtime.GOOS, runtime.GOARCH, runtime.Version())
	return err
}
