package vite

import (
	"encoding/json"
	"github.com/moriyoshi/oganesson/internal/npmscript"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A project's own scripts say how it builds and how it serves; og follows
// them rather than guessing (package.json "build", then "preview" /
// "start" / "serve").

// Invocation is one `vite build` from the build script: its CLI arguments
// after "build".
type Invocation struct {
	Args []string
}

// ServerProgram is the Node program a script runs to serve the build
// (`[cross-env A=B] node server`, or a dependency's bin such as
// `react-router-serve ./build/server/index.js`): its file, its arguments
// (process.argv after the file) and environment.
type ServerProgram struct {
	File string
	Args []string
	Env  map[string]string
}

type packageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func readPackage(dir string) packageJSON {
	var pkg packageJSON
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(b, &pkg)
	}
	return pkg
}

// buildInvocations expands the build script into its `vite build`
// commands, following `npm run` (pnpm, yarn, bun) into other scripts;
// other commands (tsc, …) are skipped. nil when it has none.
func buildInvocations(pkg packageJSON) []Invocation {
	var out []Invocation
	seen := map[string]bool{}
	var expand func(name string)
	expand = func(name string) {
		script, ok := pkg.Scripts[name]
		if !ok || seen[name] {
			return
		}
		seen[name] = true
		for _, cmd := range splitCommands(script) {
			args, _ := stripEnv(shellWords(cmd))
			if len(args) == 0 {
				continue
			}
			if run := runTarget(args, pkg.Scripts); run != "" {
				expand(run)
				continue
			}
			// Qwik's CLI: `qwik build [preview]` runs the prebuild.*
			// scripts, build.client, then build.lib, build.server (or
			// build.preview) and build.static, then the postbuild.* scripts
			// (build.types is tsc). `qwik check-client` builds the client
			// only when it is stale: after build.client it is not.
			if args[0] == "qwik" && len(args) > 1 {
				if args[1] == "build" {
					for _, name := range qwikBuildScripts(pkg, len(args) > 2 && args[2] == "preview") {
						expand(name)
					}
				}
				continue
			}
			if isVite(args[0]) && len(args) > 1 && args[1] == "build" {
				out = append(out, Invocation{Args: args[2:]})
			}
		}
		seen[name] = false
	}
	expand("build")
	return out
}

// qwikBuildScripts are the scripts `qwik build` runs, in order.
func qwikBuildScripts(pkg packageJSON, preview bool) []string {
	var pre, post []string
	for name := range pkg.Scripts {
		if strings.HasPrefix(name, "prebuild.") {
			pre = append(pre, name)
		} else if strings.HasPrefix(name, "postbuild.") {
			post = append(post, name)
		}
	}
	sort.Strings(pre)
	sort.Strings(post)
	server := "build.server"
	if preview {
		server = "build.preview"
	}
	out := append(pre, "build.client", "build.lib", server, "build.static")
	return append(out, post...)
}

// serverProgram finds the Node program the preview / start / serve script
// runs, resolved as node resolves its argument.
func serverProgram(dir string, pkg packageJSON) *ServerProgram {
	return findServerProgram(dir, pkg, true)
}

// findServerProgram is serverProgram, with dependency bins considered or
// not (Nuxt's `nuxt preview` is its CLI, which runs Nitro's server in a
// child process: the program is the `node` one).
func findServerProgram(dir string, pkg packageJSON, bins bool) *ServerProgram {
	// A `node <file>` command in any of the scripts wins over a
	// dependency's bin: SolidStart's template has both
	// "preview": "vite preview" and "start": "node .output/server/index.mjs".
	if p := scanServerScripts(dir, pkg, false); p != nil || !bins {
		return p
	}
	return scanServerScripts(dir, pkg, true)
}

