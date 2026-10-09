package next

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/microsoft/TypeScript/tsc/ogapi"
)

// Inline Server Actions: functions of a server module whose bodies start
// with "use server" (a form action defined inside a Server Component).
// This is next-swc's server_actions transform for them:
//
//   - each is hoisted to the module's top level as an exported
//     $$RSC_SERVER_ACTION_<n>, registered as a server reference;
//   - the values it closes over (variables of the enclosing functions,
//     narrowed to the property paths it reads) become bound arguments,
//     encrypted where the action is defined and decrypted when it runs:
//     `$$RSC_SERVER_ACTION_0.bind(null, encryptActionBoundArgs(id, a, b))`
//     at the original place, `var [$$ACTION_ARG_0, $$ACTION_ARG_1] = await
//     decryptActionBoundArgs(id, $$ACTION_CLOSURE_BOUND)` in the body.
//   - an action nested in another is hoisted too, and replaced inside its
//     parent's hoisted body by its own binding (next accepts nested actions).
//
// The hoisted exports are the module's actions (actionModule), in the
// registry and manifest like a "use server" module's.

const actionEncryption = "next/dist/server/app-render/encryption"

// inlineActionName is the export next-swc gives the n-th hoisted action.
func inlineActionName(n int) string { return fmt.Sprintf("$$RSC_SERVER_ACTION_%d", n) }

