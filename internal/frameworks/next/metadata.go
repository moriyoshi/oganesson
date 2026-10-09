package next

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif" // image.DecodeConfig formats for metadata images
	_ "image/jpeg"
	_ "image/png"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/moriyoshi/oganesson/internal/bundle/rsc"
)

// This file ports Next's metadata file conventions (icon, apple-icon,
// opengraph-image, twitter-image, favicon.ico, manifest, robots, sitemap):
//
//   - discovery and route naming: next/dist/lib/metadata/is-metadata-route.js,
//     get-metadata-route.js and build/route-discovery.js (createPagesMapping);
//   - the loader tree's `metadata` entries: build/webpack/loaders/metadata/discover.js;
//   - the generated modules: next-metadata-image-loader.js and
//     next-metadata-route-loader.js.
//
// Like tree.go, the code generators are Go ports; what they emit calls Next's
// own runtime helpers (resolveRouteData, fillMetadataSegment, NextResponse).

const (
	metadataRouteModule = "og:metadata-route:" // + page
	metadataImageModule = "og:metadata-image:" // + query (type, segment, file)
)

type staticMetadataImage struct {
	filename   string
	extensions []string
}

// staticMetadataImages mirrors STATIC_METADATA_IMAGES.
var staticMetadataImages = map[string]staticMetadataImage{
	"icon":      {"icon", []string{"ico", "jpg", "jpeg", "png", "svg"}},
	"apple":     {"apple-icon", []string{"jpg", "jpeg", "png"}},
	"favicon":   {"favicon", []string{"ico"}},
	"openGraph": {"opengraph-image", []string{"jpg", "jpeg", "png", "gif"}},
	"twitter":   {"twitter-image", []string{"jpg", "jpeg", "png", "gif"}},
}

// imageExtMimeTypes mirrors next/dist/lib/mime-type's imageExtMimeTypeMap.
var imageExtMimeTypes = map[string]string{
	"jpeg": "image/jpeg", "png": "image/png", "ico": "image/x-icon", "svg": "image/svg+xml",
	"avif": "image/avif", "webp": "image/webp", "gif": "image/gif",
}

func extensionRegex(static, dynamic []string) string {
	if len(dynamic) == 0 {
		return `(\.(?:` + strings.Join(static, "|") + `))`
	}
	return `(?:\.(` + strings.Join(static, "|") + `)|(\.(` + strings.Join(dynamic, "|") + `)))`
}

// isMetadataRouteFile ports isMetadataRouteFile. p is app-dir relative and
// starts with "/". strict requires the extension and forbids the "-hash"
// suffix of normalized routes.
func isMetadataRouteFile(p string, pageExts []string, strict bool) bool {
	if len(p) < 2 {
		return false
	}
	switch p {
	case "/favicon.ico", "/robots.txt", "/manifest.json", "/manifest.webmanifest":
		return true
	}
	if strings.HasSuffix(p, "/sitemap.xml") {
		return true
	}
	trailing, suffix := "?$", `\d?(-\w{6})?`
	if strict {
		trailing, suffix = "$", `\d?`
	}
	exts := func(extra ...string) []string { return append(slices.Clone(pageExts), extra...) }
	res := []string{
		`^/robots` + extensionRegex(exts("txt"), nil) + trailing,
		`^/manifest` + extensionRegex(exts("webmanifest", "json"), nil) + trailing,
		`/sitemap` + extensionRegex([]string{"xml"}, pageExts) + trailing,
	}
	for _, typ := range []string{"icon", "apple", "openGraph", "twitter"} {
		img := staticMetadataImages[typ]
		res = append(res, `/`+img.filename+suffix+extensionRegex(img.extensions, pageExts)+trailing)
	}
	for _, re := range res {
		if regexp.MustCompile(re).MatchString(p) {
			return true
		}
	}
	return false
}

// djb2Hash ports next/dist/shared/lib/hash's djb2Hash (UTF-16 code units).
func djb2Hash(s string) uint32 {
	h := uint32(5381)
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			h = h<<5 + h + uint32(0xD800+(r>>10))
			h = h<<5 + h + uint32(0xDC00+(r&0x3FF))
			continue
		}
		h = h<<5 + h + uint32(r)
	}
	return h
}

