package main

import (
	"os"
	"path/filepath"

	"github.com/moriyoshi/oganesson/internal/nativegen"
	"github.com/moriyoshi/oganesson/runtime/kv"
)

// typesFile declares Oganesson's built-in modules for TypeScript, the way
// next-env.d.ts does for Next.
const typesFile = "og-env.d.ts"

// writeTypes (re)writes og-env.d.ts in the project root when it is
// missing or stale: og:kv's declarations, then the project's native Go
// modules' (og/<name>, internal/nativegen). tsconfig "include" patterns
// such as **/*.ts pick it up.
func writeTypes(dir string) error {
	native, err := nativegen.Scan(dir)
	if err != nil {
		return err
	}
	text := kv.Types + native.TypeScript()
	p := filepath.Join(dir, typesFile)
	if b, err := os.ReadFile(p); err == nil && string(b) == text {
		return nil
	}
	return os.WriteFile(p, []byte(text), 0o644)
}
