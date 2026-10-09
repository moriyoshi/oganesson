package next

import (
	"fmt"
	"sort"
	"strings"

	"github.com/microsoft/TypeScript/tsc/ogapi"
)

// ssgTransform ports next-swc's next_ssg transform
// (crates/next-custom-transforms/src/transforms/next_ssg.rs), which Next
// applies to pages in the browser build: the data functions
// (getStaticProps, getStaticPaths, getServerSideProps) are removed, together
// with the declarations and imports only they used, and the page instead
// exports __N_SSG or __N_SSP, which tells the client router to fetch
// /_next/data on navigation.
//
// Like next-swc, a declaration is only removed when the removed code
// referenced it and nothing else does; unrelated unused code stays.

var ssgExports = map[string]string{
	"getStaticProps": "__N_SSG", "getStaticPaths": "__N_SSG", "getServerSideProps": "__N_SSP",
	"unstable_getStaticProps": "__N_SSG", "unstable_getStaticPaths": "__N_SSG",
	"unstable_getServerProps": "__N_SSP", "unstable_getServerSideProps": "__N_SSP",
}

func mightExportData(code string) bool {
	return strings.Contains(code, "getStaticProps") || strings.Contains(code, "getStaticPaths") ||
		strings.Contains(code, "getServerSideProps") || strings.Contains(code, "getServerProps")
}

type span struct{ start, end int }

func ssgTransform(path, code string) (string, error) {
	if !mightExportData(code) {
		return code, nil
	}
	m := ogapi.ParseModule(path, code)
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	var removed []span
	flags := map[string]bool{}
	remove := func(start, end int, replacement string) {
		edits = append(edits, edit{start, end, replacement})
		removed = append(removed, span{start, end})
	}
	vars := map[int]ogapi.VarStatement{}
	for _, v := range m.Vars {
		vars[v.Start] = v
	}

	// 1. The data exports.
	for _, st := range m.Statements {
		switch {
		case st.Kind == "function" && st.Exported && len(st.Names) == 1 && ssgExports[st.Names[0]] != "":
			flags[ssgExports[st.Names[0]]] = true
			remove(st.Start, st.End, "")
		case st.Kind == "var" && st.Exported:
			v := vars[st.Start]
			var keep []string
			hit := false
			for _, d := range v.Decls {
				if f := ssgExports[d.Name]; f != "" {
					flags[f] = true
					hit = true
					removed = append(removed, span{d.Start, d.End})
				} else {
					keep = append(keep, code[d.Start:d.End])
				}
			}
			if !hit {
				continue
			}
			text := ""
			if len(keep) > 0 {
				text = "export " + v.Kind + " " + strings.Join(keep, ", ") + ";"
			}
			edits = append(edits, edit{st.Start, st.End, text})
		case st.Kind == "export" && !st.TypeOnly:
			var keep []string
			hit := false
			for _, s := range st.Specifiers {
				if f := ssgExports[s.Exported]; f != "" && !s.TypeOnly {
					flags[f] = true
					hit = true
					continue
				}
				spec := s.Local
				if s.Local != s.Exported {
					spec += " as " + s.Exported
				}
				if s.TypeOnly {
					spec = "type " + spec
				}
				keep = append(keep, spec)
			}
			if !hit {
				continue
			}
			text := ""
			if len(keep) > 0 {
				text = "export { " + strings.Join(keep, ", ") + " }"
				if st.From != "" {
					text += " from " + jsonStr(st.From)
				}
				text += ";"
			}
			// The clause's references to the local bindings go away with it.
			remove(st.Start, st.End, text)
		}
	}
	if len(flags) == 0 {
		return code, nil
	}
	if flags["__N_SSG"] && flags["__N_SSP"] {
		return "", fmt.Errorf("next: %s: You can not use getStaticProps or getStaticPaths with getServerSideProps. To use SSG, please remove getServerSideProps", path)
	}

	inRemoved := func(at int) bool {
		for _, r := range removed {
			if at >= r.start && at < r.end {
				return true
			}
		}
		return false
	}
	// liveAndDead reports whether name has references outside the removed
	// code (and outside its own declaration), and whether removed code used it.
	refs := func(name string, own span) (live, usedByRemoved bool) {
		for _, at := range m.Refs[name] {
			switch {
			case inRemoved(at):
				usedByRemoved = true
			case at >= own.start && at < own.end:
			default:
				live = true
			}
		}
		return
	}

	// 2. Declarations and imports that only the removed code used, to a fixpoint.
	done := map[int]bool{}
	for changed := true; changed; {
		changed = false
		for _, st := range m.Statements {
			if done[st.Start] || inRemoved(st.Start) || st.Exported || (st.Kind != "var" && st.Kind != "function" && st.Kind != "class") || len(st.Names) == 0 {
				continue
			}
			dead, used := true, false
			for _, n := range st.Names {
				live, u := refs(n, span{st.Start, st.End})
				if live {
					dead = false
				}
				used = used || u
			}
			if dead && used {
				done[st.Start] = true
				remove(st.Start, st.End, "")
				changed = true
			}
		}
	}
	for _, im := range m.Imports {
		if len(im.Bindings) == 0 || im.TypeOnly {
			continue
		}
		var keep []ogapi.ImportBinding
		dropped := false
		for _, bd := range im.Bindings {
			if live, used := refs(bd.Local, span{im.Start, im.End}); !live && used {
				dropped = true
				continue
			}
			keep = append(keep, bd)
		}
		if !dropped {
			continue
		}
		edits = append(edits, edit{im.Start, im.End, importText(im.Specifier, keep)})
	}

	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, e := range edits {
		code = code[:e.start] + e.text + code[e.end:]
	}
	for _, f := range []string{"__N_SSG", "__N_SSP"} {
		if flags[f] {
			code += "\nexport var " + f + " = true;\n"
		}
	}
	return code, nil
}

// importText rebuilds an import declaration from its remaining bindings
// ("" when none remain).
func importText(from string, bindings []ogapi.ImportBinding) string {
	if len(bindings) == 0 {
		return ""
	}
	var def, ns string
	var named []string
	for _, bd := range bindings {
		switch bd.Imported {
		case "default":
			def = bd.Local
		case "*":
			ns = "* as " + bd.Local
		default:
			if bd.Imported == bd.Local {
				named = append(named, bd.Local)
			} else {
				named = append(named, bd.Imported+" as "+bd.Local)
			}
		}
	}
	var parts []string
	if def != "" {
		parts = append(parts, def)
	}
	if ns != "" {
		parts = append(parts, ns)
	}
	if len(named) > 0 {
		parts = append(parts, "{ "+strings.Join(named, ", ")+" }")
	}
	return "import " + strings.Join(parts, ", ") + " from " + jsonStr(from) + ";"
}
