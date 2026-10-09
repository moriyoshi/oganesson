package next

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/internal/npmscript"
)

// codegen runs what a project's scripts run before Next compiles it, as npm
// would: "postinstall" and "prepare" at install time (og's installer runs
// no lifecycle scripts), then "prebuild" and the build script's commands
// before `next build`. Code generators the app imports the output of run
// there (Prisma's client, panda's styled-system, GraphQL clients, compiled
// message catalogs, an RSS feed). Each command runs on og's build host:
//
//   - `prisma generate`: Prisma 7's generator writes the TypeScript client
//     (with its WebAssembly query compiler) the app imports. The CLI wants a
//     native schema engine before any command, which `generate` does not
//     use: PRISMA_SCHEMA_ENGINE_BINARY points it at a placeholder (it
//     cannot run: og has no child_process). CHECKPOINT_DISABLE keeps it
//     from its telemetry request.
//   - `node file.js ...` and the bins of installed packages: the program
//     runs as Node would run it (jshost.RunProgram).
//   - Checkers and formatters (tsc, eslint, prettier, biome, ...) and shell
//     utilities are skipped: they produce nothing the build reads. So is a
//     command no installed package provides.
//
// A command that fails fails the build, as it fails npm run build.
func (b *builder) codegen(ctx context.Context, log func(string, ...any)) error {
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	data, err := os.ReadFile(filepath.Join(b.dir, "package.json"))
	if err != nil {
		return nil
	}
	_ = json.Unmarshal(data, &pkg)
	var cmds []npmscript.Command
	for _, name := range []string{"postinstall", "prepare", "prebuild"} {
		cmds = append(cmds, npmscript.Commands(pkg.Scripts, name)...)
	}
	for _, c := range npmscript.Commands(pkg.Scripts, "build") {
		if c.Args[0] == "next" && len(c.Args) > 1 && c.Args[1] == "build" {
			break
		}
		cmds = append(cmds, c)
	}
	ran := map[string]bool{}
	for _, c := range cmds {
		key := strings.Join(c.Args, " ")
		if ran[key] {
			continue
		}
		ran[key] = true
		if err := b.runScriptCommand(ctx, c, log); err != nil {
			return err
		}
	}
	return nil
}

// skippedCommands produce nothing a build reads (or are shell utilities).
var skippedCommands = map[string]bool{
	"next": true, "tsc": true, "vue-tsc": true, "eslint": true, "prettier": true, "biome": true, "oxlint": true,
	"stylelint": true, "jest": true, "vitest": true, "playwright": true, "cypress": true, "husky": true,
	"lint-staged": true, "echo": true, "rm": true, "rimraf": true, "mkdir": true, "cp": true, "mv": true,
	"cd": true, "true": true, "exit": true, "sleep": true, "printf": true, "test": true,
}

// commandTimeout bounds one script command (a watcher would never end).
const commandTimeout = 10 * time.Minute

func (b *builder) runScriptCommand(ctx context.Context, c npmscript.Command, log func(string, ...any)) error {
	name := c.Args[0]
	line := strings.Join(c.Args, " ")
	switch {
	case name == "prisma" && len(c.Args) > 1 && c.Args[1] == "generate":
		return b.runPrisma(ctx, c, log)
	case name == "prisma" && len(c.Args) > 2 && c.Args[1] == "db" && c.Args[2] == "push":
		// Can't push at build time (no datasource yet); record it so the app
		// pushes the schema at startup (see prismaMigrate). --accept-data-loss
		// carries through to schemaPush's force.
		b.prismaPush = &prismaPushConfig{acceptDataLoss: slices.Contains(c.Args, "--accept-data-loss")}
		log("prisma db push: will push the schema at app startup")
		return nil
	case skippedCommands[name]:
		log("skipping `%s` (nothing for the build)", line)
		return nil
	}
	var program string
	args := c.Args[1:]
	if name == "node" {
		// node [flags] file args...
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			args = args[1:]
		}
		if len(args) == 0 {
			log("skipping `%s`", line)
			return nil
		}
		program, args = filepath.Join(b.dir, args[0]), args[1:]
	} else {
		bin, ok := findBin(b.dir, name)
		if !ok {
			log("skipping `%s` (no installed package provides %s)", line, name)
			return nil
		}
		program = bin
	}
	log("running `%s`", line)
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	h := &jshost.Host{Dir: b.dir, Env: c.Env, Out: os.Stderr}
	code, err := h.RunProgram(ctx, program, args)
	if err != nil {
		return fmt.Errorf("next: `%s`: %w", line, err)
	}
	if code != 0 {
		return fmt.Errorf("next: `%s` exited with code %d", line, code)
	}
	return nil
}

