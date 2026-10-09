// Package ogapi is Oganesson's facade over the TypeScript-Go compiler.
//
// TypeScript-Go keeps all of its packages under internal/, so this package is
// copied into the vendored module at tsc/ogapi (see scripts/sync-typescript.sh),
// where Go's internal-package rule lets it import them. Only plain Go types
// cross this boundary.
package ogapi

import (
	"context"
	"sort"
	"strings"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/bundled"
	"github.com/microsoft/TypeScript/tsc/internal/compiler"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/diagnosticwriter"
	"github.com/microsoft/TypeScript/tsc/internal/locale"
	"github.com/microsoft/TypeScript/tsc/internal/tsoptions"
	"github.com/microsoft/TypeScript/tsc/internal/tspath"
	"github.com/microsoft/TypeScript/tsc/internal/vfs"
	"github.com/microsoft/TypeScript/tsc/internal/vfs/osvfs"
)

// Diagnostic is a compiler message with a 1-based source position.
type Diagnostic struct {
	File     string
	Line     int
	Column   int
	Code     int
	Category string
	Message  string
}

// Config is the subset of a resolved tsconfig that drives bundling.
type Config struct {
	ConfigFile              string
	FileNames               []string
	BaseURL                 string
	Paths                   map[string][]string
	PathsBase               string
	JSX                     string // "preserve", "react", "react-jsx", "react-jsxdev", "react-native"
	JSXImportSource         string
	Target                  string
	ExperimentalDecorators  bool
	EmitDecoratorMetadata   bool
	UseDefineForClassFields bool
	VerbatimModuleSyntax    bool
}

type host struct {
	fs  vfs.FS
	cwd string
}

func (h *host) FS() vfs.FS                  { return h.fs }
func (h *host) GetCurrentDirectory() string { return h.cwd }

func newHost(cwd string) *host {
	return &host{fs: bundled.WrapFS(osvfs.FS()), cwd: tspath.NormalizePath(cwd)}
}

func parse(configFile, cwd string) (*tsoptions.ParsedCommandLine, *host, []Diagnostic) {
	h := newHost(cwd)
	parsed, errs := tsoptions.GetParsedCommandLineOfConfigFile(configFile, &core.CompilerOptions{}, nil, h, nil)
	if len(errs) > 0 {
		return nil, h, convert(errs)
	}
	if errs := parsed.Errors; len(errs) > 0 {
		return parsed, h, convert(errs)
	}
	return parsed, h, nil
}

// LoadConfig resolves a tsconfig.json, following "extends" chains, exactly as
// tsc would.
func LoadConfig(configFile, cwd string) (*Config, []Diagnostic) {
	parsed, _, diags := parse(configFile, cwd)
	if parsed == nil {
		return nil, diags
	}
	o := parsed.CompilerOptions()
	cfg := &Config{
		ConfigFile:              o.ConfigFilePath,
		FileNames:               parsed.FileNames(),
		BaseURL:                 o.BaseUrl,
		PathsBase:               o.PathsBasePath,
		JSX:                     jsxName(o.Jsx),
		JSXImportSource:         o.JsxImportSource,
		Target:                  targetName(o.GetEmitScriptTarget()),
		ExperimentalDecorators:  o.ExperimentalDecorators == core.TSTrue,
		EmitDecoratorMetadata:   o.EmitDecoratorMetadata == core.TSTrue,
		UseDefineForClassFields: o.GetUseDefineForClassFields(),
		VerbatimModuleSyntax:    o.VerbatimModuleSyntax == core.TSTrue,
	}
	if o.Paths != nil {
		cfg.Paths = map[string][]string{}
		for k, v := range o.Paths.Entries() {
			cfg.Paths[k] = v
		}
	}
	return cfg, diags
}

// Check type-checks the project described by configFile and returns every
// diagnostic tsc --noEmit would report.
func Check(ctx context.Context, configFile, cwd string) ([]Diagnostic, error) {
	parsed, h, diags := parse(configFile, cwd)
	if parsed == nil {
		return diags, nil
	}
	compilerHost := compiler.NewCachedFSCompilerHost(h.cwd, h.fs, bundled.LibPath(), nil, nil, nil)
	program := compiler.NewProgram(compiler.ProgramOptions{
		ProgramConfig: compiler.ProgramConfig{Config: parsed},
		ProgramHosts:  compiler.ProgramHosts{Host: compilerHost},
	})
	all := append([]*ast.Diagnostic{}, program.GetConfigFileParsingDiagnostics()...)
	all = append(all, program.GetProgramDiagnostics()...)
	all = append(all, program.GetGlobalDiagnostics(ctx)...)
	for _, f := range program.GetSourceFiles() {
		all = append(all, program.GetSyntacticDiagnostics(ctx, f)...)
		all = append(all, program.GetSemanticDiagnostics(ctx, f)...)
	}
	return append(diags, convert(all)...), nil
}

func convert(in []*ast.Diagnostic) []Diagnostic {
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		var msg strings.Builder
		diagnosticwriter.WriteFlattenedASTDiagnosticMessage(&msg, d, "\n", locale.Default)
		item := Diagnostic{
			Code:     int(d.Code()),
			Category: strings.ToLower(d.Category().Name()),
			Message:  msg.String(),
		}
		if f := d.File(); f != nil {
			item.File = f.FileName()
			item.Line, item.Column = lineCol(f.Text(), d.Pos())
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

func lineCol(text string, pos int) (int, int) {
	if pos > len(text) {
		pos = len(text)
	}
	line := 1 + strings.Count(text[:pos], "\n")
	col := pos - strings.LastIndexByte(text[:pos], '\n')
	return line, col
}

func jsxName(j core.JsxEmit) string {
	switch j {
	case core.JsxEmitPreserve:
		return "preserve"
	case core.JsxEmitReact:
		return "react"
	case core.JsxEmitReactJSX:
		return "react-jsx"
	case core.JsxEmitReactJSXDev:
		return "react-jsxdev"
	case core.JsxEmitReactNative:
		return "react-native"
	}
	return ""
}

func targetName(t core.ScriptTarget) string {
	switch t {
	case core.ScriptTargetES5:
		return "es5"
	case core.ScriptTargetESNext:
		return "esnext"
	}
	if t >= core.ScriptTargetES2015 && t <= core.ScriptTargetES2026 {
		return "es" + itoa(2015+int(t-core.ScriptTargetES2015))
	}
	return ""
}

func itoa(n int) string {
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
