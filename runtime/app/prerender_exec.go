package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// PrerenderEnv names the directory of a built app that a binary prerenders
// instead of serving it (PrerenderMain). og build runs an app binary that
// has the project's native Go modules this way, so that pages calling them
// prerender with them.
const PrerenderEnv = "OG_PRERENDER_DIR"

type prerenderOutput struct {
	JSON    []byte   `json:"json"`
	Static  []string `json:"static"`
	Dynamic []string `json:"dynamic"`
}

// PrerenderMain prerenders the app in PrerenderEnv's directory when it is
// set (the routes are JSON on stdin; the result is JSON on stdout) and
// reports whether it did; a main function returns then.
func PrerenderMain() bool {
	dir := os.Getenv(PrerenderEnv)
	if dir == "" {
		return false
	}
	var routes []PrerenderRoute
	if err := json.NewDecoder(os.Stdin).Decode(&routes); err != nil {
		fmt.Fprintln(os.Stderr, "prerender:", err)
		os.Exit(1)
	}
	pre, err := Prerender(os.DirFS(dir), routes)
	if err != nil {
		fmt.Fprintln(os.Stderr, "prerender:", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(prerenderOutput{JSON: pre.JSON, Static: pre.Static, Dynamic: pre.Dynamic}); err != nil {
		fmt.Fprintln(os.Stderr, "prerender:", err)
		os.Exit(1)
	}
	return true
}

// PrerenderIn is Prerender run by the app binary exe (PrerenderMain), over
// the app's files written to a temporary directory. The result has JSON,
// Static and Dynamic.
func PrerenderIn(exe string, files map[string][]byte, routes []PrerenderRoute) (*PrerenderResult, error) {
	dir, err := os.MkdirTemp("", "og-prerender-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for name, b := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return nil, err
		}
	}
	in, err := json.Marshal(routes)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), PrerenderEnv+"="+dir)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("prerender with %s: %w", filepath.Base(exe), err)
	}
	var res prerenderOutput
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, err
	}
	return &PrerenderResult{JSON: res.JSON, Static: res.Static, Dynamic: res.Dynamic}, nil
}