// scanServerScripts looks through the preview, start and serve scripts
// for a `node <file>` command, or with bins, for a dependency's bin.
func scanServerScripts(dir string, pkg packageJSON, bins bool) *ServerProgram {
	for _, name := range []string{"preview", "start", "serve"} {
		script, ok := pkg.Scripts[name]
		if !ok {
			continue
		}
		for _, cmd := range splitCommands(script) {
			args, env := stripEnv(shellWords(cmd))
			if len(args) == 0 {
				continue
			}
			if args[0] != "node" {
				// A dependency's bin, run as node runs it.
				if !bins {
					continue
				}
				if file := dependencyBin(dir, pkg, args[0]); file != "" {
					return &ServerProgram{File: file, Args: args[1:], Env: env}
				}
				continue
			}
			for i, a := range args[1:] {
				if strings.HasPrefix(a, "-") {
					continue
				}
				if file := resolveNodeMain(filepath.Join(dir, a)); file != "" {
					return &ServerProgram{File: file, Args: args[i+2:], Env: env}
				}
				break
			}
		}
	}
	return nil
}

// ownPreviews are the packages whose preview servers og implements
// itself; their bins are never server programs, even when a template
// lists them under dependencies (SolidStart lists vite there).
var ownPreviews = map[string]bool{"vite": true, "astro": true}

// dependencyBin is the file of the bin named name that one of the
// project's dependencies declares (package.json "bin"), or "". Packages
// og implements itself (ownPreviews) are never bins here.
func dependencyBin(dir string, pkg packageJSON, name string) string {
	for dep := range pkg.Dependencies {
		if ownPreviews[dep] {
			continue
		}
		pkgDir := filepath.Join(dir, "node_modules", filepath.FromSlash(dep))
		b, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
		if err != nil {
			continue
		}
		var pj struct {
			Bin json.RawMessage `json:"bin"`
		}
		if json.Unmarshal(b, &pj) != nil || len(pj.Bin) == 0 {
			continue
		}
		var single string
		bins := map[string]string{}
		if json.Unmarshal(pj.Bin, &single) == nil {
			// A string bin is named after the package (without its scope).
			bins[dep[strings.LastIndex(dep, "/")+1:]] = single
		} else {
			_ = json.Unmarshal(pj.Bin, &bins)
		}
		if rel, ok := bins[name]; ok {
			if file := filepath.Join(pkgDir, filepath.FromSlash(rel)); fileExists(file) {
				return file
			}
		}
	}
	return ""
}

func resolveNodeMain(p string) string {
	for _, c := range []string{p, p + ".js", p + ".mjs", p + ".cjs", filepath.Join(p, "index.js"), filepath.Join(p, "index.mjs")} {
		if fileExists(c) {
			return c
		}
	}
	return ""
}

func isVite(bin string) bool {
	return bin == "vite" || strings.HasSuffix(bin, "/.bin/vite") || strings.HasSuffix(bin, "/vite/bin/vite.js")
}

func runTarget(args []string, scripts map[string]string) string {
	return npmscript.RunTarget(args, scripts)
}

func stripEnv(args []string) ([]string, map[string]string) { return npmscript.StripEnv(args) }
func splitCommands(script string) []string                 { return npmscript.SplitCommands(script) }
func shellWords(cmd string) []string                       { return npmscript.ShellWords(cmd) }

// cliConfig turns `vite build` arguments into what Vite's CLI passes to
// createBuilder: the root, the mode, a config file, and inline config
// (build options from --outDir, --ssr, …; --base).
func cliConfig(args []string) (root, mode, configFile string, inline map[string]any) {
	build := map[string]any{}
	inline = map[string]any{}
	optional := map[string]bool{"ssr": true, "sourcemap": true, "minify": true, "manifest": true, "ssrManifest": true, "emptyOutDir": true, "watch": true, "app": true}
	value := func(s string) any {
		switch s {
		case "true":
			return true
		case "false":
			return false
		}
		return s
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			root = a
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !hasVal && !optional[name] && i+1 < len(args) {
			val, hasVal = args[i+1], true
			i++
		} else if !hasVal && optional[name] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			val, hasVal = args[i+1], true
			i++
		}
		var v any = true
		if hasVal {
			v = value(val)
		}
		switch name {
		case "m", "mode":
			mode, _ = v.(string)
		case "c", "config":
			configFile, _ = v.(string)
		case "base":
			inline["base"] = v
		case "l", "logLevel", "clearScreen", "configLoader", "d", "debug", "f", "filter", "app", "w", "watch":
			// Not build output options.
		default:
			build[name] = v
		}
	}
	if len(build) > 0 {
		inline["build"] = build
	}
	return
}
