package webapi

import (
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/internal/jsvm"
)

func TestPreludeMatchesEngine(t *testing.T) {
	// A runtime guard can skip installation while leaving the fallback
	// code and data in the program that gets bound in every VM.
	for _, marker := range []string{"@formatjs/", "regexp-tree", "function installProxyHasInstance("} {
		want := !jsvm.UsesEkuma
		if got := strings.Contains(preludeSource, marker); got != want {
			t.Errorf("prelude contains %q = %t, want %t", marker, got, want)
		}
	}
}
