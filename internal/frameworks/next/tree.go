package next

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// This file ports next-app-loader's createTreeCodeFromPath
// (next/dist/build/webpack/loaders/next-app-loader/index.js) to Go. It
// produces the JS "loader tree" consumed by Next's app-page template.
//
// Static metadata files are attached by segmentMetadata (metadata.go).

const (
	pageSegmentKey    = "__PAGE__"
	defaultSegmentKey = "__DEFAULT__"
	pageSegment       = "page$"
	parallelVirtual   = "slot$"

	builtinDir                = "next/dist/client/components/builtin/"
	defaultGlobalErrorPath    = builtinDir + "global-error.js"
	defaultNotFoundPath       = builtinDir + "not-found.js"
	defaultLayoutPath         = builtinDir + "layout.js"
	parallelRouteDefault      = builtinDir + "default.js"
	parallelRouteDefaultNull  = builtinDir + "default-null.js"
	notFoundRoute             = "/_not-found"
	notFoundRouteEntry        = notFoundRoute + "/page"
	globalErrorFileType       = "global-error"
	globalNotFoundFileType    = "global-not-found"
	defaultGlobalNotFoundPath = builtinDir + "global-not-found.js"
)

// fileTypes mirrors FILE_TYPES (global-error and the experimental
// global-not-found are handled specially at the root layer, not in this loop).
var fileTypes = []string{"layout", "template", "error", "loading", "global-error", "not-found", "forbidden", "unauthorized"}

var accessFallbacks = []struct{ typ, path string }{
	{"not-found", builtinDir + "not-found.js"},
	{"forbidden", builtinDir + "forbidden.js"},
	{"unauthorized", builtinDir + "unauthorized.js"},
}

// Declaration is a module the tree imports lazily: `const Var = () => import(Path)`.
type Declaration struct {
	Var  string
	Path string
}

// TreeResult is the generated loader tree for one app page entry.
type TreeResult struct {
	Code         string // JS expression evaluating to the root tree node
	Declarations []Declaration
	RootLayout   string
}

type treeBuilder struct {
	appDir         string
	page           string   // entry name, e.g. "/blog/[slug]/page"
	appPaths       []string // app paths served by this entry
	allAppPaths    []string // every app path in the app (for static siblings)
	extensions     []string // e.g. [".tsx", ".ts", ".jsx", ".js"]
	decls          []Declaration
	rootLayout     string
	globalError    string
	globalNotFound string // the root global-not-found component, when enabled
	gnfEnabled     bool   // experimental.globalNotFound
	isDefaultNF    bool
	basePath       string // next.config basePath, for metadata image URLs
}

// BuildTree generates the loader tree for an app page entry. globalNotFound
// enables the experimental global-not-found convention (next.config
// experimental.globalNotFound).
func BuildTree(appDir, page string, appPaths, allAppPaths, pageExtensions []string, basePath string, globalNotFound bool) (*TreeResult, error) {
	exts := make([]string, len(pageExtensions))
	for i, e := range pageExtensions {
		exts[i] = "." + e
	}
	b := &treeBuilder{
		appDir: appDir, page: page, appPaths: appPaths, allAppPaths: allAppPaths, extensions: exts,
		globalError: defaultGlobalErrorPath, basePath: basePath,
		globalNotFound: defaultGlobalNotFoundPath, gnfEnabled: globalNotFound,
		isDefaultNF: page == notFoundRouteEntry && resolveFile(appDir, "/not-found", exts) == "",
	}
	code, err := b.subtree(nil)
	if err != nil {
		return nil, err
	}
	if b.rootLayout == "" {
		return nil, fmt.Errorf("next: %s doesn't have a root layout", page)
	}
	return &TreeResult{Code: code + ".children", Declarations: b.decls, RootLayout: b.rootLayout}, nil
}

func (b *treeBuilder) declare(prefix, path string) string {
	v := fmt.Sprintf("%s%d", prefix, len(b.decls))
	b.decls = append(b.decls, Declaration{Var: v, Path: path})
	return v
}

// resolve finds "<appDir><p><ext>" for the configured extensions.
func (b *treeBuilder) resolve(p string) string { return resolveFile(b.appDir, p, b.extensions) }