// findBin is the file of the installed package bin named name: the
// package of that name first, then any top-level package declaring it.
func findBin(dir, name string) (string, bool) {
	if p, err := packageBin(dir, name, name); err == nil {
		return p, true
	}
	nm := filepath.Join(dir, "node_modules")
	entries, _ := os.ReadDir(nm)
	var pkgs []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "@") {
			scoped, _ := os.ReadDir(filepath.Join(nm, e.Name()))
			for _, s := range scoped {
				pkgs = append(pkgs, e.Name()+"/"+s.Name())
			}
		} else if !strings.HasPrefix(e.Name(), ".") {
			pkgs = append(pkgs, e.Name())
		}
	}
	for _, pkg := range pkgs {
		data, err := os.ReadFile(filepath.Join(nm, filepath.FromSlash(pkg), "package.json"))
		if err != nil || !strings.Contains(string(data), `"`+name+`"`) {
			continue
		}
		if p, err := packageBin(dir, pkg, name); err == nil {
			var many map[string]string
			var pj struct {
				Bin json.RawMessage `json:"bin"`
			}
			_ = json.Unmarshal(data, &pj)
			if json.Unmarshal(pj.Bin, &many) == nil && many[name] != "" {
				return p, true
			}
		}
	}
	return "", false
}

func (b *builder) runPrisma(ctx context.Context, c npmscript.Command, log func(string, ...any)) error {
	bin, err := packageBin(b.dir, "prisma", "prisma")
	if err != nil {
		return fmt.Errorf("next: `%s`: %w", strings.Join(c.Args, " "), err)
	}
	env := map[string]string{"CHECKPOINT_DISABLE": "1"}
	for k, v := range c.Env {
		env[k] = v
	}
	if _, set := os.LookupEnv("PRISMA_SCHEMA_ENGINE_BINARY"); !set {
		tmp, err := os.MkdirTemp("", "og-prisma-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		placeholder := filepath.Join(tmp, "schema-engine")
		if err := os.WriteFile(placeholder, []byte("#!/bin/sh\necho 'og: no Prisma schema engine (og runs prisma generate only)' >&2\nexit 1\n"), 0o755); err != nil {
			return err
		}
		env["PRISMA_SCHEMA_ENGINE_BINARY"] = placeholder
	}
	line := strings.Join(c.Args, " ")
	log("running `%s`", line)
	// `prisma generate` loads its TS config through @prisma/config -> c12 ->
	// jiti, whose config load flakes under CPU starvation in the build host:
	// it surfaces as Prisma's ConfigFileSyntaxError and a process.exit(1),
	// intermittently, on constrained runners (a 2-core CI box fails a build
	// here perhaps one run in three). Each run is a fresh VM, so a bounded
	// retry clears the flake without masking a genuinely broken config (which
	// fails every attempt and still surfaces). This is a mitigation; the
	// underlying build-host engine race is tracked in JOURNAL.md
	// ("Prisma 7 CI failures").
	const attempts = 3
	for i := 1; i <= attempts; i++ {
		h := &jshost.Host{Dir: b.dir, Env: env, Out: os.Stderr}
		var code int
		if code, err = h.RunProgram(ctx, bin, c.Args[1:]); err == nil && code == 0 {
			return nil
		} else if err == nil {
			err = fmt.Errorf("next: `%s` exited with code %d", line, code)
		} else {
			err = fmt.Errorf("next: `%s`: %w", line, err)
		}
		if ctx.Err() != nil {
			break // the timeout or a cancel, not the flake: do not retry
		}
		if i < attempts {
			log("`%s` failed (attempt %d/%d), retrying: %v", line, i, attempts, err)
		}
	}
	return err
}

// packageBin is the file of a package's bin command, from its package.json.
func packageBin(dir, pkgName, cmd string) (string, error) {
	pkgDir := filepath.Join(dir, "node_modules", filepath.FromSlash(pkgName))
	data, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return "", fmt.Errorf("%s is not installed", pkgName)
	}
	var pj struct {
		Bin json.RawMessage `json:"bin"`
	}
	_ = json.Unmarshal(data, &pj)
	var one string
	if json.Unmarshal(pj.Bin, &one) == nil && one != "" {
		return filepath.Join(pkgDir, filepath.FromSlash(one)), nil
	}
	var many map[string]string
	if json.Unmarshal(pj.Bin, &many) == nil && many[cmd] != "" {
		return filepath.Join(pkgDir, filepath.FromSlash(many[cmd])), nil
	}
	return "", fmt.Errorf("%s has no %q bin", pkgName, cmd)
}
