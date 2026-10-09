package next

import (
	"strings"

	"github.com/microsoft/TypeScript/tsc/ogapi"
)

// typeofWindow replaces `typeof window` in a project module with its value
// for the build's target, as Next's SWC optimizer does
// (optimizer.globals.typeofs: "object" in browser builds, "undefined" in
// server ones). esbuild then leaves the other branch's imports out:
// with-msw imports msw/node only under typeof window === "undefined", which
// the browser build could not resolve. Modules from node_modules, and
// modules that bind the name window themselves, are left alone.
func typeofWindow(path, code, value string) string {
	if strings.Contains(path, "/node_modules/") || !strings.Contains(code, "typeof") || !strings.Contains(code, "window") {
		return code
	}
	spans, bound := ogapi.TypeofGlobal(path, code, "window")
	if bound || len(spans) == 0 {
		return code
	}
	var sb strings.Builder
	last := 0
	for _, s := range spans {
		sb.WriteString(code[last:s.Start])
		sb.WriteString(`"` + value + `"`)
		last = s.End
	}
	sb.WriteString(code[last:])
	return sb.String()
}