func resolveFile(appDir, p string, exts []string) string {
	for _, ext := range exts {
		abs := filepath.Join(appDir, filepath.FromSlash(p)) + ext
		if st, err := os.Stat(abs); err == nil && !st.IsDir() {
			return abs
		}
	}
	return ""
}

type parallel struct {
	key     string
	segment []string // [name] for a plain segment; [marker, rest...] for slots
}

func (b *treeBuilder) resolveParallelSegments(pathname string) []parallel {
	matched := map[string][]string{}
	var order []string
	set := func(k string, v []string) {
		if _, ok := matched[k]; !ok {
			order = append(order, k)
		}
		matched[k] = v
	}
	existingChildren := ""
	for _, appPath := range b.appPaths {
		if !strings.HasPrefix(appPath, pathname+"/") {
			continue
		}
		rest := strings.Split(appPath[len(pathname)+1:], "/")
		if len(rest) == 1 && rest[0] == "page" {
			existingChildren = appPath
			set("children", []string{pageSegment})
			continue
		}
		if strings.HasPrefix(rest[0], "@") {
			if len(rest) == 2 && rest[1] == "page" {
				set(rest[0], []string{pageSegment, ""}) // array form: ['page$']
				continue
			}
			set(rest[0], append([]string{parallelVirtual}, rest[1:]...))
			continue
		}
		if existingChildren != "" {
			if cur := matched["children"]; len(cur) == 0 || cur[0] != rest[0] {
				if strings.Contains(appPath, "@") {
					continue
				}
				if !strings.Contains(existingChildren, "@") {
					panic(fmt.Sprintf("next: you cannot have two parallel pages that resolve to the same path: %s and %s", existingChildren, appPath))
				}
			}
		}
		existingChildren = appPath
		set("children", []string{rest[0]})
	}
	out := make([]parallel, 0, len(order))
	for _, k := range order {
		out = append(out, parallel{k, matched[k]})
	}
	return out
}

func (b *treeBuilder) hasChildRoutes(segmentPath string) bool {
	prefix := ""
	if segmentPath != "" {
		prefix = segmentPath + "/"
	}
	for _, appPath := range b.appPaths {
		if !strings.HasPrefix(appPath, prefix) {
			continue
		}
		var segs []string
		for _, s := range strings.Split(appPath[len(prefix):], "/") {
			if !isGroupSegment(s) {
				segs = append(segs, s)
			}
		}
		if len(segs) == 1 && segs[0] == "page" {
			continue
		}
		if len(segs) > 0 && strings.HasPrefix(segs[0], "@") {
			continue
		}
		if len(segs) > 1 || (len(segs) == 1 && segs[0] != "page") {
			return true
		}
	}
	return false
}

