package next

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/microsoft/TypeScript/tsc/ogapi"
	"github.com/moriyoshi/oganesson/internal/jshost"
)

// next/font support.
//
//   - transformFonts ports next-swc's next_font transform
//     (crates/next-custom-transforms/src/transforms/fonts): font loader
//     imports are removed, and `const inter = Inter({...})` at module scope
//     becomes `import inter from "<loader>/target.css?<query>"`. The query
//     is Next's: {path, import, arguments, variableName}. Oganesson prefixes
//     the specifier with fontSpecifier.
//   - That import resolves to a module in the fontNamespace: its default
//     export is what css-loader exports for next-font-loader's output
//     (style, className, variable), and it imports the font's stylesheet, a
//     virtual "<id>.css" that compileCSS builds without the user's PostCSS
//     (Next's font rule has no postcss-loader). entryCSS links it like any
//     stylesheet and records preloadable font files in the next/font
//     manifest (NextFontManifestPlugin).
//   - The CSS, exports and font files come from Next's own @next/font loader
//     and postcss-next-font, run in the build's JS host (jshost bridge
//     nextFont).

// fontLoaders are the font loader modules next-swc recognizes.
var fontLoaders = []string{"next/font/google", "next/font/local", "@next/font/google", "@next/font/local"}

// googleFontsNotice attributes the font files next/font/google downloads at
// build time and embeds as assets; they have no local package to credit.
const googleFontsNotice = `Fonts fetched from Google Fonts (https://fonts.google.com) at build time by
next/font/google are embedded into this app as static font assets. Google
Fonts are distributed under open font licenses, most under the SIL Open Font
License 1.1 and some under the Apache License 2.0. Per-family license and
attribution details are at https://fonts.google.com/attribution.`

// noteGoogleFonts records the Google Fonts attribution when the app uses a
// next/font/google loader.
func (b *builder) noteGoogleFonts() {
	b.mu.Lock()
	used := false
	for _, f := range b.fonts {
		if strings.Contains(f.loader, "google") {
			used = true
			break
		}
	}
	b.mu.Unlock()
	if used {
		b.licenses.AddNotice("Google Fonts (downloaded at build time)", googleFontsNotice)
	}
}

const (
	fontSpecifier  = "og:next-font:" // + "<loader>/target.css?<query>"
	fontNamespace  = "og-next-font"
	fontPathPrefix = "/og-next-font/" // + "<id>.js" | "<id>.css"
)

type fontModule struct {
	loader string
	query  map[string]any

	once   sync.Once
	result *jshost.FontResult
	err    error
}

// fontQuery is the target.css query, in next-swc's key order.
type fontQuery struct {
	Path         string `json:"path"`
	Import       string `json:"import"`
	Arguments    []any  `json:"arguments"`
	VariableName string `json:"variableName"`
}

func mightUseFonts(src string) bool {
	return strings.Contains(src, "font/google") || strings.Contains(src, "font/local")
}