// transformInlineActions hoists the inline Server Actions and cached
// functions ("use cache") of the RSC-layer module at path; fileCache is a
// module-level "use cache[: kind]" directive ("" without), whose exported
// async functions are cached too. moduleUseServer is set when the module
// itself is a "use server" module: its top-level "use server" functions are
// the module's own actions (registered by addActions/serverActionSource), so
// they are not hoisted here (only its inline "use cache" functions are). It
// returns the code unchanged when there is nothing to hoist.
func (b *builder) transformInlineActions(path, code, fileCache string, moduleUseServer bool) (string, error) {
	all := ogapi.ServerActions(path, code)
	if fileCache != "" {
		inline := map[int]bool{}
		for _, a := range all {
			inline[a.Start] = true
		}
		for _, a := range ogapi.CacheFileFunctions(path, code, fileCache) {
			if !inline[a.Start] {
				all = append(all, a)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Start < all[j].Start })
	}
	if len(all) == 0 {
		return code, nil
	}

	// Nesting: parent[i] is the innermost action whose body contains all[i]
	// (walk order lists an action before the ones nested in it), or -1.
	parent := actionParents(all)

	// A "use server" module's top-level "use server" functions are its own
	// actions, registered by addActions/serverActionSource after this
	// transform. Hoisting one here too would register it twice (once under an
	// inline $$RSC_SERVER_ACTION id, once under its export id), so drop them:
	// only the functions that need inline hoisting remain, the module's inline
	// "use cache" functions and any actions nested inside those. (A top-level
	// "use server" function that itself contains a nested action is left to be
	// hoisted — an exotic shape og does not special-case.)
	if moduleUseServer {
		hasChild := make([]bool, len(all))
		for i := range all {
			if parent[i] != -1 {
				hasChild[parent[i]] = true
			}
		}
		var kept []ogapi.ServerAction
		for i, a := range all {
			if a.Directive == "use server" && parent[i] == -1 && !hasChild[i] {
				continue
			}
			kept = append(kept, a)
		}
		if len(kept) == 0 {
			return code, nil
		}
		if len(kept) != len(all) {
			all = kept
			parent = actionParents(all)
		}
	}

	type action struct {
		name, id, value string // value: the binding that replaces the function in place
		cached          bool
		args            []string
	}
	infos := make([]action, len(all))
	names := map[string]string{}
	usesCache := false

	// Pass 1: validate each action and assign its name, id and binding. The
	// binding is needed before pass 2 so a parent's body can reference the
	// bindings of the actions nested in it.
	n := 0
	for i, a := range all {
		cached := a.Directive != "use server"
		what := "Server Action"
		where := fmt.Sprintf("next: %s: inline \"use server\" function", path)
		if cached {
			what = "\"use cache\" function"
			where = fmt.Sprintf("next: %s: %q function", path, a.Directive)
		}
		if a.Name != "" {
			where += " " + a.Name
		}
		switch {
		case a.Kind == "method":
			return "", fmt.Errorf("%s: methods cannot be %ss; use a function", where, what)
		case !a.Async && cached:
			return "", fmt.Errorf("%s: \"use cache\" functions must be async functions.", where)
		case !a.Async:
			return "", fmt.Errorf("%s: Server Actions must be async functions.", where)
		case a.UsesThis:
			return "", fmt.Errorf("%s: a %s cannot use this, arguments or super", where, what)
		}
		for _, c := range a.Captured {
			for _, r := range c.Refs {
				if r.Start < a.ParamsEnd {
					return "", fmt.Errorf("%s: a parameter default reads %s, a closed-over value; read it in the body", where, c.Path)
				}
			}
		}
		// A nested action may close over an enclosing function's own
		// parameters and locals (they stay named in the hoisted parent), but
		// not a value an enclosing action itself closes over: that would need
		// the parent's decrypted argument, which og does not thread through.
		for p := parent[i]; p != -1; p = parent[p] {
			if sharesCapture(a, all[p]) {
				return "", fmt.Errorf("%s: a %s nested in another that closes over the same value is not supported yet", where, what)
			}
		}
		name := inlineActionName(n)
		if cached {
			name = inlineCacheName(n)
		}
		n++
		params := code[a.ParamsStart:a.ParamsEnd]
		var id string
		if cached {
			id = b.cacheID(path, name, cacheInfoByte(params, len(a.Captured) > 0))
		} else {
			id = b.actionID(path, name)
		}
		names[name] = id
		var args []string
		for k := range a.Captured {
			args = append(args, fmt.Sprintf("$$ACTION_ARG_%d", k))
		}
		// The binding that replaces the function where it was written (the
		// module's top level, or inside its parent's hoisted body): the
		// hoisted reference, bound to its captured values.
		value := name
		if len(a.Captured) > 0 {
			var exprs []string
			for _, c := range a.Captured {
				exprs = append(exprs, c.Path)
			}
			value = fmt.Sprintf("%s.bind(null, __og_eab(%s, %s))", name, jsonStr(id), strings.Join(exprs, ", "))
		}
		switch {
		case a.Kind != "declaration":
			value = "(" + value + ")"
		case a.Exported && a.Default && a.Name != "":
			value = fmt.Sprintf("var %s = %s; export default %s;", a.Name, value, a.Name)
		case a.Exported && a.Default:
			value = "export default " + value + ";"
		case a.Exported:
			value = fmt.Sprintf("export var %s = %s;", a.Name, value)
		default:
			value = fmt.Sprintf("var %s = %s;", a.Name, value)
		}
		infos[i] = action{name: name, id: id, value: value, cached: cached, args: args}
		if cached {
			usesCache = true
		}
	}
	b.addInlineActions(path, names)

	// Pass 2: hoist each function. Its captured reads become its decrypted
	// bound arguments, a cached function's directive goes, and each action
	// nested directly in it is replaced by that action's binding.
	var hoisted strings.Builder
	var edits []actionEdit
	for i, a := range all {
		inf := infos[i]
		params := code[a.ParamsStart:a.ParamsEnd]
		var bedits []actionEdit
		for k, c := range a.Captured {
			for _, r := range c.Refs {
				if insideChild(all, parent, i, r.Start) {
					continue // belongs to a nested action, replaced wholesale below
				}
				text := inf.args[k] + r.Suffix
				if r.Shorthand {
					text = code[r.Start:r.End] + ": " + text
				}
				bedits = append(bedits, actionEdit{r.Start, r.End, text})
			}
		}
		if inf.cached && a.DirectiveEnd > a.DirectiveStart {
			bedits = append(bedits, actionEdit{a.DirectiveStart, a.DirectiveEnd, ""})
		}
		for j := range all {
			if parent[j] == i {
				bedits = append(bedits, actionEdit{all[j].Start, all[j].End, infos[j].value})
			}
		}
		sort.Slice(bedits, func(x, y int) bool { return bedits[x].start < bedits[y].start })
		body := applyActionEdits(code, a.BodyStart, a.BodyEnd, bedits)
		fn := "function"
		if a.Name != "" {
			fn += " " + a.Name
		}
		if inf.cached {
			// next-swc: the cached body as NAME_INNER (bound values arrive
			// decrypted, as one array first), the export a React-cached
			// wrapper calling Next's cache with the arguments it uses.
			kind := "default"
			if k, ok := strings.CutPrefix(a.Directive, "use cache:"); ok {
				kind = strings.TrimSpace(k)
			}
			innerParams := params
			if len(inf.args) > 0 {
				innerParams = "[" + strings.Join(inf.args, ", ") + "]"
				if strings.TrimSpace(params) != "" {
					innerParams += ", " + params
				}
			}
			fmt.Fprintf(&hoisted, "const %s_INNER = async %s(%s) {%s};\n", inf.name, fn, innerParams, body)
			fmt.Fprintf(&hoisted, "export var %s = __og_rc(%s() { return __og_cache(%s, %s, %d, %s_INNER, %s); });\n",
				inf.name, fn, jsonStr(kind), jsonStr(inf.id), len(inf.args), inf.name, cacheArgs(params, len(a.Captured) > 0))
			fmt.Fprintf(&hoisted, "__og_rsr(%s, %s, null);\n", inf.name, jsonStr(inf.id))
			if a.Name != "" {
				fmt.Fprintf(&hoisted, "Object[\"defineProperty\"](%s, \"name\", { value: %s });\n", inf.name, jsonStr(a.Name))
			}
		} else {
			prologue := ""
			p := params
			if len(inf.args) > 0 {
				if strings.TrimSpace(params) == "" {
					p = "$$ACTION_CLOSURE_BOUND"
				} else {
					p = "$$ACTION_CLOSURE_BOUND, " + params
				}
				prologue = fmt.Sprintf("var [%s] = await __og_dab(%s, $$ACTION_CLOSURE_BOUND);", strings.Join(inf.args, ", "), jsonStr(inf.id))
			}
			fmt.Fprintf(&hoisted, "export const %s = async %s(%s) {%s%s};\n", inf.name, fn, p, prologue, body)
			fmt.Fprintf(&hoisted, "__og_rsr(%s, %s, null);\n", inf.name, jsonStr(inf.id))
		}
		// Only a top-level action is replaced in the module; a nested one was
		// replaced inside its parent's hoisted body above.
		if parent[i] == -1 {
			edits = append(edits, actionEdit{a.Start, a.End, inf.value})
		}
	}

	var sb strings.Builder
	// The hoisted functions come first (after a directive prologue), so the
	// module's own top-level code may use them.
	start := directivePrologueEnd(code)
	sb.WriteString(code[:start])
	sb.WriteString("\n")
	sb.WriteString(hoisted.String())
	at := start
	for _, e := range edits {
		sb.WriteString(code[at:e.start])
		sb.WriteString(e.text)
		at = e.end
	}
	sb.WriteString(code[at:])
	fmt.Fprintf(&sb, "\nimport { registerServerReference as __og_rsr } from %s;\n", jsonStr(serverRefModule))
	fmt.Fprintf(&sb, "import { encryptActionBoundArgs as __og_eab, decryptActionBoundArgs as __og_dab } from %s;\n", jsonStr(actionEncryption))
	if usesCache {
		fmt.Fprintf(&sb, "import { cache as __og_cache } from %s;\n", jsonStr(cacheWrapper))
		sb.WriteString("import { cache as __og_rc } from \"react\";\n")
	}
	return sb.String(), nil
}

