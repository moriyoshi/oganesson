package main

import (
	"debug/buildinfo"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
)

// The runtime of an app binary is an og executable: og build -o appends the
// app to it. For the host platform that is the running og; for another
// (-target goos/goarch) it is an og built for that platform from the same
// source, given with -runtime or found in the runtime cache.

const ogMainPath = "github.com/moriyoshi/oganesson/cmd/og"

// runtimeCacheDir is where runtimes for other platforms are looked up:
// $UserCacheDir/oganesson/runtimes/<source>/.
func runtimeCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "oganesson", "runtimes", sourceID(selfInfo())), nil
}

func selfInfo() *debug.BuildInfo {
	bi, _ := debug.ReadBuildInfo()
	return bi
}

func setting(bi *debug.BuildInfo, key string) string {
	if bi == nil {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

// sourceID names the source an og was built from: its VCS revision (with
// "-dirty" for modified trees) or, without VCS stamping, its module version.
func sourceID(bi *debug.BuildInfo) string {
	if rev := setting(bi, "vcs.revision"); rev != "" {
		if setting(bi, "vcs.modified") == "true" {
			return rev + "-dirty"
		}
		return rev
	}
	if bi != nil && bi.Main.Version != "" {
		return strings.NewReplacer("(", "", ")", "").Replace(bi.Main.Version)
	}
	return "unknown"
}

// runtimeFileName is a cached runtime's file name.
func runtimeFileName(goos, goarch string) string {
	name := "og-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// parseTarget splits "goos/goarch" ("" means the host).
func parseTarget(target string) (goos, goarch string, err error) {
	if target == "" {
		return runtime.GOOS, runtime.GOARCH, nil
	}
	goos, goarch, ok := strings.Cut(target, "/")
	if !ok || goos == "" || goarch == "" {
		return "", "", fmt.Errorf("-target must be goos/goarch (e.g. linux/amd64), got %q", target)
	}
	return goos, goarch, nil
}

// selectRuntime returns the og executable to package an app for target with.
func selectRuntime(target, explicit string) (string, error) {
	goos, goarch, err := parseTarget(target)
	if err != nil {
		return "", err
	}
	if explicit != "" {
		return explicit, checkRuntime(explicit, goos, goarch, ogMainPath, selfInfo())
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		return os.Executable()
	}
	dir, err := runtimeCacheDir()
	if err != nil {
		return "", err
	}
	cached := filepath.Join(dir, runtimeFileName(goos, goarch))
	if _, err := os.Stat(cached); err != nil {
		return "", fmt.Errorf("no og runtime for %s/%s: build one from the same source with\n  scripts/build-og.sh %s %s/%s\nor pass -runtime <og built for %s/%s>", goos, goarch, cached, goos, goarch, goos, goarch)
	}
	return cached, checkRuntime(cached, goos, goarch, ogMainPath, selfInfo())
}

// checkRuntime verifies that file is the program mainPath (og) for
// goos/goarch, without cgo, built from the same source as self.
func checkRuntime(file, goos, goarch, mainPath string, self *debug.BuildInfo) error {
	bi, err := buildinfo.ReadFile(file)
	if err != nil {
		return fmt.Errorf("runtime %s: not a Go executable: %w", file, err)
	}
	if bi.Path != mainPath {
		return fmt.Errorf("runtime %s: not og (it is %s)", file, bi.Path)
	}
	if g, a := setting(bi, "GOOS"), setting(bi, "GOARCH"); g != goos || a != goarch {
		return fmt.Errorf("runtime %s: built for %s/%s, not %s/%s", file, g, a, goos, goarch)
	}
	if setting(bi, "CGO_ENABLED") == "1" {
		return fmt.Errorf("runtime %s: built with cgo; build runtimes with scripts/build-og.sh", file)
	}
	if !sameSource(self, bi) {
		return fmt.Errorf("runtime %s: built from %s, but this og is from %s", file, sourceID(bi), sourceID(self))
	}
	return nil
}

// sameSource reports whether two builds may come from the same source: they
// must when both are stamped with a VCS revision. Unstamped builds pass
// here; the app format version (app.FormatVersion) still guards the
// runtime when it starts.
func sameSource(a, b *debug.BuildInfo) bool {
	if a == nil || b == nil || setting(a, "vcs.revision") == "" || setting(b, "vcs.revision") == "" {
		return true
	}
	return sourceID(a) == sourceID(b)
}
