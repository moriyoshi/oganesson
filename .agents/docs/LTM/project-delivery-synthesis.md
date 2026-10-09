# Project Delivery Synthesis

## Summary

Oganesson's delivery system joins repository process, verification, release
engineering, license attribution, and user documentation. Canonical engineering
knowledge lives in `ARCHITECTURE.md` and `.agents/docs`; product documentation
lives in `docs/` and is itself built by Oganesson through the VitePress adapter.
Tagged releases are the sole binary and website publication path.

## Included Documents

| Document | Focus |
|----------|-------|
| [harness-and-project-meta.md](./harness-and-project-meta.md) | Repository rules, rename, CI/CD, notices, and license collection. |
| [user-documentation-and-site.md](./user-documentation-and-site.md) | User guides, VitePress site, branding, and Pages deployment. |

## Stable Knowledge

- The project is Oganesson. Public and internal names use `og`, `og:*`,
  `og.config.*`, `.og/`, `x-og-*`, `Symbol.for("og.*")`, `__og*`, `og-*`, and
  the `OGPAYLD1` trailer. Older journal entries may use the former name.
- `ARCHITECTURE.md` is canonical for subsystem boundaries. `.agents/docs`
  carries project overview, testing and quality-gate procedures, open work, the
  append-only journal, and LTM. Memory skills maintain those layers.
- CI runs formatting/vet/generated-file checks and sharded tests on Linux. It
  does not build release binaries. Fixture tests report skips and must not count
  absent `node_modules` as success.
- Tagged release workflows wait for CI, build six OS/architecture binaries with
  `scripts/build-og.sh`, validate embedded metadata, create platform archives
  and `SHA256SUMS`, and publish the full set atomically.
- `scripts/collect-notices.sh` produces deterministic embedded third-party
  notices covering the release-target dependency union, TypeScript, vendored
  Go/JavaScript, and generated Unicode data. `og licenses`, packaged app
  `-licenses`, and ejected modules expose them.
- Application npm licenses are collected from shipped esbuild inputs plus
  build-host reads of package CSS/fonts. Synthetic notices cover downloaded
  Google Fonts. Build-only dependencies should not be attributed as shipped
  application code.
- The root README is a compact landing page. Detailed public behavior belongs
  in `docs/`, with `docs/index.md` as the website home and
  `docs/introduction.md` as the guide map and feature overview.
- The documentation site uses an isolated VitePress toolchain and the canonical
  branding assets. It can be built by Node for authoring and by `og build docs`
  as a compatibility fixture.
- Release tags build the site with root base `/` and deploy through the
  protected `github-pages` environment for the custom domain.
- VitePress features needing unsupported build-host behavior remain disabled:
  git-based `lastUpdated`, automatic bare-URL linkification, and Vite 8's native
  Lightning CSS minifier.

## Operational Guidance

- Put architectural decisions in `ARCHITECTURE.md`, accumulated implementation
  findings in the journal/LTM cycle, open work in `TODO.md`, and user-facing
  claims in the topical `docs/` guide. Avoid duplicating a feature matrix in
  the README.
- Follow `.agents/docs/QUALITY_GATE.md` and report skipped fixture suites. Run
  `gofmt` before build, vet, or test for every Go change.
- Regenerate and diff notices whenever dependencies, vendored code, generated
  Unicode data, or license discovery changes. Missing attribution is a failure;
  a cross-target superset is acceptable.
- Keep release outputs outside the checkout so Go's VCS stamp is not marked
  dirty and cross-target runtime source validation remains stable.
- For documentation edits, validate local Markdown links and the strict
  VitePress build. Keep repository-external files linked through GitHub URLs
  because they are not VitePress routes.
- Changes to the release workflow should preserve least-privilege deployment,
  immutable action pins, artifact completeness, and explicit target metadata
  checks.

## Files

- `AGENTS.md`, `ARCHITECTURE.md`, `.agents/docs/*`, `.agents/skills/*` -
  engineering process and durable memory.
- `.github/workflows/ci.yml`, `.github/workflows/release.yml`,
  `.github/actions/typescript` - CI, release, Pages, and TypeScript-Go setup.
- `scripts/build-og.sh`, `scripts/collect-notices.sh`,
  `scripts/install-fixtures.sh` - builds, attribution, and fixture setup.
- `runtime/notices`, `internal/licenses`, `NOTICE` - embedded and app-specific
  license reporting.
- `README.md`, `docs/*.md`, `docs/.vitepress`, `docs/package.json` - user
  documentation and site.
- `assets/branding/oganesson-logo.svg` - canonical logo.

## Tests

- Repository gates are defined in `.agents/docs/QUALITY_GATE.md`; fixture and
  browser procedures are in `.agents/docs/TESTING.md`.
- License guards include `TestNoticesCoverLinkedModules`,
  `TestNoticesCoverVendoredJS`, `internal/licenses` tests,
  `TestBuildReactNotices`, `TestFixtureNotices`, and `TestWrite`.
- Documentation checks include strict `npm run build`, link/anchor validation,
  root-base assertions, and `TestBuildVitePress` plus `og build docs` serving.

## Pitfalls

- Do not hand-edit generated notices, generated runtime prelude, vendored trees,
  or the TypeScript-Go checkout; use their owning scripts.
- Re-eject preserves a user's `main.go`, so older ejected applications do not
  automatically gain newer generated command-line behavior.
- Astro static license collection can over-attribute packages used only during
  prerender. This is known and safer than missing a shipped dependency.
- The installed Next suite may exceed Go's default timeout. Use the documented
  gate timeout and diagnose the actual panic line, not an arbitrary blocked
  goroutine from the timeout dump.
- The custom documentation domain requires site base `/`; a project-scoped
  Pages deployment would require a repository prefix instead.
