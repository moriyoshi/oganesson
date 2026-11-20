package gojatarget

import (
	"maps"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/jsvm"
)

func TestBackendPipeline(t *testing.T) {
	const source = `
async function* generate() { yield 1; }
async function assign(x) { x ??= await Promise.resolve(2); return x; }
function dispose() { using resource = { [Symbol.dispose]() {} }; }
globalThis.result = [/a/d, /[\p{ASCII}&&\p{Letter}]/v, generate, assign, dispose];
`
	transform := TransformOptions(api.LoaderJS, "backend.js")
	build := api.BuildOptions{Stdin: &api.StdinOptions{Contents: source, Loader: api.LoaderJS}, Bundle: true, Write: false}
	Apply(&build)
	if !maps.Equal(transform.Supported, build.Supported) {
		t.Fatal("bundle and script transforms selected different profiles")
	}
	for _, name := range []string{"decorators", "dynamic-import", "import-meta", "top-level-await"} {
		if supported, ok := build.Supported[name]; !ok || supported {
			t.Errorf("shared script constraint %q was lost", name)
		}
	}

	result := api.Build(build)
	if len(result.Errors) != 0 {
		t.Fatal(result.Errors)
	}
	code := string(result.OutputFiles[0].Contents)
	for _, syntax := range []string{"async function*", "??=", "using resource", "/a/d", `/[\p{ASCII}&&\p{Letter}]/v`} {
		if got := strings.Contains(code, syntax); got != jsvm.UsesEkuma {
			t.Errorf("output contains %q = %v, want %v", syntax, got, jsvm.UsesEkuma)
		}
	}
	transformed := api.Transform(source, transform)
	if len(transformed.Errors) != 0 {
		t.Fatal(transformed.Errors)
	}
	if got := strings.Contains(string(transformed.Code), "??="); got != jsvm.UsesEkuma {
		t.Errorf("script output contains native logical assignment = %v, want %v", got, jsvm.UsesEkuma)
	}
}

func TestBackendThenableDescriptor(t *testing.T) {
	const source = `Object.prototype.hasOwnProperty.call(Object.getOwnPropertyDescriptor({ then(resolve) { resolve(1); } }, "then"), "value");`
	code := Finish(source)
	if jsvm.UsesEkuma && code != source {
		t.Fatal("rewrote the then method on Ekuma")
	}
	vm := jsvm.New()
	value, err := vm.RunString(code)
	if err != nil {
		t.Fatal(err)
	}
	if got := value.ToBoolean(); got != jsvm.UsesEkuma {
		t.Errorf("has data descriptor = %v, want %v", got, jsvm.UsesEkuma)
	}
}
