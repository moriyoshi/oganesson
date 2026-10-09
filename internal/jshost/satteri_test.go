package jshost

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestSatteriMarkdown: satteri (Astro's Markdown processor) is an emnapi
// addon whose WebAssembly build og loads (napi-rs-wasi.js): napi-rs
// creates a threadsafe function at registration, in emnapi's compiled-in
// C, which rejects env 0 (napi-wasm numbered environments from 0) and calls
// emnapi's runtime hooks (napi_set_last_error, ...). The HTML is Node 24's
// for the same input.
func TestSatteriMarkdown(t *testing.T) {
	dir, _ := filepath.Abs("../../testdata/apps/astro-static")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/satteri")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/astro-static)")
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import * as s from "satteri";
const md = "---\ntitle: T\n---\n# Hi *there*\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n- [x] done\n\n~~strike~~ www.example.com 日本語[^1]\n\n[^1]: Note.\n";
globalThis.__ogResult = s.markdownToHtml(md, { features: { gfm: true, frontmatter: true } }).html;
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	want := "<h1>Hi <em>there</em></h1>\n<table>\n<thead>\n<tr>\n<th>a</th>\n<th>b</th>\n</tr>\n</thead>\n<tbody>\n<tr>\n<td>1</td>\n<td>2</td>\n</tr>\n</tbody>\n</table>\n<ul class=\"contains-task-list\">\n<li class=\"task-list-item\"><input type=\"checkbox\" checked disabled> done</li>\n</ul>\n<p><del>strike</del> <a href=\"http://www.example.com\">www.example.com</a> 日本語<sup><a href=\"#user-content-fn-1\" id=\"user-content-fnref-1\" data-footnote-ref aria-describedby=\"footnote-label\">1</a></sup></p>\n<section data-footnotes class=\"footnotes\"><h2 class=\"sr-only\" id=\"footnote-label\">Footnotes</h2>\n<ol>\n<li id=\"user-content-fn-1\">\n<p>Note. <a href=\"#user-content-fnref-1\" data-footnote-backref=\"\" aria-label=\"Back to reference 1\" class=\"data-footnote-backref\">↩</a></p>\n</li>\n</ol>\n</section>\n"
	if v != want {
		t.Fatalf("got\n%v\nwant\n%s", v, want)
	}
}
