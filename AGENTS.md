# Documents for both humans and coding agents

* [ARCHITECTURE.md](./ARCHITECTURE.md) ... system architecture (canonical; human-reader-ready).

# Documents for coding agents

* [./.agents/docs/OVERVIEW.md](./.agents/docs/OVERVIEW.md) ... project overview: goals, decisions already made, milestone map.
* [./.agents/docs/JOURNAL.md](./.agents/docs/JOURNAL.md) ... findings, insights, and peer code review history (append-only). Starts with the M0 to M5 milestone records and the `og:kv` design.
* [./.agents/docs/LTM/INDEX.md](./.agents/docs/LTM/INDEX.md) ... long-term memory index for durable project knowledge under `./.agents/docs/LTM/`.
* [./.agents/docs/TODO.md](./.agents/docs/TODO.md) ... open to-do items. Check and update this file when picking up or finishing work. Re-verify an entry against the tree before acting on it; the list goes stale in both directions.
* [./.agents/docs/TESTING.md](./.agents/docs/TESTING.md) ... testing guide: package suites, the WPT runner, fixture apps, and browser verification of built binaries.
* [./.agents/docs/QUALITY_GATE.md](./.agents/docs/QUALITY_GATE.md) ... the standard verification gate to run before declaring a change complete.

# Rules and protocols

## General

* **Naming.** The project is **Oganesson** (renamed from "gots" on 2026-10-06; older JOURNAL entries use the old name).
  - CLI and binary: `og`.
  - Go module: `github.com/moriyoshi/oganesson`.
  - Config file: `og.config.*`.
  - Built-in modules: `og:*` (e.g. `og:kv`).
  - Internal headers: `x-og-*`.
  - Build output: `.og/`.
  - JS hooks: `Symbol.for("og.*")`, `__og*` globals.
  - esbuild namespaces and plugin names: `og-*`.
  - Payload trailer: `OGPAYLD1`.
  - The checkout directory may still be named `gots`.

* Oganesson compiles a Next.js (later Vite) project into one self-contained Go executable. Read `./ARCHITECTURE.md` before changing subsystem boundaries.
* **Pure Go, no Node.** Neither `og build` nor the produced binary may require Node.js, npm, or any native addon. JavaScript that must run at build time runs in Sobek (`internal/jshost`); at runtime it runs in Sobek (`runtime/`).
* **Per-request isolated VMs.** Every request gets a fresh Sobek VM. Do not introduce state shared between request VMs except through deliberate Go-backed services (`runtime/kv`, `runtime/isr`).
* **Speed is not a criterion.** Prefer correctness and fidelity to `next start` over throughput.
* **Reuse prior work.** Before hand-writing a shim, loader, or polyfill, look for an existing library or upstream implementation and adopt it (as with goja_nodejs's `require` (vendored for Sobek), unenv, web-streams-polyfill, and Next's own templates and `loadConfig`). Vendor through `scripts/vendor-js.sh`, pinned to an exact version.
* **Use Next's own code over reimplementations.** Instantiate Next's entry templates and call its config/manifest helpers instead of re-deriving their behavior. When Next's code needs adjusting, patch it at bundle time (`internal/frameworks/next/patches.go`) and say why in a comment.
* The CLI is a small `flag`-based dispatcher (`cmd/og/main.go`). Keep new commands in that table.

## File Management

* When you'd make summary documents for your work, be sure to write them under `./.agents/docs`, not under `/tmp`.
* Temporary files should be created under `./.agents-workspace/tmp`, not under `/tmp`.
* ❌ Do not build binaries into the version-controlled tree (e.g. `go build -o og ./cmd/og` at the repo root). Always output to `./.agents-workspace/tmp` (e.g. `go build -o ./.agents-workspace/tmp/og ./cmd/og`). The same applies to packaged app binaries (`og build -o ./.agents-workspace/tmp/app ...`).
* ❌ Do not create feasibility experiments or spikes inside the version-controlled tree. Write them under `./.agents-workspace/tmp/spikes` instead (gitignored). The two spikes still tracked under `spikes/` are retained only because shipped code references them for provenance and regeneration: `spikes/esbuild-define-race` (`internal/frameworks/vite/esbuild.go`) and `spikes/m0/features` (`internal/gojatarget/gojatarget.go`).
* ❌ Never delete user files without permission. Only safe to delete: files YOU created in THIS session that are in `./.agents-workspace/tmp/`. Always ask first if unsure. Assume all pre-existing files belong to the user.
* Vendored third-party trees (`third_party/`, `runtime/webapi/js/vendor/`, `runtime/nodeshim/vendor/`) are produced by the scripts under `scripts/`. Do not hand-edit them; change the script or patch at load time instead (see `runtime/nodeshim/nodeshim.go`).

## Building

