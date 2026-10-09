package next

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/moriyoshi/oganesson/internal/npmscript"
)

// next build picks its bundler before it loads next.config
// (lib/bundler.js's parseBundlerArgs): --webpack, --turbopack / --turbo
// (TURBOPACK=1), or, with neither, Turbopack by default with
// TURBOPACK=auto. Config plugins read that variable: next-rspack switches
// to Rspack (NEXT_RSPACK) only under "auto", @next/mdx adds its Turbopack
// rule only with TURBOPACK set. og loads next.config with the variable the
// project's build script would give it; the webpack hook (bridge.js) then
// applies the turbopack section or webpack(), as the bundler would.

// bundlerEnv is the environment next.config loads with: TURBOPACK as the
// project's `next build` command sets it (nothing for --webpack; an
// inherited TURBOPACK is kept, as Next keeps it).
func bundlerEnv(dir string) map[string]string {
	if os.Getenv("TURBOPACK") != "" {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
	}
	for _, c := range npmscript.Commands(pkg.Scripts, "build") {
		if len(c.Args) < 2 || c.Args[0] != "next" || c.Args[1] != "build" {
			continue
		}
		if v := c.Env["TURBOPACK"]; v != "" {
			return map[string]string{"TURBOPACK": v}
		}
		for _, a := range c.Args[2:] {
			switch a {
			case "--webpack":
				return nil
			case "--turbopack", "--turbo":
				return map[string]string{"TURBOPACK": "1"}
			}
		}
		break
	}
	return map[string]string{"TURBOPACK": "auto"}
}
