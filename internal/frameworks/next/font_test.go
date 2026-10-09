package next

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/internal/licenses"
)

// TestNoteGoogleFonts: a next/font/google loader adds the Google Fonts
// attribution (the downloaded font bytes ship but have no local package);
// next/font/local alone does not.
func TestNoteGoogleFonts(t *testing.T) {
	google := &builder{licenses: &licenses.Collector{}, fonts: map[string]*fontModule{
		"a": {loader: "next/font/google"}, "b": {loader: "next/font/local"},
	}}
	google.noteGoogleFonts()
	text, err := google.licenses.Text()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "Google Fonts (downloaded at build time)") ||
		!strings.Contains(string(text), "fonts.google.com/attribution") {
		t.Errorf("google notice missing:\n%s", text)
	}

	local := &builder{licenses: &licenses.Collector{}, fonts: map[string]*fontModule{
		"b": {loader: "next/font/local"},
	}}
	local.noteGoogleFonts()
	if text, _ := local.licenses.Text(); text != nil {
		t.Errorf("local-only fonts produced a notice:\n%s", text)
	}
}

func TestNextFont(t *testing.T) {
	srv := serveFixture(t)
	get := func(p string) []byte {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d", p, resp.StatusCode)
		}
		return b
	}
	body := string(get("/gallery"))
	p := regexp.MustCompile(`<p id="font" class="__className_([0-9a-f]{6}) __variable_([0-9a-f]{6})" data-family="&#x27;geist&#x27;, &#x27;geist Fallback&#x27;">`).FindStringSubmatch(body)
	if p == nil || p[1] != p[2] {
		t.Fatalf("font <p> not as expected:\n%s", regexp.MustCompile(`<p id="font"[^>]*>`).FindString(body))
	}
	hash := p[1]
	pre := regexp.MustCompile(`<link rel="preload" href="(/_next/static/media/[0-9a-f]{20}-s\.p\.woff2)" as="font" crossorigin="" type="font/woff2"/>`).FindStringSubmatch(body)
	if pre == nil {
		t.Fatalf("no font preload link in /gallery")
	}
	want, err := os.ReadFile(filepath.Join(fixtureDir(t), "app/gallery/fonts/geist-latin.woff2"))
	if err != nil {
		t.Fatal(err)
	}
	if got := get(pre[1]); !bytes.Equal(got, want) {
		t.Errorf("served font differs from the source (%d vs %d bytes)", len(got), len(want))
	}
	var css string
	for _, m := range regexp.MustCompile(`<link rel="stylesheet" href="(/_next/static/css/[^"]+)"`).FindAllStringSubmatch(body, -1) {
		if c := string(get(m[1])); strings.Contains(c, "@font-face") {
			css += c
		}
	}
	for _, w := range []string{
		"@font-face{font-family:geist;src:url(" + pre[1] + `) format("woff2");font-display:swap}`,
		`@font-face{font-family:geist Fallback;src:local("Arial");ascent-override:`,
		".__className_" + hash + "{font-family:geist,geist Fallback}",
		".__variable_" + hash + `{--font-geist: "geist", "geist Fallback" }`,
	} {
		if !strings.Contains(css, w) {
			t.Errorf("font stylesheet lacks %s:\n%s", w, css)
		}
	}
	// A client component's font (preload: false): its class is applied and
	// its stylesheet linked, but its file is not preloaded.
	c := regexp.MustCompile(`<figcaption id="caption" class="__className_([0-9a-f]{6})">`).FindStringSubmatch(body)
	if c == nil || c[1] == hash {
		t.Fatalf("client component font class: %v", c)
	}
	if !strings.Contains(css, ".__className_"+c[1]+"{font-family:geistBold,geistBold Fallback;font-weight:700}") {
		t.Errorf("client font rule missing:\n%s", css)
	}
	if n := strings.Count(body, `as="font"`); n != 1 {
		t.Errorf("%d font preloads, want 1", n)
	}
	// Only the entry that uses the font preloads it.
	if home := string(get("/")); strings.Contains(home, `as="font"`) {
		t.Errorf("/ preloads a font it does not use")
	}
}

func TestTransformFonts(t *testing.T) {
	b := &builder{dir: "/proj"}
	src := `import { Inter, Roboto_Mono as Mono } from "next/font/google";
import localFont from 'next/font/local';
import type { X } from "next/font/google";
export const inter = Inter({ subsets: ["latin"], weight: ["400", 700], display: "swap", adjust: -1.5, axes: null });
const mono = Mono();
const local = localFont({ src: [{ path: "./a.woff2", weight: "400" }], variable: "--f" } as const);
export default function Page() { return inter.className + mono.className + local.variable; }
`
	out, err := b.transformFonts("/proj/app/page.tsx", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		`import inter from "og:next-font:next/font/google/target.css?{\"path\":\"app/page.tsx\",\"import\":\"Inter\",\"arguments\":[{\"adjust\":-1.5,\"axes\":null,\"display\":\"swap\",\"subsets\":[\"latin\"],\"weight\":[\"400\",700]}],\"variableName\":\"inter\"}"; export { inter };`,
		`import mono from "og:next-font:next/font/google/target.css?{\"path\":\"app/page.tsx\",\"import\":\"Roboto_Mono\",\"arguments\":[],\"variableName\":\"mono\"}";`,
		`import local from "og:next-font:next/font/local/target.css?{\"path\":\"app/page.tsx\",\"import\":\"\",\"arguments\":[{\"src\":[{\"path\":\"./a.woff2\",\"weight\":\"400\"}],\"variable\":\"--f\"}],\"variableName\":\"local\"}";`,
		`import type { X } from "next/font/google";`,
		`export default function Page()`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %s:\n%s", w, out)
		}
	}
	if strings.Contains(out, `import { Inter`) || strings.Contains(out, `from 'next/font/local'`) {
		t.Errorf("font loader imports remain:\n%s", out)
	}
	// A const statement with other declarators keeps them (next-swc).
	mixed, err := b.transformFonts("/proj/app/m.ts", `import { Inter } from "next/font/google";
export const a = 1, inter = Inter(), b = [a, 2];
`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mixed, `import inter from "og:next-font:next/font/google/target.css?`) || !strings.Contains(mixed, `export { inter }; export const a = 1, b = [a, 2];`) {
		t.Errorf("mixed declarators:\n%s", mixed)
	}
	// Same-named property keys and members are not references to the loader.
	if _, err := b.transformFonts("/proj/app/k.ts", `import { Inter } from "next/font/google";
const inter = Inter();
const o = { Inter: 1 }; export const n = o.Inter + inter.className;
`); err != nil {
		t.Errorf("property names flagged: %v", err)
	}
	for in, want := range map[string]string{
		`import { Inter } from "next/font/google"; let a = Inter();`:                                  "must be assigned to a const",
		`import { Inter } from "next/font/google"; const s = "x"; const a = Inter({ subsets: [s] });`: "explicitly written literals",
		`import { Inter } from "next/font/google"; function f() { return Inter(); }`:                  "assigned to a const in the module scope",
		`import { Inter } from "next/font/google"; const i = Inter(); export const again = Inter;`:    "assigned to a const in the module scope",
		`import { Inter } from "next/font/google"; const i = Inter(); const o = { Inter };`:           "assigned to a const in the module scope",
		`import * as F from "next/font/google";`:                                                      "namespace imports",
	} {
		if _, err := b.transformFonts("/proj/app/x.ts", in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", in, err, want)
		}
	}
}
