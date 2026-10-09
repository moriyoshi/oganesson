package gojatarget

import (
	"testing"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
)

// TestLoweredAsyncIteratorPrototype: the idiom reaching
// %AsyncIteratorPrototype% through an async generator function (whatwg-url,
// loaded by MongoDB's driver) works once esbuild lowers the function for
// Sobek, also with a comment inside the call. Node 24 gives
// "function,true,true,function".
func TestLoweredAsyncIteratorPrototype(t *testing.T) {
	src := `
const AIP = Object.getPrototypeOf(Object.getPrototypeOf(async function* () {}).prototype);
const it = Object.create(AIP);
// As @sec-ant/readable-stream writes it (Prisma 8's CLI loads it).
const AIP2 = Object.getPrototypeOf(
  Object.getPrototypeOf(
    /* istanbul ignore next */
    async function* () {
    }
  ).prototype
);
globalThis.result = [typeof AIP[Symbol.asyncIterator], it[Symbol.asyncIterator]() === it, AIP !== Object.prototype, typeof AIP2[Symbol.asyncIterator]].join(",");
`
	bo := api.BuildOptions{Stdin: &api.StdinOptions{Contents: src, Loader: api.LoaderJS}, Bundle: true, Write: false}
	Apply(&bo)
	res := api.Build(bo)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors[0].Text)
	}
	vm := sobek.New()
	if _, err := vm.RunString(Finish(string(res.OutputFiles[0].Contents))); err != nil {
		t.Fatal(err)
	}
	if got, want := vm.Get("result").String(), "function,true,true,function"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