* Go 1.27 is on `PATH` (`/opt/homebrew/bin/go`); the module's `go 1.27.0` directive governs the toolchain.
* `go build ./...` needs `third_party/typescript` (the `go.mod` `replace` for TypeScript-Go). Populate it with `scripts/sync-typescript.sh`, which also installs the `_tsoverlay/ogapi` facade at the pinned commit. Building inside the vendored tree needs `GOWORK=off`.
* Run `gofmt -w` on every Go file you change before running `go build`, `go vet`, or `go test`, and before reporting a change as done.
* The standard local gate for any Go change you make — this applies to subagents too:
  ```
  gofmt -l <changed files>      # must print nothing
  go build -tags nodynamic ./...
  go vet -tags nodynamic ./cmd/... ./internal/... ./runtime/... ./pack/...
  go test -tags nodynamic ./cmd/... ./runtime/... ./internal/... ./pack/...   # or a focused package
  ```
  Fix violations and re-run until clean. Do not declare a change complete with failing build, vet, or tests. Feasibility experiments live under `./.agents-workspace/tmp/spikes` (see File Management) and are not part of the gate.
* Build `og` itself with `scripts/build-og.sh [out] [goos/goarch]` (CGO off, `-trimpath`, `-tags nodynamic`). `og build -o` copies the running `og` into every packaged app, so this is also how app binaries are built. `nodynamic` keeps gen2brain/webp from dlopening a system libwebp, which is also why the gate uses the tag.
* JS sources under `runtime/webapi/js/src` are TypeScript compiled into `prelude.js`; regenerate it with `go generate ./runtime/webapi/` after editing them.

## Testing

* Make sure that regression tests are ready for your fix.
* **Audit the code before you write the test for it.** Read what the code actually does and state it, then write the test against that. A test written from your model of the fix inherits that model's blind spots, and a test that passes for the wrong reason is worse than no test.
* Before trusting any check — a new test, a `grep`, a curl probe, a built binary — ask **what would a PASS look like if this claim were false?** If there is no answer, the check proves nothing yet. Then neutralize it: deliberately break the fix and confirm the test fails with the diagnostic you intended. ❌ A COMPILE error is not a valid neutralization.
* Tests must not require Node.js or network access in the default path. Fixture-based tests (`internal/frameworks/next`, `internal/jshost`) skip when the fixture's `node_modules` is absent; install fixtures with `go run ./cmd/og install <dir>` (this one step uses the network). The WPT runner needs `scripts/sync-wpt.sh`.
* A skipped test is not a passing test. When you report results, say which suites skipped and why.
* For behavior that only shows up in a browser (hydration, soft navigation, server actions, CSS), build a packaged binary and verify it with the Playwright MCP tools. See [./.agents/docs/TESTING.md](./.agents/docs/TESTING.md).

## Git Workflow

* ❌ Do not run `git checkout` or `git restore` against the working tree — another agent may be working concurrently in the same directory.
* ❌ Never make discretionary commits. Commit or push only when the user explicitly asks.

## Documentation

* Try to write your work summary to one of the existing documents under `./.agents/docs`. A finished piece of work gets a `JOURNAL.md` entry: what changed, why, the files, the tests, and the pitfalls found.
* ❌ Avoid editing any existing sections of `JOURNAL.md`. Append new entries to the end. (The sole exception is the `reconcile-journal-ltm` skill, which may remove entries already consolidated into `.agents/docs/LTM/` per the canonical `## LTM Consolidation Record`.)
* ❌ For repo-authored documentation only (e.g. `AGENTS.md`, `ARCHITECTURE.md`, `.agents/docs/**`), never use full-width parentheses (`（` `）`). Use half-width parentheses (`(` `)`) with a half-width space before/after when adjacent to a non-whitespace character.
* ❌ For repo-authored documentation only, never use full-width colons (`：`). Use a half-width colon followed by a half-width space.
* ❌ Never emit decomposed Japanese. Always write CJK text in NFKC normal form. Check with:

  ```sh
  rg -n --pcre2 '[\x{3099}\x{309A}]' --glob '*.md' .
  ```

## Memory skills

The agent memory cycle lives in `.agents/skills/` (exposed to Claude Code through the `.claude/skills` symlink):

* `good-sleep` ... consolidate `JOURNAL.md` into topic documents under `.agents/docs/LTM/` and extract to-dos into `TODO.md`.
* `deep-sleep` ... merge overlapping LTM documents into synthesis documents.
* `reconcile-journal-ltm` ... audit JOURNAL against LTM, fill gaps, and collapse consolidation records.
* `distill-memories` ... promote durable knowledge into `OVERVIEW.md` and `ARCHITECTURE.md`.
* `tackle-todos` ... sweep `TODO.md` and `// TODO` comments and resolve items in parallel.

## Shell Pitfalls (prezto defaults)

The user's shell uses prezto, which sets aliases and options that break non-interactive scripts:

* ❌ `cp src dst` prompts interactively when `dst` exists (prezto aliases `cp` to `cp -i`). Always `rm -f dst` before `cp`. Also kill any process using the destination file first (e.g. a running packaged app binary).
* ❌ `cat > file <<'EOF'` and `echo > file` fail with `file exists` when the target exists (prezto enables `NO_CLOBBER`). Workaround: `rm -f file` before writing, or use `>|`, `tee`, or `/bin/cat`.
* ❌ `rm file` prompts for confirmation on some files (prezto aliases `rm` to `rm -i`). Always use `rm -f` for non-interactive deletion.
* ❌ An unquoted word starting with `=` (e.g. `echo =====`) is zsh `=command` expansion and fails with `not found`. Quote it.
