package jshost

import (
	"context"
	"strings"
	"testing"
)

func TestSortedRoutes(t *testing.T) {
	h := &Host{Dir: project(t, nil)}
	got, err := h.SortedRoutes(context.Background(), []string{"/posts/[id]", "/[slug]", "/", "/posts/new", "/blog/[...all]", "/_app", "/blog", "/about"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "/ /_app /about /blog /blog/[...all] /posts/new /posts/[id] /[slug]"; strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
	// Next's validation: one slug name per level.
	if _, err := h.SortedRoutes(context.Background(), []string{"/[a]", "/[b]"}); err == nil {
		t.Fatal("conflicting slug names accepted")
	}
}