// actionParents returns, for each action, the index of the innermost action
// whose body contains it (walk order lists an action before those nested in
// it), or -1 for a top-level one.
func actionParents(all []ogapi.ServerAction) []int {
	parent := make([]int, len(all))
	for i := range all {
		parent[i] = -1
		for j := range all {
			if i != j && all[j].BodyStart <= all[i].Start && all[i].End <= all[j].BodyEnd {
				if parent[i] == -1 || all[j].Start > all[parent[i]].Start {
					parent[i] = j
				}
			}
		}
	}
	return parent
}

type actionEdit struct {
	start, end int
	text       string
}

// applyActionEdits splices edits (sorted, non-overlapping) into code[start:end].
func applyActionEdits(code string, start, end int, edits []actionEdit) string {
	var sb strings.Builder
	at := start
	for _, e := range edits {
		sb.WriteString(code[at:e.start])
		sb.WriteString(e.text)
		at = e.end
	}
	sb.WriteString(code[at:end])
	return sb.String()
}

// insideChild reports whether position pos falls inside an action nested
// directly in all[i].
func insideChild(all []ogapi.ServerAction, parent []int, i, pos int) bool {
	for j := range all {
		if parent[j] == i && pos >= all[j].Start && pos < all[j].End {
			return true
		}
	}
	return false
}

