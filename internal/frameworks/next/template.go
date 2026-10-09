package next

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Template expansion replaces Next's native expandNextJsTemplate (SWC):
//
//	'VAR_NAME'                 -> JSON string of the replacement
//	// INJECT:name             -> const name = <code>;
//	// INJECT_RAW:name         -> <code>
//	// OPTIONAL_IMPORT:name    -> import name from "<path>" | const name = null;
//	// OPTIONAL_IMPORT:* as name -> import * as name from "<path>" | const name = null;

// TemplateArgs are the substitutions for one template instance.
type TemplateArgs struct {
	Replacements map[string]string  // VAR_* -> string value
	Injections   map[string]string  // INJECT name -> JS expression
	Raw          map[string]string  // INJECT_RAW name -> JS code
	Imports      map[string]*string // OPTIONAL_IMPORT name -> module path (nil = null)
}

var (
	varRe       = regexp.MustCompile(`(['"])(VAR_[A-Z_]+)(['"])`)
	directiveRe = regexp.MustCompile(`(?m)^([ \t]*)// (INJECT|INJECT_RAW|OPTIONAL_IMPORT):(\* as )?([A-Za-z_$][\w$]*)[ \t]*$`)
)

// ExpandTemplate reads next/dist/esm/build/templates/<name>.js and applies args.
func ExpandTemplate(nextDir, name string, args TemplateArgs) (string, error) {
	path := filepath.Join(nextDir, "dist", "esm", "build", "templates", name+".js")
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	out, err := expand(string(src), args)
	if err != nil {
		return "", fmt.Errorf("next: template %s: %w", name, err)
	}
	return out, nil
}

func expand(src string, args TemplateArgs) (string, error) {
	var missing []string
	used := map[string]bool{}
	src = varRe.ReplaceAllStringFunc(src, func(m string) string {
		key := varRe.FindStringSubmatch(m)[2]
		v, ok := args.Replacements[key]
		if !ok {
			missing = append(missing, key)
			return m
		}
		used[key] = true
		return jsonStr(v)
	})
	src = directiveRe.ReplaceAllStringFunc(src, func(m string) string {
		g := directiveRe.FindStringSubmatch(m)
		indent, kind, star, name := g[1], g[2], g[3], g[4]
		switch kind {
		case "INJECT":
			code, ok := args.Injections[name]
			if !ok {
				missing = append(missing, "INJECT:"+name)
				return m
			}
			used["INJECT:"+name] = true
			return fmt.Sprintf("%sconst %s = %s;", indent, name, code)
		case "INJECT_RAW":
			used["INJECT_RAW:"+name] = true
			return indent + args.Raw[name] // absent means empty
		default: // OPTIONAL_IMPORT
			if p := args.Imports[name]; p != nil {
				return fmt.Sprintf("%simport %s%s from %s;", indent, star, name, jsonStr(*p))
			}
			return fmt.Sprintf("%sconst %s = null;", indent, name)
		}
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("unfilled placeholders: %s", strings.Join(missing, ", "))
	}
	for k := range args.Replacements {
		if !used[k] {
			return "", fmt.Errorf("replacement %s not used by the template", k)
		}
	}
	return src, nil
}
