package jshost

import (
	"context"
	"testing"
)

// TestThenableAsyncContext: a thenable's then runs in the async context it
// was resolved in, as in Node (Drizzle's query builders, Astro's render
// scope). The engine's thenable job drops it; gojatarget.ContextualThenables
// restores it for then methods (class, static, async, object literal,
// then: function). Node 24 gives "S" everywhere; then methods using super
// and arrow-valued then properties are the workaround's known gaps.
func TestThenableAsyncContext(t *testing.T) {
	h := &Host{Dir: t.TempDir()}
	code, err := h.Bundle("", `import { AsyncLocalStorage } from "node:async_hooks";
const als = new AsyncLocalStorage();
const out = [];
class Query {
  constructor(v) { this.v = v; }
  then(onFulfilled, onRejected) { out.push("class:" + als.getStore()); return Promise.resolve(this.v).then(onFulfilled, onRejected); }
}
class Static { static then(r) { out.push("static:" + als.getStore()); r("s"); } }
class AsyncThen { async then(r) { out.push("async:" + als.getStore()); r("a"); } }
const lit = { then(r) { out.push("literal:" + als.getStore()); r("l"); } };
const prop = { "then": function (r) { out.push("prop:" + als.getStore()); r("p"); } };
class Base { then(r) { r("base"); } }
class Sub extends Base { then(r) { out.push("super:" + als.getStore()); super.then(r); } }
const arrow = { then: (r) => { out.push("arrow:" + als.getStore()); r("arr"); } };
globalThis.result = als.run("S", async () => {
  const got = [await new Query(1), await Static, await new AsyncThen(), await lit, await prop, await new Sub(), await arrow];
  await new Promise((res) => res(new Query(2)));
  await Promise.resolve(lit);
  const q = new Query(3);
  const desc = Object.getOwnPropertyDescriptor(Query.prototype, "then");
  q.then = function (r) { r("own"); };
  got.push(await q, typeof new Query(0).then, new Query(0).then.name, new Query(0).then.length, typeof desc, Object.keys(lit).join());
  return got.join(",");
}).then((v) => v + " | " + out.join(" "));
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	want := "1,s,a,l,p,base,arr,own,function,then,2,object,then | class:S static:S async:S literal:S prop:S super:undefined arrow:undefined class:S literal:S"
	if v != want {
		t.Fatalf("got\n%v\nwant\n%s", v, want)
	}
}
