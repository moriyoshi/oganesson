package rsc

import "testing"

// TestHasUseClient: "use client" anywhere in the directive prologue, after
// comments and other directives, and not past it.
func TestHasUseClient(t *testing.T) {
	for src, want := range map[string]bool{
		`"use client";`:                          true,
		"// c\n/* d */\n'use client'\n":          true,
		"\"use strict\";\n'use client';\nvar a;": true,
		"'use strict'\n\"use client\"":           true,
		"\"use strict\";\nvar a = 'use client';": false,
		"import x from 'y';\n'use client';":      false,
		"\"use server\";":                        false,
	} {
		if got := HasUseClient([]byte(src)); got != want {
			t.Errorf("HasUseClient(%q) = %v, want %v", src, got, want)
		}
	}
}
