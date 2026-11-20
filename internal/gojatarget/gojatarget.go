// Package gojatarget configures esbuild and source fixups for scripts
// executed by Oganesson's embedded JavaScript backend.
package gojatarget

import "github.com/evanw/esbuild/pkg/api"

// Backend selects a transpilation profile. The zero value uses the backend
// selected by the build tag.
type Backend uint8

const (
	Default Backend = iota
	Sobek
	Ekuma
)

func (b Backend) resolved() Backend {
	if b == Default {
		return backend
	}
	return b
}

// TransformOptions returns esbuild transform options for the built backend.
func TransformOptions(loader api.Loader, sourcefile string) api.TransformOptions {
	return api.TransformOptions{
		Loader:     loader,
		Target:     Target,
		Supported:  backend.supported(),
		Charset:    Charset,
		Sourcefile: sourcefile,
	}
}

// Sobek's regexp flags are lowered through the prelude's RegExp constructor.
// Its native logical assignment can lose the write when the RHS awaits.
var sobekUnsupported = []string{
	"async-generator",
	"logical-assignment",
	"regexp-match-indices",
	"regexp-set-notation",
	"using",
}

// Target is the esbuild language target before backend overrides.
const Target = api.ESNext

func (b Backend) supported() map[string]bool {
	m := map[string]bool{
		// Keep esbuild's decorator lowering consistent with the build tools.
		"decorators": false,
		// Both backends run scripts through Oganesson's module loader.
		"dynamic-import":  false,
		"import-meta":     false,
		"top-level-await": false,
	}
	if b.resolved() == Sobek {
		for _, feature := range sobekUnsupported {
			m[feature] = false
		}
	}
	return m
}

// Charset must be UTF-8: Goja's parser panics on string literals that mix a
// \x escape with an astral \u{...} escape (e.g. "\x00\u{10FFFF}"), which
// esbuild's default ASCII output produces. Raw UTF-8 avoids those escapes.
const Charset = api.CharsetUTF8

// Apply sets the built backend's profile on build options.
func Apply(o *api.BuildOptions) {
	backend.Apply(o)
}

// Apply sets b's profile on build options.
func (b Backend) Apply(o *api.BuildOptions) {
	o.Target = Target
	o.Supported = b.supported()
	o.Charset = Charset
	o.Platform = api.PlatformNeutral
	if o.Format == api.FormatDefault {
		o.Format = api.FormatIIFE
	}
}
