package native_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/native"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

type user struct {
	Name   string   `json:"name"`
	Emails []string `json:"emails,omitempty"`
	Secret string   `json:"-"`
	Age    int      `json:"age"`
}

type holder struct {
	UserID int64 `json:"userId"`
}

type base struct {
	ID int64 `json:"id"`
}

// record embeds base: its promoted id and own size are both int64, so a struct
// with an embedded field must still keep nested bigint precision (the walk
// promotes plain embedded structs rather than falling back to JSON).
type record struct {
	base
	Size int64 `json:"size"`
}

// account exercises nested int64/uint64 (in a struct field, a slice, a map and
// through a pointer) alongside the JSON-form nested types (time.Time, []byte).
type account struct {
	ID      int64            `json:"id"`
	Balance uint64           `json:"balance"`
	Tags    []int64          `json:"tags"`
	Meta    map[string]int64 `json:"meta,omitempty"`
	Owner   *holder          `json:"owner"`
	When    time.Time        `json:"when"`
	Data    []byte           `json:"data,omitempty"`
}

func init() {
	native.Register(native.Module{Name: "ogtest", Funcs: []native.Func{
		{Name: "greet", Fn: func(name string) string { return "hello " + name }},
		{Name: "add", Fn: func(a, b int) int { return a + b }},
		{Name: "upper", Fn: func(b []byte) []byte { return []byte(strings.ToUpper(string(b))) }},
		{Name: "later", Fn: func(t time.Time) time.Time { return t.Add(time.Hour) }},
		{Name: "older", Fn: func(u user) (user, error) {
			if u.Age < 0 {
				return user{}, errors.New("negative age")
			}
			u.Age++
			u.Secret = "hidden"
			return u, nil
		}},
		{Name: "bignum", Fn: func(x int64) int64 { return x + 1 }},
		{Name: "half", Fn: func(x uint64) uint64 { return x / 2 }},
		{Name: "makeAccount", Fn: func() account {
			return account{
				ID:      9007199254740993, // 2^53 + 1
				Balance: 18446744073709551615,
				Tags:    []int64{9007199254740994, 7},
				Meta:    map[string]int64{"x": 9007199254740995},
				Owner:   &holder{UserID: 9007199254740996},
				When:    time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
				Data:    []byte("hi"),
			}
		}},
		{Name: "ownerId", Fn: func(a account) int64 { return a.Owner.UserID }},
		{Name: "firstTag", Fn: func(a account) int64 { return a.Tags[0] }},
		{Name: "metaX", Fn: func(a account) int64 { return a.Meta["x"] }},
		{Name: "makeRecord", Fn: func() record {
			return record{base: base{ID: 9007199254741001}, Size: 9007199254741002}
		}},
		{Name: "boom", Fn: func() { panic("kaboom") }},
		{Name: "nothing", Fn: func() error { return nil }},
		{Name: "wait", Async: true, Fn: func(ctx context.Context, ms int) (string, error) {
			select {
			case <-time.After(time.Duration(ms) * time.Millisecond):
				return "waited", ctx.Err()
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}},
		{Name: "fail", Async: true, Fn: func(ctx context.Context) error { return errors.New("async failure") }},
	}})
}

// TestNativeNestedBigInt: int64/uint64 keep full 64-bit precision at any depth,
// not only at the top level — in a struct field, a slice, a map and through a
// pointer. toJS renders them as bigints (makeAccount's values are all beyond
// 2^53, so a number would round them); nested time.Time and []byte keep their
// JSON forms (an RFC 3339 string, a base64 string). toGo carries a nested
// bigint back through the JSON round-trip precisely (ownerId/firstTag/metaX read
// a nested field off the object makeAccount produced, and off a hand-built one).
func TestNativeNestedBigInt(t *testing.T) {
	got := run(t, `
import { makeAccount, ownerId, firstTag, metaX, makeRecord } from "og:ogtest";
const a = makeAccount();
const out = [];
// toJS: nested int64/uint64 are bigints with full precision.
out.push("id " + typeof a.id + " " + a.id);
out.push("balance " + typeof a.balance + " " + a.balance);
out.push("tag " + typeof a.tags[0] + " " + a.tags[0]);
out.push("meta " + typeof a.meta.x + " " + a.meta.x);
out.push("owner " + typeof a.owner.userId + " " + a.owner.userId);
// nested time.Time / []byte keep their JSON (string) forms.
out.push("when " + typeof a.when + " " + a.when);
out.push("data " + typeof a.data + " " + a.data);
// toGo: the object round-trips back with its nested bigints intact.
out.push("rt " + ownerId(a) + " " + firstTag(a) + " " + metaX(a));
// toGo: a hand-built object with a nested bigint field.
out.push("built " + ownerId({ owner: { userId: 9007199254740999n } }));
// An embedded struct's promoted int64 and the outer int64 both keep precision.
const rec = makeRecord();
out.push("rec " + typeof rec.id + " " + rec.id + " " + typeof rec.size + " " + rec.size);
globalThis.result = out.join("\n");
`)
	want := strings.Join([]string{
		"id bigint 9007199254740993",
		"balance bigint 18446744073709551615",
		"tag bigint 9007199254740994",
		"meta bigint 9007199254740995",
		"owner bigint 9007199254740996",
		"when string 2024-01-02T03:04:05Z",
		"data string aGk=",
		"rt 9007199254740996 9007199254740994 9007199254740995",
		"built 9007199254740999",
		"rec bigint 9007199254741001 bigint 9007199254741002",
	}, "\n")
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// run bundles src (resolving og:* with native.Plugin) and evaluates it,
// awaiting globalThis.result.
func run(t *testing.T, src string) string {
	t.Helper()
	bo := api.BuildOptions{
		Stdin:   &api.StdinOptions{Contents: src, Loader: api.LoaderJS},
		Bundle:  true,
		Write:   false,
		Plugins: []api.Plugin{native.Plugin([]string{"ogtest", "absent"})},
	}
	gojatarget.Apply(&bo)
	res := api.Build(bo)
	if len(res.Errors) > 0 {
		t.Fatal(res.Errors[0].Text)
	}
	e := engine.New(engine.Options{})
	defer e.Close()
	if _, err := webapi.Install(e, webapi.Options{Stdout: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if err := native.Install(e); err != nil {
		t.Fatal(err)
	}
	if _, err := e.VM.RunString(string(res.OutputFiles[0].Contents)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := e.Await(ctx, e.VM.Get("result"))
	if err != nil {
		t.Fatal(err)
	}
	return v.String()
}

// TestNativeModule: a registered Go module imported as og:ogtest:
// conversions both ways (numbers, strings, []byte as Uint8Array, time.Time
// as Date, structs by json tags), errors and panics thrown, asynchronous
// functions as Promises off the loop with a context, and a module the
// binary does not have.
func TestNativeModule(t *testing.T) {
	got := run(t, `
import { greet, add, upper, later, older, boom, nothing, wait, fail } from "og:ogtest";
const out = [];
const t = (label, f) => { try { out.push(label + ": " + JSON.stringify(f())); } catch (e) { out.push(label + ": " + e.name + " " + e.message); } };
t("greet", () => greet("og"));
t("add", () => add(2, 40));
t("upper", () => { const r = upper(new TextEncoder().encode("abc")); return [r instanceof Uint8Array, new TextDecoder().decode(r)]; });
t("later", () => { const r = later(new Date(Date.UTC(2026, 9, 9, 1, 2, 3))); return [r instanceof Date, r.toISOString()]; });
t("older", () => older({ name: "ann", age: 41, secret: "x" }));
t("older error", () => older({ name: "bob", age: -1 }));
t("bad argument", () => add("x", 1));
t("boom", () => boom());
t("nothing", () => nothing());
globalThis.result = (async () => {
  const p = wait(20);
  out.push("wait is a promise: " + (p instanceof Promise));
  out.push("wait: " + (await p));
  try { await fail(); } catch (e) { out.push("fail: " + e.message); }
  try { await import("og:absent"); } catch (e) { out.push("absent: " + e.message.slice(0, 40)); }
  return out.join("\n");
})();
`)
	want := strings.Join([]string{
		`greet: "hello og"`,
		`add: 42`,
		`upper: [true,"ABC"]`,
		`later: [true,"2026-10-09T02:02:03.000Z"]`,
		`older: {"name":"ann","age":42}`,
		`older error: Error negative age`,
		`bad argument: TypeError og:ogtest.add: argument 1: json: cannot unmarshal string into Go value of type int`,
		`boom: Error panic: kaboom`,
		`nothing: undefined`,
		`wait is a promise: true`,
		`wait: waited`,
		`fail: async failure`,
		`absent: og: no native module og:absent in this b`,
	}, "\n")
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// TestNativeBigInt: a top-level int64/uint64 crosses as a JS bigint, so
// values beyond 2^53 keep full precision in both directions. 9007199254740992
// is 2^53 (the last integer a JS number represents exactly); bignum returns
// 2^53+1, which a number could not hold. half halves a uint64 above the int64
// range to prove the unsigned path.
func TestNativeBigInt(t *testing.T) {
	got := run(t, `
import { bignum, half } from "og:ogtest";
const out = [];
const r = bignum(9007199254740992n);
out.push("bignum type: " + typeof r);
out.push("bignum: " + r.toString());
out.push("half: " + half(18446744073709551614n).toString());
globalThis.result = out.join("\n");
`)
	want := strings.Join([]string{
		`bignum type: bigint`,
		`bignum: 9007199254740993`,
		`half: 9223372036854775807`,
	}, "\n")
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