func (b *treeBuilder) staticSiblings(segmentPath string) []string {
	cur := strings.Split(strings.Trim(NormalizeAppPath(segmentPath+"/page"), "/"), "/")
	if len(cur) == 0 || cur[0] == "" {
		return []string{}
	}
	last := cur[len(cur)-1]
	if !isDynamicSegment(last) {
		return []string{}
	}
	level := len(cur) - 1
	parent := "/" + strings.Join(cur[:level], "/")
	seen := map[string]bool{}
	out := []string{}
	for _, appPath := range b.allAppPaths {
		segs := strings.Split(strings.Trim(NormalizeAppPath(appPath), "/"), "/")
		if len(segs) <= level || segs[0] == "" {
			continue
		}
		if "/"+strings.Join(segs[:level], "/") != parent {
			continue
		}
		s := segs[level]
		if s != last && !isDynamicSegment(s) && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (b *treeBuilder) subtree(segments []string) (string, error) {
	segmentPath := strings.Join(segments, "/")
	isRoot := len(segments) == 0
	var parallels []parallel
	if isRoot {
		parallels = []parallel{{"children", []string{""}}}
	} else {
		parallels = b.resolveParallelSegments(segmentPath)
	}
	props := map[string]string{}
	var keys []string
	setProp := func(k, v string) {
		if _, ok := props[k]; !ok {
			keys = append(keys, k)
		}
		props[k] = v
	}
	// Metadata files (icon.png, …) in this segment's directory; Next attaches
	// them to the page and to every child segment's modules at this level.
	metadata := b.segmentMetadata(segmentPath, len(segments) <= 1)

	for _, p := range parallels {
		seg0 := p.segment[0]
		isArray := len(p.segment) > 1
		// A bare page$ marker (not the array form) is this route's page.
		if seg0 == pageSegment && !isArray {
			suffix := ""
			if p.key != "children" {
				suffix = "/" + p.key
			}
			pagePath := b.resolve(segmentPath + suffix + "/page")
			if pagePath == "" {
				return "", fmt.Errorf("next: can't resolve %s%s/page", segmentPath, suffix)
			}
			v := b.declare("page", pagePath)
			setProp(normalizeParallelKey(p.key), fmt.Sprintf("['%s', {}, {\n  page: [%s, %s],\n  %s\n}]", pageSegmentKey, v, jsonStr(pagePath), metadata))
			continue
		}

		sub := slices.Clone(segments)
		if p.key != "children" {
			sub = append(sub, p.key)
		}
		if seg0 != pageSegment && seg0 != parallelVirtual {
			sub = append(sub, seg0)
		}
		parallelSegmentPath := strings.Join(sub, "/")

		type entry struct{ typ, path string }
		var defined []entry
		dirPrefix := parallelSegmentPath
		if !strings.HasSuffix(dirPrefix, "/") {
			dirPrefix += "/"
		}
		for _, ft := range fileTypes {
			if ft == globalErrorFileType {
				continue // added below for every layer
			}
			if fp := b.resolve(dirPrefix + ft); fp != "" {
				defined = append(defined, entry{ft, fp})
			}
		}
		if isRoot {
			if fp := b.resolve("/" + globalErrorFileType); fp != "" {
				b.globalError = fp
			}
		}
		// FILE_TYPES order puts global-error before not-found etc.; keep it.
		ge := entry{globalErrorFileType, b.globalError}
		idx := slices.IndexFunc(defined, func(e entry) bool { return e.typ == "not-found" || e.typ == "forbidden" || e.typ == "unauthorized" })
		if idx < 0 {
			defined = append(defined, ge)
		} else {
			defined = slices.Insert(defined, idx, ge)
		}
		// Experimental global-not-found: at the root layer Next always sets a
		// global-not-found component (the user's app/global-not-found or the
		// builtin default), set just before global-error, so the renderer can
		// serve it as a full-document 404.
		if isRoot && b.gnfEnabled {
			if fp := b.resolve("/" + globalNotFoundFileType); fp != "" {
				b.globalNotFound = fp
			}
			gnf := entry{globalNotFoundFileType, b.globalNotFound}
			if gi := slices.IndexFunc(defined, func(e entry) bool { return e.typ == globalErrorFileType }); gi >= 0 {
				defined = slices.Insert(defined, gi, gnf)
			} else {
				defined = append(defined, gnf)
			}
		}

		existing := map[string]bool{}
		for _, e := range defined {
			existing[e.typ] = true
		}
		groups := 0
		for _, s := range sub {
			if isGroupSegment(s) {
				groups++
			}
		}
		isFirstLayerGroup := len(segments) == 1 && groups == 1
		if isRoot || isFirstLayerGroup {
			for _, f := range accessFallbacks {
				hasRoot := b.resolve("/"+f.typ) != ""
				if !(hasRoot && isFirstLayerGroup) && !existing[f.typ] {
					if !(b.isDefaultNF && f.typ == "not-found") {
						defined = append(defined, entry{f.typ, f.path})
					}
				}
			}
		}
		if b.rootLayout == "" {
			for _, e := range defined {
				if e.typ == "layout" {
					b.rootLayout = e.path
					break
				}
			}
			if b.isDefaultNF && b.rootLayout == "" {
				b.rootLayout = defaultLayoutPath
				defined = append(defined, entry{"layout", defaultLayoutPath})
			}
		}

		segKey := seg0
		if segKey == parallelVirtual || segKey == pageSegment {
			segKey = "(__SLOT__)"
		}
		normKey := normalizeParallelKey(p.key)

		var subtreeCode string
		if b.page == notFoundRouteEntry && normKey == "children" {
			nfPath := defaultNotFoundPath
			for _, e := range defined {
				if e.typ == "not-found" {
					nfPath = e.path
				}
			}
			v := b.declare("notFound", nfPath)
			subtreeCode = fmt.Sprintf("{\n  children: [%s, {\n    children: ['%s', {}, {\n      page: [%s, %s]\n    }]\n  }, {}]\n}",
				jsonStr(strings.TrimPrefix(notFoundRoute, "/")), pageSegmentKey, v, jsonStr(nfPath))
		}

		var mods strings.Builder
		mods.WriteString("{\n")
		for _, e := range defined {
			v := b.declare("module", e.path)
			fmt.Fprintf(&mods, "  '%s': [%s, %s],\n", e.typ, v, jsonStr(e.path))
		}
		mods.WriteString("  " + metadata + "\n}")

		if subtreeCode == "" {
			c, err := b.subtree(sub)
			if err != nil {
				return "", err
			}
			subtreeCode = c
		}
		siblings, _ := json.Marshal(b.staticSiblings(parallelSegmentPath))
		setProp(normKey, fmt.Sprintf("[\n'%s',\n%s,\n%s,\n%s\n]", segKey, subtreeCode, mods.String(), siblings))
	}

	// Parallel slots present on disk but not matched get a default page.
	for _, adj := range b.adjacentParallelSegments(segmentPath) {
		k := normalizeParallelKey(adj)
		if _, ok := props[k]; ok {
			continue
		}
		actual := ""
		if adj != "children" {
			actual = "/" + adj
		}
		full := segmentPath + actual
		def := b.resolve(full + "/default")
		if def == "" {
			if adj == "children" {
				def = parallelRouteDefault
				if isInterceptionRoute(b.page) {
					def = parallelRouteDefaultNull
				}
			} else {
				insideCatchAll := slices.ContainsFunc(segments, isCatchAllSegment)
				if !insideCatchAll && b.hasChildRoutes(segmentPath) {
					return "", fmt.Errorf("next: no default component was found for a parallel route rendered on this page (app%s/%s/default)", segmentPath, adj)
				}
				def = parallelRouteDefault
			}
		}
		v := b.declare("default", def)
		setProp(k, fmt.Sprintf("[\n'%s',\n{},\n{\n  defaultPage: [%s, %s],\n}\n]", defaultSegmentKey, v, jsonStr(def)))
	}

	var out strings.Builder
	out.WriteString("{\n")
	for i, k := range keys {
		if i > 0 {
			out.WriteString(",\n")
		}
		fmt.Fprintf(&out, "%s: %s", k, props[k])
	}
	out.WriteString("\n}")
	return out.String(), nil
}

func (b *treeBuilder) adjacentParallelSegments(segmentPath string) []string {
	dir := filepath.Join(b.appDir, filepath.FromSlash(segmentPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := []string{"children"}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "@") {
			out = append(out, e.Name())
		}
	}
	return out
}

// NormalizeAppPath maps an app path to its URL pathname (Next's
// normalizeAppPath): drops groups, parallel slots and the page/route leaf.
func NormalizeAppPath(route string) string {
	segs := strings.Split(route, "/")
	var b strings.Builder
	for i, s := range segs {
		switch {
		case s == "", isGroupSegment(s), strings.HasPrefix(s, "@"):
			continue
		case (s == "page" || s == "route") && i == len(segs)-1:
			continue
		}
		b.WriteString("/" + s)
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

func isGroupSegment(s string) bool {
	return strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
}

func isDynamicSegment(s string) bool { return strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") }

func isCatchAllSegment(s string) bool {
	return strings.HasPrefix(s, "[...") || strings.HasPrefix(s, "[[...")
}

func isInterceptionRoute(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if strings.HasPrefix(s, "(.)") || strings.HasPrefix(s, "(..)") || strings.HasPrefix(s, "(...)") {
			return true
		}
	}
	return false
}

func normalizeParallelKey(k string) string { return strings.TrimPrefix(k, "@") }

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
