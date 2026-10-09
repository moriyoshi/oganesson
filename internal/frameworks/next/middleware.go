package next

import (
	"context"
	"os"
	"path/filepath"

	"github.com/moriyoshi/oganesson/internal/jshost"
)

// middleware is the project's proxy.ts (Next 16) or middleware.ts.
type middleware struct {
	File     string
	Page     string // "/proxy", "/middleware", "/src/proxy", ...
	Matchers []map[string]any
}

func (b *builder) findMiddleware(ctx context.Context, host *jshost.Host) (*middleware, error) {
	for _, dir := range []string{"", "src"} {
		for _, name := range []string{"proxy", "middleware"} {
			for _, ext := range b.exts {
				file := filepath.Join(b.dir, dir, name+"."+ext)
				if _, err := os.Stat(file); err != nil {
					continue
				}
				page := "/" + name
				if dir != "" {
					page = "/" + dir + page
				}
				matchers, err := host.MiddlewareMatchers(ctx, file, b.nextDir, b.config)
				if err != nil {
					return nil, err
				}
				return &middleware{File: file, Page: page, Matchers: matchers}, nil
			}
		}
	}
	return nil, nil
}

func (b *builder) middlewareSource() (string, error) {
	return ExpandTemplate(b.nextDir, "middleware", TemplateArgs{
		Replacements: map[string]string{"VAR_USERLAND": b.middleware.File, "VAR_DEFINITION_PAGE": b.middleware.Page},
		Imports:      cacheHandlerImport,
	})
}