// sharesCapture reports whether inner and outer close over the same value:
// the same source reference position appears in both.
func sharesCapture(inner, outer ogapi.ServerAction) bool {
	pos := map[int]bool{}
	for _, c := range outer.Captured {
		for _, r := range c.Refs {
			pos[r.Start] = true
		}
	}
	for _, c := range inner.Captured {
		for _, r := range c.Refs {
			if pos[r.Start] {
				return true
			}
		}
	}
	return false
}

// cacheWrapper is Next's "use cache" runtime (private-next-rsc-cache-wrapper).
const cacheWrapper = "next/dist/build/webpack/loaders/next-flight-loader/cache-wrapper"

// inlineCacheName is the export next-swc gives the n-th hoisted cached
// function (numbered with the actions).
func inlineCacheName(n int) string { return fmt.Sprintf("$$RSC_SERVER_CACHE_%d", n) }

// cacheParams counts a parameter list's parameters and reports a rest one.
func cacheParams(params string) (count int, rest bool) {
	depth := 0
	nonEmpty := false
	for i, r := range params {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				count++
				nonEmpty = false
				continue
			}
		}
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			nonEmpty = true
		}
		if depth == 0 && strings.HasPrefix(params[i:], "...") {
			rest = true
		}
	}
	if nonEmpty {
		count++
	}
	return count, rest
}

// cacheInfoByte is a cached function's id prefix (server-reference-info):
// 1 (cache), a bit per argument it uses among the first six (the bound
// values count as the first), and 1 for a rest parameter or more than six.
func cacheInfoByte(params string, bound bool) byte {
	count, rest := cacheParams(params)
	if bound {
		count++
	}
	info := byte(0x80)
	for i := 0; i < count && i < 6; i++ {
		info |= 0x40 >> i
	}
	if rest {
		info |= 0x7e
	}
	if rest || count > 6 {
		info |= 1
	}
	return info
}

// cacheArgs is the arguments a cached function's wrapper passes on: none,
// all (a rest parameter), or as many as it declares (plus the bound ones).
func cacheArgs(params string, bound bool) string {
	count, rest := cacheParams(params)
	if bound {
		count++
	}
	switch {
	case rest:
		return "Array.prototype.slice.call(arguments)"
	case count == 0:
		return "[]"
	}
	return fmt.Sprintf("Array.prototype.slice.call(arguments, 0, %d)", count)
}

var directivePrologueRe = regexp.MustCompile(`^(?:(?:\s|//[^\n]*\n|/\*[\s\S]*?\*/)*(?:'[^'\n]*'|"[^"\n]*")[ \t]*;?)*`)

// directivePrologueEnd is the offset after a module's leading directives
// ("use strict"; …), or 0.
func directivePrologueEnd(code string) int {
	m := directivePrologueRe.FindStringIndex(code)
	if m == nil {
		return 0
	}
	return m[1]
}

// addInlineActions records the hoisted actions of path (export name ->
// action id).
func (b *builder) addInlineActions(path string, ids map[string]string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.actions[path]
	if m == nil {
		m = &actionModule{Path: path, IDs: map[string]string{}}
		b.actions[path] = m
	}
	for name, id := range ids {
		m.IDs[name] = id
	}
}
