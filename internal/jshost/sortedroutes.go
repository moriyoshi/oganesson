package jshost

import (
	"context"
	"encoding/json"
	"fmt"
)

// SortedRoutes orders pages with Next's own getSortedRoutes (each route
// before its children, static segments before [param], [...rest] and
// [[...optional]]), the order of _buildManifest.js's sortedPages, in which
// the client router matches dynamic routes. It also rejects the route sets
// Next rejects (e.g. different slug names at one level).
func (h *Host) SortedRoutes(ctx context.Context, pages []string) ([]string, error) {
	in, err := json.Marshal(pages)
	if err != nil {
		return nil, err
	}
	code, err := h.Bundle("", fmt.Sprintf(`import { getSortedRoutes } from "next/dist/shared/lib/router/utils/sorted-routes";
globalThis.__ogSorted = getSortedRoutes(%s);
`, in), "")
	if err != nil {
		return nil, err
	}
	v, err := h.Eval(ctx, code, "__ogSorted")
	if err != nil {
		return nil, fmt.Errorf("jshost: sorting routes: %w", err)
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("jshost: sorted routes: unexpected %T", v)
	}
	out := make([]string, 0, len(list))
	for _, p := range list {
		s, _ := p.(string)
		out = append(out, s)
	}
	return out, nil
}
