package jshost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MiddlewareMatchers evaluates a middleware/proxy file's `config` export
// and converts its matchers with Next's own getMiddlewareMatchers. Without
// a matcher, middleware runs for every path, as in Next.
func (h *Host) MiddlewareMatchers(ctx context.Context, file, nextDir string, nextConfig map[string]any) ([]map[string]any, error) {
	cfg, err := json.Marshal(nextConfig)
	if err != nil {
		return nil, err
	}
	src, err := lift(filepath.Join(nextDir, "dist", "build", "analysis", "get-page-static-info.js"))
	if err != nil {
		return nil, err
	}
	entry := fmt.Sprintf(`
import * as mod from %s;
const _constants = require("next/dist/lib/constants");
const _trytoparsepath = require("next/dist/lib/try-to-parse-path");
const _escaperegexp = require("next/dist/shared/lib/escape-regexp");
const _zod = require("next/dist/shared/lib/zod");
const _middlewareconfig = require("next/dist/build/segment-config/middleware/middleware-config");
%s
const c = mod.config;
globalThis.__ogMatchers = c && c.matcher
  ? getMiddlewareMatchers(c.matcher, %s)
  : [{ regexp: "^/.*$", originalSource: "/:path*" }];
`, jsonString(file), src, cfg)
	saved := h.Alias
	h.Alias = map[string]string{"@opentelemetry/api": "next/dist/compiled/@opentelemetry/api"}
	code, err := h.Bundle("", entry, "")
	h.Alias = saved
	if err != nil {
		return nil, err
	}
	v, err := h.Eval(ctx, code, "__ogMatchers")
	if err != nil {
		return nil, fmt.Errorf("jshost: middleware config of %s: %w", file, err)
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("jshost: middleware matchers: unexpected %T", v)
	}
	out := make([]map[string]any, 0, len(list))
	for _, m := range list {
		if mm, ok := m.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out, nil
}

// lift extracts getMiddlewareMatchers and the module-local constants it
// uses from Next's get-page-static-info.js. Importing that module pulls in
// Node-only dependencies; its matcher logic itself is pure.
func lift(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	src := string(b)
	start := strings.Index(src, "const APP_ROUTE_RSC_SUFFIX_MATCHER")
	constEnd := strings.Index(src, "const OPTIONAL_MIDDLEWARE_NEXT_DATA_PREFIX")
	fnStart := strings.Index(src, "function getMiddlewareMatchers(")
	fnEnd := strings.Index(src, "\nfunction parseMiddlewareConfig(")
	if start < 0 || constEnd < 0 || fnStart < 0 || fnEnd < fnStart {
		return "", fmt.Errorf("jshost: unrecognized layout of %s (unsupported Next version?)", path)
	}
	constEnd += strings.Index(src[constEnd:], "\n")
	return src[start:constEnd] + "\n" + src[fnStart:fnEnd] + "\n", nil
}
