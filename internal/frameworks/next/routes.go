package next

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultPageExtensions matches Next's default pageExtensions.
var DefaultPageExtensions = []string{"tsx", "ts", "jsx", "js"}

// ScanAppPaths lists app-dir entry files as app paths ("/page",
// "/blog/[slug]/page", "/api/hello/route"), skipping private "_folders".
func ScanAppPaths(appDir string, exts []string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(appDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != appDir && strings.HasPrefix(d.Name(), "_") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		for _, ext := range exts {
			for _, kind := range []string{"page", "route"} {
				if name == kind+"."+ext {
					rel, _ := filepath.Rel(appDir, filepath.Dir(p))
					rel = filepath.ToSlash(rel)
					if rel == "." {
						rel = ""
					} else {
						rel = "/" + rel
					}
					out = append(out, rel+"/"+kind)
				}
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Route is one served route: an app page or route handler entry.
type Route struct {
	Page     string   // entry name, e.g. "/blog/[slug]/page"
	Pathname string   // URL pattern, e.g. "/blog/[slug]"
	AppPaths []string // app paths rendered by this entry (parallel slots)
	IsRoute  bool     // route handler (route.ts) rather than a page
	Pages    bool     // a Pages Router page (pages/), or API route with IsRoute
}

// GroupRoutes groups app paths into entries by URL pathname; parallel slot
// pages join the entry of the "children" page they render alongside.
func GroupRoutes(appPaths []string) []Route {
	byPath := map[string]*Route{}
	var order []string
	for _, ap := range appPaths {
		pathname := NormalizeAppPath(ap)
		r, ok := byPath[pathname]
		if !ok {
			r = &Route{Pathname: pathname}
			byPath[pathname] = r
			order = append(order, pathname)
		}
		r.AppPaths = append(r.AppPaths, ap)
		if !strings.Contains(ap, "/@") || r.Page == "" {
			r.Page = ap
			r.IsRoute = strings.HasSuffix(ap, "/route")
		}
	}
	out := make([]Route, 0, len(order))
	for _, p := range order {
		out = append(out, *byPath[p])
	}
	return out
}