// metadataRouteSuffix ports getMetadataRouteSuffix: a hash of the parent
// path when it contains route groups or parallel slots, except for sitemaps.
func metadataRouteSuffix(page string) string {
	if strings.HasSuffix(page, "/sitemap") || strings.HasSuffix(page, "/sitemap.xml") {
		return ""
	}
	parent := path.Dir(page)
	for _, seg := range strings.Split(parent, "/") {
		if isGroupSegment(seg) || (strings.HasPrefix(seg, "@") && seg != "@children") {
			s := strconv.FormatUint(uint64(djb2Hash(parent)), 36)
			return s[:min(6, len(s))]
		}
	}
	return ""
}

// parsePath splits like Node's path.parse: name and ext of the last element.
func parsePath(p string) (dir, name, ext string) {
	dir, base := path.Split(p)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" && strings.HasPrefix(p, "/") {
		dir = "/"
	}
	ext = path.Ext(base)
	if ext == base { // ".hidden" has no extension
		ext = ""
	}
	return dir, strings.TrimSuffix(base, ext), ext
}

// normalizeMetadataRoute ports normalizeMetadataRoute: "/icon.png" ->
// "/icon.png/route", "/robots" -> "/robots.txt/route", "/(g)/icon" ->
// "/(g)/icon-<hash>/route".
func normalizeMetadataRoute(page string) string {
	if strings.HasSuffix(page, "/route") || !isMetadataRouteFile(page, nil, false) {
		return page
	}
	route, suffix := page, ""
	switch page {
	case "/robots":
		route += ".txt"
	case "/manifest":
		route += ".webmanifest"
	default:
		suffix = metadataRouteSuffix(page)
	}
	dir, name, ext := parsePath(route)
	if suffix != "" {
		suffix = "-" + suffix
	}
	return path.Join(dir, name+suffix+ext, "route")
}

// normalizeMetadataPageToRoute ports normalizeMetadataPageToRoute.
func normalizeMetadataPageToRoute(page string, dynamic bool) string {
	isRoute := strings.HasSuffix(page, "/route")
	p := strings.TrimSuffix(page, "/route")
	if dynamic {
		p += "/[__metadata_id__]"
	} else if strings.HasSuffix(p, "/sitemap") {
		p += ".xml"
	}
	if isRoute {
		p += "/route"
	}
	return p
}

// metadataRoute is a metadata file served as a route handler.
type metadataRoute struct {
	Page    string // e.g. "/favicon.ico/route", "/sitemap.xml/route"
	File    string // absolute source file
	Dynamic bool   // a page-extension module (sitemap.ts) rather than a static file
}

// scanMetadataRoutes finds metadata files in the app directory, as
// route-discovery's createPagesMapping does with the app file matcher.
func scanMetadataRoutes(appDir string, exts []string) ([]metadataRoute, error) {
	var out []metadataRoute
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
		rel, _ := filepath.Rel(appDir, p)
		rel = "/" + filepath.ToSlash(rel)
		if !isMetadataRouteFile(rel, exts, true) {
			return nil
		}
		pageKey, dynamic := rel, false
		sorted := slices.Clone(exts)
		sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
		for _, e := range sorted {
			if strings.HasSuffix(pageKey, "."+e) {
				pageKey, dynamic = strings.TrimSuffix(pageKey, "."+e), true
				break
			}
		}
		generates := false
		if dynamic {
			names, err := rsc.ProbeExports(p)
			if err != nil {
				return err
			}
			generates = slices.Contains(names, "generateImageMetadata") || slices.Contains(names, "generateSitemaps")
		}
		out = append(out, metadataRoute{
			Page:    normalizeMetadataPageToRoute(normalizeMetadataRoute(pageKey), generates),
			File:    p,
			Dynamic: dynamic,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Page < out[j].Page })
	return out, err
}

