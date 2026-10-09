// Command og compiles Next.js (and later Vite) projects into a single Go
// binary. See ARCHITECTURE.md and .agents/docs/JOURNAL.md for design notes.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// version is og's release version (e.g. "v0.3.0"), injected at build time
// with -ldflags "-X main.version=..." (scripts/build-og.sh does this from
// OG_VERSION). og build -eject pins it in the ejected go.mod.
var version string

type command struct {
	name, usage string
	run         func(ctx context.Context, args []string) error
}

var commands = []command{
	{"install", "install [dir]   install package.json dependencies (no npm needed)", runInstall},
	{"check", "check [dir]     type-check the project with TypeScript-Go", runCheck},
	{"build", "build [-o bin [-target goos/goarch] [-runtime og]] [-eject dir [-og-source checkout]] [-readable] [dir]\n                  build the app into .og/ (a single binary with -o, a Go module with -eject)", runBuild},
	{"serve", "serve [-addr :3000] [dir]  serve a built app", runServe},
	{"compat", "compat [-run re] [-failed report.json] [-j n] [-pages n] [-json file] dir...\n                  install, build, serve and crawl apps (the compatibility corpus)", runCompat},
	{"licenses", "licenses        print the licenses of og and the software it includes", runLicenses},
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func main() {
	// A Oganesson binary with an appended app payload is that app's server.
	if ran, err := servePayload(); ran {
		if err != nil {
			fmt.Fprintln(os.Stderr, "og:", err)
			os.Exit(1)
		}
		return
	}
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: og <command> [arguments]\n\ncommands:")
		for _, c := range commands {
			fmt.Fprintln(os.Stderr, "  "+c.usage)
		}
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signalContext()
	defer stop()
	for _, c := range commands {
		if c.name == flag.Arg(0) {
			if err := c.run(ctx, flag.Args()[1:]); err != nil {
				fmt.Fprintln(os.Stderr, "og:", err)
				os.Exit(1)
			}
			return
		}
	}
	fmt.Fprintf(os.Stderr, "og: unknown command %q\n", flag.Arg(0))
	flag.Usage()
	os.Exit(2)
}

func projectDir(fs *flag.FlagSet) string {
	if fs.NArg() > 0 {
		return fs.Arg(0)
	}
	return "."
}
