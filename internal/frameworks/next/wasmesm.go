package next

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// webpack's experiments.asyncWebAssembly (with-webassembly sets it in
// next.config's webpack()): a .wasm import is an ES module whose exports
// are the instance's, the wasm's imports resolved as modules relative to
// it (wasm-bindgen's "./x_bg.js"). In the server build the module
// instantiates synchronously when it loads; webpack's module is async,
// which its importers (an await import() in with-webassembly) do not see.

// wasmESMNamespace is the namespace of .wasm files loaded as ES modules.
const wasmESMNamespace = "og-wasm-esm"

// wasmESMResolve claims .wasm imports when the server's webpack() hook
// enables asyncWebAssembly.
func (b *builder) wasmESMResolve(a api.OnResolveArgs) (api.OnResolveResult, bool) {
	if h := b.hooks["server"]; h == nil || !h.AsyncWebAssembly {
		return api.OnResolveResult{}, false
	}
	switch a.Kind {
	case api.ResolveJSImportStatement, api.ResolveJSDynamicImport, api.ResolveJSRequireCall:
	default:
		return api.OnResolveResult{}, false
	}
	p := a.Path
	if !filepath.IsAbs(p) {
		if !strings.HasPrefix(p, "./") && !strings.HasPrefix(p, "../") {
			return api.OnResolveResult{}, false
		}
		p = filepath.Join(a.ResolveDir, p)
	}
	if !fileExists(p) {
		return api.OnResolveResult{}, false
	}
	return api.OnResolveResult{Path: p, Namespace: wasmESMNamespace}, true
}

// wasmESMSource is the ES module standing in for the .wasm file at path.
func wasmESMSource(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	imports, exports, err := wasmInterface(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "import bytes from %s;\n", jsonStr(binaryPrefix+filepath.Base(path)))
	var entries []string
	for i, m := range imports {
		spec := m
		if !strings.HasPrefix(spec, ".") && !strings.HasPrefix(spec, "/") && !strings.Contains(spec, ":") {
			spec = "./" + spec // webpack resolves wasm import modules from the wasm file
		}
		fmt.Fprintf(&sb, "import * as __og_wasm_import%d from %s;\n", i, jsonStr(spec))
		entries = append(entries, fmt.Sprintf("%s: __og_wasm_import%d", jsonStr(m), i))
	}
	fmt.Fprintf(&sb, "const instance = new WebAssembly.Instance(new WebAssembly.Module(bytes), { %s });\n", strings.Join(entries, ", "))
	for i, e := range exports {
		fmt.Fprintf(&sb, "const __og_wasm_export%d = instance.exports[%s];\nexport { __og_wasm_export%d as %s };\n", i, jsonStr(e), i, jsonStr(e))
	}
	return sb.String(), nil
}

// wasmInterface lists a WebAssembly binary's import module names (each
// once, in order) and export names.
func wasmInterface(data []byte) (imports, exports []string, err error) {
	if len(data) < 8 || !bytes.Equal(data[:4], []byte("\x00asm")) {
		return nil, nil, errors.New("not a WebAssembly binary")
	}
	r := bytes.NewReader(data[8:])
	u32 := func() uint32 {
		v, e := binary.ReadUvarint(r)
		if e != nil && err == nil {
			err = e
		}
		return uint32(v)
	}
	name := func() string {
		n := u32()
		if err != nil || int(n) > r.Len() {
			if err == nil {
				err = errors.New("truncated name")
			}
			return ""
		}
		b := make([]byte, n)
		_, _ = r.Read(b)
		return string(b)
	}
	limits := func() {
		flags, _ := r.ReadByte()
		u32()
		if flags&1 != 0 {
			u32()
		}
	}
	seen := map[string]bool{}
	for r.Len() > 0 && err == nil {
		id, _ := r.ReadByte()
		size := u32()
		if err != nil || int(size) > r.Len() {
			return nil, nil, errors.New("truncated section")
		}
		start := r.Len()
		switch id {
		case 2: // imports: module, field, description
			for n := u32(); n > 0 && err == nil; n-- {
				mod := name()
				name()
				kind, _ := r.ReadByte()
				switch kind {
				case 0: // function: type index
					u32()
				case 1: // table: reference type, limits
					_, _ = r.ReadByte()
					limits()
				case 2: // memory
					limits()
				case 3: // global: value type, mutability
					_, _ = r.ReadByte()
					_, _ = r.ReadByte()
				case 4: // tag: attribute, type index
					_, _ = r.ReadByte()
					u32()
				default:
					return nil, nil, fmt.Errorf("unknown import kind %d", kind)
				}
				if !seen[mod] {
					seen[mod] = true
					imports = append(imports, mod)
				}
			}
		case 7: // exports: name, kind, index
			for n := u32(); n > 0 && err == nil; n-- {
				exports = append(exports, name())
				_, _ = r.ReadByte()
				u32()
			}
		default:
			if _, e := r.Seek(int64(size), 1); e != nil {
				return nil, nil, e
			}
		}
		if consumed := start - r.Len(); id == 2 || id == 7 {
			if consumed != int(size) && err == nil {
				err = fmt.Errorf("section %d: read %d of %d bytes", id, consumed, size)
			}
		}
	}
	return imports, exports, err
}