// staticMetadataRoute ports getStaticMetadataRoute: the URL directory of a
// segment, with dynamic segments flattened ("[slug]" -> "-").
func staticMetadataRoute(segment string) string {
	parts := []string{"/"}
	for _, s := range strings.Split(NormalizeAppPath(segment), "/") {
		if s == "" || s == "." {
			continue
		}
		for {
			m := parameterPattern.FindStringSubmatch(s)
			if m == nil {
				break
			}
			s = m[1] + "-" + m[3]
		}
		parts = append(parts, s)
	}
	return path.Join(parts...)
}

// parameterPattern is get-dynamic-param's PARAMETER_PATTERN.
var parameterPattern = regexp.MustCompile(`^([^\[]*)\[((?:\[[^\]]*\])|[^\]]+)\](.*)$`)

// fillStaticMetadataSegment ports fillStaticMetadataSegment.
func fillStaticMetadataSegment(segment, lastSegment string) string {
	_, name, ext := parsePath(lastSegment)
	suffix := metadataRouteSuffix(path.Join(segment, name))
	if suffix != "" {
		suffix = "-" + suffix
	}
	return path.Join(staticMetadataRoute(segment), name+suffix+ext)
}

// segmentMetadata ports createStaticMetadataFromRoute and
// createMetadataExportsCode: the `metadata` property of a loader tree node
// for the files in segment's directory, or "" when there are none.
func (b *treeBuilder) segmentMetadata(segment string, isRootLayoutOrRootPage bool) string {
	dir := filepath.Join(b.appDir, filepath.FromSlash(segment))
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return ""
	}
	pageExts := make([]string, len(b.extensions))
	for i, e := range b.extensions {
		pageExts[i] = strings.TrimPrefix(e, ".")
	}
	enum := func(filename string, exts []string, numeric bool) []string {
		names := []string{filename}
		if numeric {
			for i := range 10 {
				names = append(names, filename+strconv.Itoa(i))
			}
		}
		var files []string
		for _, n := range names {
			for _, e := range exts {
				p := filepath.Join(dir, n+"."+e)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					files = append(files, p)
				}
			}
		}
		return files
	}
	found := false
	images := map[string][]string{}
	manifest := "undefined"
	collect := func(typ string) {
		if typ == "manifest" {
			static := []string{"webmanifest", "json"}
			if files := enum("manifest", append(slices.Clone(static), pageExts...), false); len(files) > 0 {
				found = true
				_, name, ext := parsePath(filepath.ToSlash(files[0]))
				ext = strings.TrimPrefix(ext, ".")
				if !slices.Contains(static, ext) {
					ext = "webmanifest"
				}
				manifest = jsonStr(fmt.Sprintf("%s/%s.%s", b.basePath, name, ext))
			}
			return
		}
		img := staticMetadataImages[typ]
		exts := slices.Clone(img.extensions)
		if typ != "favicon" {
			exts = append(exts, pageExts...)
		}
		files := enum(img.filename, exts, typ != "favicon")
		sort.Strings(files)
		for _, f := range files {
			found = true
			q := url.Values{"type": {typ}, "segment": {segment}, "file": {f}}
			mod := fmt.Sprintf("(async (props) => (await import(%s)).default(props))", jsonStr(metadataImageModule+q.Encode()))
			if typ == "favicon" {
				images["icon"] = append([]string{mod}, images["icon"]...)
			} else {
				images[typ] = append(images[typ], mod)
			}
		}
	}
	for _, typ := range []string{"icon", "apple", "openGraph", "twitter"} {
		collect(typ)
	}
	if isRootLayoutOrRootPage {
		collect("favicon")
		collect("manifest")
	}
	if !found {
		return ""
	}
	return fmt.Sprintf("metadata: {\n    icon: [%s],\n    apple: [%s],\n    openGraph: [%s],\n    twitter: [%s],\n    manifest: %s\n  }",
		strings.Join(images["icon"], ","), strings.Join(images["apple"], ","),
		strings.Join(images["openGraph"], ","), strings.Join(images["twitter"], ","), manifest)
}

