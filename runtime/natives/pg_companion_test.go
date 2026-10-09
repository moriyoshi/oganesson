//go:build aot

package natives_test

// The AOT backend's transpiled code (pglite-go's companion module), linked for
// TestPG under -tags aot, as internal/frameworks/next does for its PGlite tests.
import _ "github.com/moriyoshi/pglite-go-aot"
