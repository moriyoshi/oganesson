// Package gojatarget describes the JavaScript dialect Goja executes, as esbuild
// options. Every bundle Oganesson produces for Goja is built with these settings.
//
// The table comes from spikes/m0/features (see the M0 entry in .agents/docs/JOURNAL.md); rerun it
// when bumping Goja.
package gojatarget

import "github.com/evanw/esbuild/pkg/api"

// TransformOptions returns esbuild transform options for Goja (no bundling).
func TransformOptions(loader api.Loader, sourcefile string) api.TransformOptions {
	return api.TransformOptions{
		Loader:     loader,
		Target:     Target,
		Supported:  Supported(),
		Charset:    Charset,
		Sourcefile: sourcefile,
	}
}

// Unsupported lists esbuild features that the engine (Sobek, a Goja fork)
// lacks and esbuild must lower.
//
// Regexp features are listed so esbuild routes such literals through the
// RegExp constructor instead of failing at parse time: the runtime
// prelude's RegExp runs the v flag as u where that means the same and
// emulates the d flag (runtime/webapi/js/src/regexp.ts). Sobek implements
// Unicode property escapes itself.
var Unsupported = []string{
	"async-generator",
	"decorators",
	"dynamic-import", // no module loader; esbuild lowers import() to require()
	"import-meta",
	// The engine loses the assignment of x ??= / ||= / &&= when the
	// right-hand side awaits (async function f(x) { x ??= (await p).y;
	// return x } can return undefined; Sobek as Goja, in other shapes than
	// that one); esbuild's lowering (x ?? (x = …)) runs correctly.
	"logical-assignment",
	"regexp-match-indices",
	"regexp-set-notation",
	"top-level-await",
	"using",
}

// Target is the esbuild language target; Unsupported narrows it.
const Target = api.ESNext

// Supported returns the esbuild Supported map for the engine.
func Supported() map[string]bool {
	m := make(map[string]bool, len(Unsupported))
	for _, f := range Unsupported {
		m[f] = false
	}
	return m
}

// Charset must be UTF-8: Goja's parser panics on string literals that mix a
// \x escape with an astral \u{...} escape (e.g. "\x00\u{10FFFF}"), which
// esbuild's default ASCII output produces. Raw UTF-8 avoids those escapes.
const Charset = api.CharsetUTF8

// Apply sets the Goja target on build options.
func Apply(o *api.BuildOptions) {
	o.Target = Target
	o.Supported = Supported()
	o.Charset = Charset
	o.Platform = api.PlatformNeutral
	if o.Format == api.FormatDefault {
		o.Format = api.FormatIIFE
	}
}
