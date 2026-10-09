# User Documentation and VitePress Site

## Summary

The `docs/` tree is Oganesson's user-facing source of truth, while the root
README is a concise landing page that points into those topical guides. The
same Markdown files form a VitePress site, built both by Node for authoring and
by Oganesson as a compatibility fixture, then deployed from tagged releases to
GitHub Pages.

## Key Facts

- `docs/index.md` is the website landing page; `docs/introduction.md` holds the
  repository-readable guide map and feature matrix.
- Guides cover getting started, every CLI command and flag, configuration,
  Next.js, Vite ecosystems, runtime compatibility, data/ORMs, native Go
  modules, deployment, capabilities, and known limitations.
- The root `README.md` deliberately avoids duplicating the feature manual.
- VitePress is isolated under `docs/package.json` and `docs/package-lock.json`;
  it is an optional documentation toolchain and not part of `og build` or the
  packaged runtime.
- The site uses the canonical Oganesson logo and a small default-theme
  extension. No duplicate branding asset is maintained under `docs/`.
- Tagged releases build the site with base `/` and deploy it through the
  protected `github-pages` environment because the project uses a custom
  domain.

## Details

### Documentation structure

The detailed product surface lives in `docs/`, not in parallel README prose.
The guides were checked against the CLI, runtime declarations and tests,
fixture inventory, architecture, LTM, and open TODOs so partial compatibility
and process-local persistence are described accurately. `docs/can-i-do-this.md`
provides a task-oriented tour and links to the deeper compatibility guide for
unsupported or constrained behavior.

The original guide index was renamed from `docs/README.md` to `docs/index.md`.
Once the VitePress home layout made that file a focused landing page, the guide
overview and feature map moved to `docs/introduction.md`. Root README links point
to the explicit guide files rather than maintaining a second reference manual.

### VitePress authoring site

`docs/.vitepress/config.mts` provides navigation, a complete topical sidebar,
local search, page outlines, edit links, clean URLs, social/footer metadata, and
strict dead-link checking. Files outside the VitePress source root are linked
through explicit GitHub URLs. The pinned toolchain is VitePress
2.0.0-alpha.20 with Vite 8.3.4: VitePress 1.6.4 pulled a dependency set with
three npm advisories, including a high-severity development-server traversal,
while the pinned `next` release produced a zero-advisory tree at selection time.

The home hero presents the project, its single-executable/no-Node positioning,
and primary guide actions. It reuses `assets/oganesson-logo.svg` through
VitePress's public directory setting. `docs/.vitepress/theme/` extends the
default theme with the logo-derived teal-and-rose palette, light/dark brand
colors, a restrained hero glow, and bounded logo sizing.

### Oganesson builds its own documentation

The docs project also exercises the VitePress adapter described in
[[vite-and-rolldown-facade]]. `og build docs` produces the static site in
`docs/.og`, and `og serve docs` serves the landing page, capability guide, Vite
guide, and logo. The checked-in site configuration disables three optional
paths that the build host does not yet support: `lastUpdated` (Git via
`child_process.spawnSync`), Markdown-it automatic bare-URL linkification, and
Vite 8's native Lightning CSS minifier. Links are explicit Markdown and the
small theme ships readable CSS.

### GitHub Pages deployment

The tag-driven release workflow builds the documentation only after the shared
CI gate. `build-docs` checks out the tag, installs Node 24 with npm caching,
runs `npm ci`, builds with `--base /`, and uploads
`docs/.vitepress/dist` as the Pages artifact. `deploy-docs` uses the protected
`github-pages` environment with only `pages: write` and `id-token: write`.
Job-level deployment concurrency permits an in-progress production deployment
to finish. Third-party actions are pinned to immutable commits.

The custom-domain deployment requires root URLs, not the `/oganesson/` prefix
used by a default GitHub project site. Validation checks the generated HTML for
root navigation/asset URLs and rejects a deliberately wrong base. The artifact
tree must contain no symlinks.

## Files

- `README.md` — concise project landing page and links to maintained guides.
- `docs/index.md` — VitePress home page.
- `docs/introduction.md` — guide index and feature map.
- `docs/can-i-do-this.md` — task-oriented capability guide.
- `docs/{getting-started,cli,configuration,nextjs,vite,runtime,data,native-go-modules,deployment,compatibility}.md` — focused user guides.
- `docs/.vitepress/config.mts` — site navigation, search, links, and public
  directory configuration.
- `docs/.vitepress/theme/` — default-theme extension and brand styling.
- `docs/package.json`, `docs/package-lock.json` — isolated documentation
  toolchain.
- `.github/workflows/release.yml` — Pages build and deployment jobs.

## Test Coverage

- `npm run build` succeeds with strict dead-link checking and renders every
  page.
- `npm audit --audit-level=moderate` reported zero vulnerabilities for the
  pinned documentation dependency tree.
- Local Markdown links and anchors in the root README and `docs/*.md` resolve.
- The rendered home page contains the hero, actions, feature cards, navigation
  logo, and hero logo; the output includes both canonical logo formats.
- `npm run build -- --base /` produces root URLs. A build with
  `--base /wrong-base/` is rejected by the deployment assertion.
- `TestBuildVitePress` and the final `og build docs` / `og serve docs` checks
  cover Oganesson's own build path; see [[vite-and-rolldown-facade]].

## Pitfalls

- Keep detailed capability claims in the topical guides; duplicating them in
  README recreates a second source that drifts.
- Files outside `docs/` are not VitePress routes. Link them through their
  repository URL rather than a relative source-root escape.
- The custom domain requires `--base /`; a repository-scoped Pages site would
  instead need `/oganesson/`.
- VitePress `lastUpdated`, automatic linkification, and the native Lightning
  CSS minifier remain disabled until their build-host gaps are closed.
