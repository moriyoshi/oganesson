package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/moriyoshi/oganesson/internal/install"
)

func runInstall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	dev := fs.Bool("dev", true, "include devDependencies")
	verbose := fs.Bool("v", false, "log each package")
	cache := fs.String("cache", defaultInstallCache(), `directory keeping registry metadata and tarballs between installs ("off": none)`)
	_ = fs.Parse(args)
	opts := install.Options{Dev: *dev, Log: func(f string, a ...any) {
		if *verbose {
			fmt.Fprintf(os.Stderr, f+"\n", a...)
		}
	}}
	if *cache != "off" {
		opts.CacheDir = *cache
	}
	return install.Install(ctx, projectDir(fs), opts)
}

// defaultInstallCache is <UserCacheDir>/oganesson/npm ("off" without a
// user cache directory).
func defaultInstallCache() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "off"
	}
	return filepath.Join(dir, "oganesson", "npm")
}