// metadataImageSource ports next-metadata-image-loader for the module
// key (the query after metadataImageModule).
func (b *builder) metadataImageSource(key string) (string, error) {
	q, err := url.ParseQuery(key)
	if err != nil {
		return "", err
	}
	typ, segment, file := q.Get("type"), q.Get("segment"), q.Get("file")
	_, fileNameBase, ext := parsePath(filepath.ToSlash(file))
	extension := strings.TrimPrefix(ext, ".")
	interpolatedName := fileNameBase + ext
	if extension == "jpg" {
		extension = "jpeg"
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	hashQuery := "?" + contentHash(content)
	pathnamePrefix := path.Join(b.basePath(), segment)
	useNumericSizes := typ == "twitter" || typ == "openGraph"

	if slices.Contains(b.exts, extension) {
		names, err := rsc.ProbeExports(file)
		if err != nil {
			return "", err
		}
		var fields []string
		for _, n := range names {
			if n != "default" {
				fields = append(fields, n)
			}
		}
		imports, members := make([]string, len(fields)), make([]string, len(fields))
		for i, f := range fields {
			imports[i] = f + " as _" + f
			members[i] = f + ": _" + f
		}
		sizes := `data.sizes = size.width + "x" + size.height;`
		if useNumericSizes {
			sizes = `data.width = size.width; data.height = size.height;`
		}
		return `import { ` + strings.Join(imports, ", ") + ` } from ` + jsonStr(file) + `
import { fillMetadataSegment } from 'next/dist/lib/metadata/get-metadata-route'

const imageModule = { ` + strings.Join(members, ", ") + ` }

function getImageMetadata(imageMetadata, idParam, resolvedParams) {
  const imageUrl = fillMetadataSegment(` + jsonStr(pathnamePrefix) + `, resolvedParams, ` + jsonStr(fileNameBase) + `, false)
  const data = {
    alt: imageMetadata.alt,
    type: imageMetadata.contentType || 'image/png',
    url: imageUrl + (idParam ? ('/' + idParam) : '') + ` + jsonStr(hashQuery) + `,
  }
  const { size } = imageMetadata
  if (size) {
    ` + sizes + `
  }
  return data
}

export default async function (props) {
  const { generateImageMetadata } = imageModule
  const resolvedParams = await props.params
  if (generateImageMetadata) {
    const imageMetadataArray = await generateImageMetadata({ params: resolvedParams })
    return imageMetadataArray.map((imageMetadata) => getImageMetadata(imageMetadata, imageMetadata.id + '', resolvedParams))
  }
  return [getImageMetadata(imageModule, '', resolvedParams)]
}
`, nil
	}

	width, height, err := imageSize(content, extension)
	if err != nil {
		return "", fmt.Errorf("Process image %q failed: %w", path.Join(cmpOr(segment, "/"), interpolatedName), err)
	}
	// Key order follows the loader's object spread: type, then sizes or
	// width/height, then alt.
	var fields []string
	if mt, ok := imageExtMimeTypes[extension]; ok {
		fields = append(fields, `"type":`+jsonStr(mt))
	}
	if useNumericSizes && width > 0 && height > 0 {
		fields = append(fields, fmt.Sprintf(`"width":%d,"height":%d`, width, height))
	} else {
		sizes := "any"
		if extension != "svg" && width > 0 && height > 0 {
			sizes = fmt.Sprintf("%dx%d", width, height)
		}
		fields = append(fields, `"sizes":`+jsonStr(sizes))
	}
	if useNumericSizes {
		if alt, err := os.ReadFile(filepath.Join(filepath.Dir(file), fileNameBase+".alt.txt")); err == nil {
			fields = append(fields, `"alt":`+jsonStr(string(alt)))
		}
	}
	imageURL := fillStaticMetadataSegment(pathnamePrefix, interpolatedName)
	return `export default function () {
  const imageData = {` + strings.Join(fields, ",") + `}
  return [{
    ...imageData,
    url: ` + jsonStr(imageURL+hashQuery) + `,
  }]
}
`, nil
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// contentHash stands in for webpack's [contenthash]; only its stability matters.
func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// imageSize reports an image's dimensions as image-size does: by the
// content, whatever the extension (a PNG named icon.ico is a PNG), the
// first image of an ICO. SVG sizes are never used, so they are not
// computed.
func imageSize(b []byte, ext string) (w, h int, err error) {
	isICO := len(b) >= 22 && binary.LittleEndian.Uint16(b[0:]) == 0 && (binary.LittleEndian.Uint16(b[2:]) == 1 || binary.LittleEndian.Uint16(b[2:]) == 2) && binary.LittleEndian.Uint16(b[4:]) != 0
	switch {
	case ext == "svg":
		return 0, 0, nil
	case isICO:
		w, h = int(b[6]), int(b[7])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		return w, h, nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

func (b *builder) basePath() string {
	s, _ := b.config["basePath"].(string)
	return s
}

const (
	cacheNoCache    = "no-cache, no-store"
	cacheRevalidate = "public, max-age=0, must-revalidate"
)

// metadataContentType ports next-metadata-route-loader's getContentType.
func metadataContentType(file string) string {
	name, ext, _ := strings.Cut(filepath.Base(file), ".")
	ext, _, _ = strings.Cut(ext, ".")
	if ext == "jpg" {
		ext = "jpeg"
	}
	switch {
	case name == "favicon" && ext == "ico":
		return "image/x-icon"
	case name == "sitemap":
		return "application/xml"
	case name == "robots":
		return "text/plain"
	case name == "manifest":
		return "application/manifest+json"
	case ext == "png" || ext == "jpeg" || ext == "ico" || ext == "svg":
		return imageExtMimeTypes[ext]
	}
	return "text/plain"
}

// metadataRouteSource ports next-metadata-route-loader: the userland route
// module for a metadata file.
func (b *builder) metadataRouteSource(m metadataRoute) (string, error) {
	name, _, _ := strings.Cut(filepath.Base(m.File), ".")
	if !m.Dynamic {
		content, err := os.ReadFile(m.File)
		if err != nil {
			return "", err
		}
		isTwitter, isOpenGraph := name == "twitter-image", name == "opengraph-image"
		limit, imgName := 8, "Open Graph"
		if isTwitter {
			limit, imgName = 5, "Twitter"
		}
		return `/* static asset route */
import { NextResponse } from 'next/server'

const contentType = ` + jsonStr(metadataContentType(m.File)) + `
const buffer = Buffer.from(` + jsonStr(base64.StdEncoding.EncodeToString(content)) + `, 'base64')

if (` + strconv.FormatBool(isTwitter || isOpenGraph) + `) {
  const fileSizeInMB = buffer.byteLength / 1024 / 1024
  if (fileSizeInMB > ` + strconv.Itoa(limit) + `) {
    throw new Error('File size for ` + imgName + ` image ' + ` + jsonStr(jsonStr(m.File)) + ` + ' exceeds ` + strconv.Itoa(limit) + `MB. ' +
    ` + "`(Current: ${fileSizeInMB.toFixed(2)}MB)\\n`" + ` +
    'Read more: https://nextjs.org/docs/app/api-reference/file-conventions/metadata/opengraph-image#image-files-jpg-png-gif'
    )
  }
}

export function GET() {
  return new NextResponse(buffer, {
    headers: {
      'Content-Type': contentType,
      'Cache-Control': ` + jsonStr(cacheRevalidate) + `,
    },
  })
}

export const dynamic = 'force-static'
`, nil
	}

	names, err := rsc.ProbeExports(m.File)
	if err != nil {
		return "", err
	}
	var reExports []string
	for _, n := range names {
		if n != "default" && n != "generateSitemaps" {
			reExports = append(reExports, n)
		}
	}
	reExportCode := ""
	if len(reExports) > 0 {
		reExportCode = "export { " + strings.Join(reExports, ", ") + " } from " + jsonStr(m.File) + "\n"
	}
	header := func(comment, imports string) string {
		return "/* " + comment + " */\nimport { NextResponse } from 'next/server'\n" + imports +
			"\nconst contentType = " + jsonStr(metadataContentType(m.File)) +
			"\nconst fileType = " + jsonStr(name) + "\n" +
			"\nif (typeof handler !== 'function') {\n  throw new Error('Default export is missing in ' + " + jsonStr(jsonStr(m.File)) + ")\n}\n" +
			reExportCode
	}
	resolveRouteData := "import { resolveRouteData } from 'next/dist/build/webpack/loaders/metadata/resolve-route-data'\n"
	textGET := `
export async function GET() {
  const data = await handler()
  const content = resolveRouteData(data, fileType)
  return new NextResponse(content, {
    headers: {
      'Content-Type': contentType,
      'Cache-Control': ` + jsonStr(cacheRevalidate) + `,
    },
  })
}
`
	switch {
	case name == "robots" || name == "manifest":
		return header("dynamic asset route", "import handler from "+jsonStr(m.File)+"\n"+resolveRouteData) + textGET, nil
	case name == "sitemap" && slices.Contains(names, "generateSitemaps"):
		return header("dynamic sitemap route with generateSitemaps",
			"import { default as handler, generateSitemaps } from "+jsonStr(m.File)+"\n"+resolveRouteData) + `
export async function GET(_, ctx) {
  const id = await ctx.params.then(params => params?.__metadata_id__)
  const hasXmlExtension = id ? id.endsWith('.xml') : false
  const baseId = id && hasXmlExtension ? id.slice(0, -4) : undefined
  const sitemaps = await generateSitemaps()
  let foundId
  for (const item of sitemaps) {
    if (item?.id == null) {
      throw new Error('id property is required for every item returned from generateSitemaps')
    }
    if (item.id.toString() === baseId) {
      foundId = item.id
    }
  }
  if (foundId == null) {
    return new NextResponse('Not Found', { status: 404 })
  }
  const data = await handler({ id: Promise.resolve(baseId) })
  const content = resolveRouteData(data, fileType)
  return new NextResponse(content, {
    headers: {
      'Content-Type': contentType,
      'Cache-Control': ` + jsonStr(cacheRevalidate) + `,
    },
  })
}

export async function generateStaticParams() {
  const sitemaps = await generateSitemaps()
  const params = []
  for (const item of sitemaps) {
    if (item?.id == null) {
      throw new Error('id property is required for every item returned from generateSitemaps')
    }
    params.push({ __metadata_id__: item.id.toString() + '.xml' })
  }
  return params
}
`, nil
	case name == "sitemap":
		return header("single sitemap route", "import { default as handler } from "+jsonStr(m.File)+"\n"+resolveRouteData) + textGET, nil
	case slices.Contains(names, "generateImageMetadata"):
		return header("dynamic image route with generateImageMetadata",
			"import { default as handler, generateImageMetadata } from "+jsonStr(m.File)+"\n") + `
export async function GET(_, ctx) {
  const paramsPromise = ctx.params
  const idPromise = paramsPromise.then(params => params?.__metadata_id__)
  const restParamsPromise = paramsPromise.then(params => {
    if (!params) return undefined
    const { __metadata_id__, ...rest } = params
    return rest
  })
  const restParams = await restParamsPromise
  const __metadata_id__ = await idPromise
  const imageMetadata = await generateImageMetadata({ params: restParams })
  const id = imageMetadata.find((item) => {
    if (item?.id == null) {
      throw new Error('id property is required for every item returned from generateImageMetadata')
    }
    return item.id.toString() === __metadata_id__
  })?.id
  if (id == null) {
    return new NextResponse('Not Found', { status: 404 })
  }
  return handler({ params: restParamsPromise, id: idPromise })
}

export async function generateStaticParams({ params }) {
  const imageMetadata = await generateImageMetadata({ params })
  const staticParams = []
  for (const item of imageMetadata) {
    if (item?.id == null) {
      throw new Error('id property is required for every item returned from generateImageMetadata')
    }
    staticParams.push({ __metadata_id__: item.id.toString() })
  }
  return staticParams
}
`, nil
	default:
		return header("dynamic image route without generateImageMetadata",
			"import { default as handler } from "+jsonStr(m.File)+"\n") + `
export async function GET(_, ctx) {
  return handler({ params: ctx.params })
}
`, nil
	}
}
