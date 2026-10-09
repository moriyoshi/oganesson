package next

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/moriyoshi/oganesson/internal/bundle/rsc"
)

// Server actions from module-level "use server" files, replacing the parts
// of Next's SWC server-actions transform and flight loaders that Oganesson needs.
//
// Inline "use server" functions are inline_actions.go.
//
// "use cache" functions are hoisted with them (inline_actions.go).

var useServerRe = regexp.MustCompile(rsc.DirectivePrologue + `['"]use server['"]`)
var inlineUseServerRe = regexp.MustCompile(`\{\s*['"]use server['"]`)

// inlineDirectiveRe finds a function body starting with "use server" or
// "use cache[: kind]"; useCacheFileRe a module-level "use cache" (its
// directive in the first group).
var inlineDirectiveRe = regexp.MustCompile(`\{\s*['"]use (?:server|cache)(?::[^'"]*)?['"]`)
var useCacheFileRe = regexp.MustCompile(rsc.DirectivePrologue + `['"](use cache(?::[^'"]*)?)['"]`)

const (
	actionsModuleID = "og:actions" // __next_require__ id of the action registry
	serverRefModule = "next/dist/build/webpack/loaders/next-flight-loader/server-reference"
	actionValidate  = "next/dist/build/webpack/loaders/next-flight-loader/action-validate"
	actionClient    = "next/dist/build/webpack/loaders/next-flight-loader/action-client-wrapper"
)

type actionModule struct {
	Path string
	IDs  map[string]string // export name -> action id
	// exports reports a "use server" module's exports recorded (addActions),
	// besides functions hoisted from it.
	exports bool
}

// actionInfoByte marks an id as a server action whose arguments are all
// used (server-reference-info.js: type bit 0, arg mask 0b111111, rest 1).
const actionInfoByte = "7f"

func (b *builder) actionID(path, export string) string {
	sum := sha1.Sum([]byte(b.buildID + path + ":" + export))
	return actionInfoByte + hex.EncodeToString(sum[:])
}

// cacheID is a cached function's id: its info byte, then the hash.
func (b *builder) cacheID(path, export string, info byte) string {
	sum := sha1.Sum([]byte(b.buildID + path + ":" + export))
	return hex.EncodeToString([]byte{info}) + hex.EncodeToString(sum[:])
}

// addActions records a "use server" module and assigns ids to its exports.
func (b *builder) addActions(path string) (*actionModule, error) {
	b.mu.Lock()
	m, ok := b.actions[path]
	b.mu.Unlock()
	if ok && m.exports {
		return m, nil
	}
	exports, err := rsc.ProbeExports(path)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if m = b.actions[path]; m == nil {
		m = &actionModule{Path: path, IDs: map[string]string{}}
		b.actions[path] = m
	}
	// The module's exports join the functions hoisted from it (its inline
	// "use cache" functions), if any.
	for _, e := range exports {
		if _, ok := m.IDs[e]; !ok {
			m.IDs[e] = b.actionID(path, e)
		}
	}
	m.exports = true
	return m, nil
}

func (m *actionModule) names() []string {
	out := make([]string, 0, len(m.IDs))
	for n := range m.IDs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// serverActionSource appends registrations to a "use server" module in the
// RSC layer (what SWC emits after the original code).
func serverActionSource(src string, m *actionModule) string {
	var sb strings.Builder
	sb.WriteString(src)
	fmt.Fprintf(&sb, "\nimport { registerServerReference as __og_rsr } from %s;\n", jsonStr(serverRefModule))
	fmt.Fprintf(&sb, "import { ensureServerEntryExports as __og_ese } from %s;\n", jsonStr(actionValidate))
	fmt.Fprintf(&sb, "import * as __og_self from %s;\n", jsonStr(m.Path))
	var refs []string
	for _, n := range m.names() {
		refs = append(refs, fmt.Sprintf("__og_self[%s]", jsonStr(n)))
	}
	fmt.Fprintf(&sb, "__og_ese([%s]);\n", strings.Join(refs, ", "))
	for _, n := range m.names() {
		fmt.Fprintf(&sb, "__og_rsr(__og_self[%s], %s, null);\n", jsonStr(n), jsonStr(m.IDs[n]))
	}
	return sb.String()
}

// clientActionSource replaces a "use server" module in client layers (SSR
// and browser) with references that call the server.
func clientActionSource(m *actionModule) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "import { createServerReference, callServer, findSourceMapURL } from %s;\n", jsonStr(actionClient))
	for _, n := range m.names() {
		ref := fmt.Sprintf("createServerReference(%s, callServer, void 0, findSourceMapURL, %s)", jsonStr(m.IDs[n]), jsonStr(n))
		if n == "default" {
			fmt.Fprintf(&sb, "export default /*#__PURE__*/ %s;\n", ref)
		} else {
			fmt.Fprintf(&sb, "export const %s = /*#__PURE__*/ %s;\n", n, ref)
		}
	}
	return sb.String()
}

func (b *builder) sortedActions() []*actionModule {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*actionModule, 0, len(b.actions))
	for _, m := range b.actions {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// actionsRegistrySource is the RSC-layer module behind actionsModuleID:
// action id -> function.
func (b *builder) actionsRegistrySource() string {
	var imports, table strings.Builder
	for i, m := range b.sortedActions() {
		fmt.Fprintf(&imports, "import * as a%d from %s;\n", i, jsonStr(m.Path))
		for _, n := range m.names() {
			fmt.Fprintf(&table, "  %s: a%d[%s],\n", jsonStr(m.IDs[n]), i, jsonStr(n))
		}
	}
	return imports.String() + "export const actions = {\n" + table.String() + "};\n"
}

// serverActionsManifest is __RSC_SERVER_MANIFEST: every page can run every
// action through the registry.
func (b *builder) serverActionsManifest() map[string]any {
	edge := map[string]any{}
	for _, m := range b.sortedActions() {
		for _, n := range m.names() {
			workers := map[string]any{}
			for _, r := range b.routes {
				workers["app"+r.Page] = map[string]any{"moduleId": actionsModuleID, "async": false}
			}
			edge[m.IDs[n]] = map[string]any{"workers": workers, "layer": map[string]any{}, "exportedName": n, "filename": m.Path}
		}
	}
	return map[string]any{"node": map[string]any{}, "edge": edge, "encryptionKey": "process.env.NEXT_SERVER_ACTIONS_ENCRYPTION_KEY"}
}
