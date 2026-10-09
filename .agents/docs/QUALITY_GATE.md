# Quality gate

The standard verification an agent (or human) runs before declaring a change complete. Details of the suites live in [TESTING.md](./TESTING.md); this file is the "what do I run before I say done" checklist.

## 1. Go gate (always)

```sh
gofmt -l <changed files>      # must print nothing
go build -tags nodynamic ./...
go vet -tags nodynamic ./cmd/... ./internal/... ./runtime/... ./pack/...
go test -tags nodynamic -timeout 30m ./cmd/... ./runtime/... ./internal/... ./pack/...   # or the focused packages you touched
```

`-tags nodynamic` is how `og` ships (`scripts/build-og.sh`): without it, gen2brain/webp dlopens a system libwebp when one is installed, and the tests would exercise that library instead of the bundled pure-Go encoder.

Run `gofmt -w` on every Go file you changed before building. Fix violations and re-run until clean. Do not declare a change complete with a failing build, vet, or test.

- The gate runs without `-race` (the user's decision, 2026-10-07: under the race detector the fixture suites took 10 to 25 minutes, and it found nothing). With the fixtures installed, `internal/frameworks/vite` takes about 2 minutes and `internal/frameworks/next` a few; `-timeout 30m` leaves room. Use `-race` deliberately when changing concurrent Go code (the engine loop, workers, the KV and ISR services).
- Report skipped suites explicitly. A fixture test that skipped because `node_modules` is missing has not verified anything.
- Build outputs never go into the version-controlled tree. Write binaries to `./.agents-workspace/tmp`.

### The gate cannot see a test that proves nothing

Every command above answers one question: *did anything turn red?* None of them answers *would this have turned red if the code were wrong?* So before you run the gate:

1. **Audit the code, then write the test.** Read what the code actually does and state it. Pin that, not your intent for it.
2. **Ask what a PASS would look like if the claim were false.** If there is no answer, the check is not evidence yet, whatever it prints.
3. **Neutralize it.** Break the fix on purpose, confirm the test fails with the diagnostic you intended, then restore. ❌ A COMPILE error is not a valid neutralization.

## 2. Generated and vendored inputs

- Edited `runtime/webapi/js/src/**`: run `go generate ./runtime/webapi/` and include the regenerated `prelude.js`.
- Changed the Web API surface: run the WPT runner. If expectations change, review the `testdata/wpt-expectations.json` diff line by line before accepting `-wpt.update`.
- Added, removed, or bumped a Go module or a vendored JS package: run `go generate ./runtime/notices` and include the regenerated `THIRD_PARTY_NOTICES.txt` (`TestNoticesCoverLinkedModules` and CI fail otherwise). The script fails when a component has no license file; find its license before shipping it.
- Bumped a vendored JS package: change the pinned version in `scripts/vendor-js.sh`, re-run it, then run the whole suite. unenv and web-streams-polyfill are embedded into every bundle.
- Bumped Next.js in the fixture: expect template and manifest drift. Re-run `internal/frameworks/next` and check every patch in `patches.go` still applies. A patch that silently stops matching is a regression.

## 3. Browser check (UI-observable changes)

For changes affecting what a browser sees (client build, hydration, CSS, actions, navigation), build a packaged binary and drive it with Playwright as described in [TESTING.md](./TESTING.md#browser-verification). "The HTML looks right" is not evidence that hydration works. Check the console for errors and exercise the interaction.

## 4. Documentation

- Append a JOURNAL entry for any finished piece of work (`.agents/docs/JOURNAL.md`).
- Update `.agents/docs/TODO.md`: check off what you finished, add what you deferred.
- Repo-authored docs use half-width parentheses and colons. Check for decomposed kana:

  ```sh
  rg -n --pcre2 '[\x{3099}\x{309A}]' --glob '*.md' .
  ```

## Pick your level

| You touched | Run |
|-------------|-----|
| Docs only | section 4 |
| One Go package, no behavior visible to bundles | section 1, focused on that package |
| `runtime/**` or `internal/jshost/**` | section 1 over `./runtime/... ./internal/...` |
| `internal/frameworks/next/**` | section 1 including `internal/frameworks/next`, plus section 3 if the browser can observe it |
| Vendored JS, prelude, Next version | sections 1, 2, and 3 |