// transformFonts ports the next_font transform for one source file.
func (b *builder) transformFonts(path, code string) (string, error) {
	if !mightUseFonts(code) {
		return code, nil
	}
	m := ogapi.ParseModule(path, code)
	type fontFn struct{ loader, name string }
	fns := map[string]fontFn{}
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	for _, im := range m.Imports {
		if !slices.Contains(fontLoaders, im.Specifier) || im.TypeOnly {
			continue
		}
		for _, bd := range im.Bindings {
			switch bd.Imported {
			case "*":
				return "", fmt.Errorf("next/font: %s: Font loaders can't have namespace imports", path)
			case "default":
				fns[bd.Local] = fontFn{im.Specifier, ""} // next/font/local's default export
			default:
				fns[bd.Local] = fontFn{im.Specifier, bd.Imported}
			}
		}
		edits = append(edits, edit{im.Start, im.End, ""})
	}
	if len(fns) == 0 {
		return code, nil
	}
	rel, err := filepath.Rel(b.dir, path)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	allowed := map[int]bool{} // callee offsets of module-scope font loader calls
	for _, vs := range m.Vars {
		var calls, others []ogapi.VarDecl
		for _, d := range vs.Decls {
			if _, ok := fns[d.Callee]; ok {
				calls = append(calls, d)
			} else {
				others = append(others, d)
			}
		}
		if len(calls) == 0 {
			continue
		}
		if vs.Kind != "const" {
			return "", fmt.Errorf("next/font: %s: Font loader calls must be assigned to a const", path)
		}
		var sb strings.Builder
		for _, d := range calls {
			if d.Name == "" {
				return "", fmt.Errorf("next/font: %s: Font loader calls must be assigned to an identifier", path)
			}
			if d.ArgsError != "" {
				return "", fmt.Errorf("next/font: %s: %s", path, d.ArgsError)
			}
			allowed[d.CalleeStart] = true
			fn := fns[d.Callee]
			args := d.Args
			if args == nil {
				args = []any{}
			}
			var q bytes.Buffer
			enc := json.NewEncoder(&q)
			enc.SetEscapeHTML(false)
			if err := enc.Encode(fontQuery{Path: rel, Import: fn.name, Arguments: args, VariableName: d.Name}); err != nil {
				return "", err
			}
			spec := fontSpecifier + fn.loader + "/target.css?" + strings.TrimSpace(q.String())
			fmt.Fprintf(&sb, "import %s from %s;", d.Name, jsonStr(spec))
			if vs.Exported {
				fmt.Fprintf(&sb, " export { %s };", d.Name)
			}
		}
		// Like next-swc, the statement keeps its other declarators.
		if len(others) > 0 {
			texts := make([]string, len(others))
			for i, d := range others {
				texts[i] = code[d.Start:d.End]
			}
			if vs.Exported {
				sb.WriteString(" export")
			}
			sb.WriteString(" const " + strings.Join(texts, ", ") + ";")
		}
		edits = append(edits, edit{vs.Start, vs.End, sb.String()})
	}
	// next-swc's find_functions_outside_module_scope: a font loader may only
	// be called in a module-scope const initializer.
	for local := range fns {
		for _, at := range m.Refs[local] {
			if !allowed[at] {
				return "", fmt.Errorf("next/font: %s: Font loaders must be called and assigned to a const in the module scope", path)
			}
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, e := range edits {
		code = code[:e.start] + e.text + code[e.end:]
	}
	return code, nil
}

// registerFont records a font module for a fontSpecifier import and
// returns its id.
func (b *builder) registerFont(spec string) (string, error) {
	rest := strings.TrimPrefix(spec, fontSpecifier)
	target, raw, ok := strings.Cut(rest, "/target.css?")
	if !ok {
		return "", fmt.Errorf("next/font: malformed import %s", spec)
	}
	var q map[string]any
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		return "", fmt.Errorf("next/font: %s: %w", spec, err)
	}
	q["loader"] = target
	sum := sha256.Sum256([]byte(rest))
	id := hex.EncodeToString(sum[:8])
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fonts == nil {
		b.fonts = map[string]*fontModule{}
	}
	if _, ok := b.fonts[id]; !ok {
		b.fonts[id] = &fontModule{loader: target, query: q}
	}
	return id, nil
}

// fontResult runs the font loader once per module.
func (b *builder) fontResult(id string) (*jshost.FontResult, error) {
	b.mu.Lock()
	f := b.fonts[id]
	b.mu.Unlock()
	if f == nil {
		return nil, fmt.Errorf("next/font: unknown font module %s", id)
	}
	f.once.Do(func() {
		if b.plugins == nil {
			f.err = fmt.Errorf("next/font: the build's JS host is not running")
			return
		}
		f.result, f.err = b.plugins.NextFont(f.query, b.assetPrefix())
		if f.err != nil {
			f.err = fmt.Errorf("next/font: %s: %w", f.query["path"], f.err)
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.media == nil {
			b.media = map[string][]byte{}
		}
		for _, file := range f.result.Files {
			b.media[file.Name] = file.Data
		}
	})
	return f.result, f.err
}

// fontCSS returns the stylesheet of a virtual font CSS path.
func (b *builder) fontCSS(path string) (string, bool, error) {
	if !strings.HasPrefix(path, fontPathPrefix) || !strings.HasSuffix(path, ".css") {
		return "", false, nil
	}
	r, err := b.fontResult(strings.TrimSuffix(strings.TrimPrefix(path, fontPathPrefix), ".css"))
	if err != nil {
		return "", true, err
	}
	return r.CSS, true, nil
}

// fontPlugin resolves and loads next/font modules in the server and
// browser builds. It must precede the builds' catch-all resolvers.
func (b *builder) fontPlugin() api.Plugin {
	return api.Plugin{Name: "og-next-font", Setup: func(pb api.PluginBuild) {
		pb.OnResolve(api.OnResolveOptions{Filter: `^og:next-font:`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			id, err := b.registerFont(a.Path)
			if err != nil {
				return api.OnResolveResult{}, err
			}
			return api.OnResolveResult{Path: fontPathPrefix + id + ".js", Namespace: fontNamespace}, nil
		})
		pb.OnResolve(api.OnResolveOptions{Filter: `^/og-next-font/`, Namespace: fontNamespace}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			return api.OnResolveResult{Path: a.Path, Namespace: fontNamespace}, nil
		})
		pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: fontNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			code := "export {};" // the stylesheet: linked through entryCSSFiles
			if strings.HasSuffix(a.Path, ".js") {
				r, err := b.fontResult(strings.TrimSuffix(strings.TrimPrefix(a.Path, fontPathPrefix), ".js"))
				if err != nil {
					return api.OnLoadResult{}, err
				}
				// css-loader's CJS locals: { name: value, ... } in export order.
				var fields []string
				for _, e := range r.Exports {
					fields = append(fields, jsonStr(e[0].(string))+": "+mustJSON(e[1]))
				}
				code = fmt.Sprintf("import %s;\nexport default {%s};\n", jsonStr(strings.TrimSuffix(a.Path, ".js")+".css"), strings.Join(fields, ", "))
			}
			return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS}, nil
		})
	}}
}

// fontFilesFor lists the font files of a font module key reached by entryCSS.
func (b *builder) fontFilesFor(key string) []string {
	p := keyPath(key)
	if !strings.HasPrefix(p, fontPathPrefix) || !strings.HasSuffix(p, ".js") {
		return nil
	}
	r, err := b.fontResult(strings.TrimSuffix(strings.TrimPrefix(p, fontPathPrefix), ".js"))
	if err != nil || r == nil {
		return nil
	}
	var out []string
	for _, f := range r.Files {
		out = append(out, f.Name)
	}
	return out
}

// assetPrefix is next.config's assetPrefix, which Next's config loading
// sets to basePath when it is unset: the prefix of every /_next/ asset URL
// ("" for none). A trailing slash is dropped, as Next drops it.
func (b *builder) assetPrefix() string {
	p, _ := b.config["assetPrefix"].(string)
	return strings.TrimSuffix(p, "/")
}
