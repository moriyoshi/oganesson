package ogapi

import (
	"context"
	"sync"

	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/transpile"
)

// optionsCache holds each tsconfig's resolved compiler options.
var optionsCache sync.Map // configFile -> *core.CompilerOptions

// TranspileDecorators transpiles one TypeScript file as tsc's
// transpileModule does with the project's compiler options, for its legacy
// decorators (experimentalDecorators) and their design:* metadata
// (emitDecoratorMetadata, which esbuild does not emit): decorators become
// __decorate / __metadata calls, types are erased. What esbuild does next
// is left to it: the target is ESNext (useDefineForClassFields stays as
// the project's target makes it), modules stay ES modules, JSX is
// preserved. It returns the JavaScript and any diagnostics.
func TranspileDecorators(ctx context.Context, configFile, cwd, fileName, text string) (string, []Diagnostic) {
	var base *core.CompilerOptions
	if v, ok := optionsCache.Load(configFile); ok {
		base = v.(*core.CompilerOptions)
	} else {
		parsed, _, diags := parse(configFile, cwd)
		if parsed == nil {
			return "", diags
		}
		base = parsed.CompilerOptions()
		optionsCache.Store(configFile, base)
	}
	opts := base.Clone()
	if opts.GetUseDefineForClassFields() {
		opts.UseDefineForClassFields = core.TSTrue
	} else {
		opts.UseDefineForClassFields = core.TSFalse
	}
	opts.Target = core.ScriptTargetESNext
	opts.Module = core.ModuleKindESNext
	opts.Jsx = core.JsxEmitPreserve
	opts.ExperimentalDecorators = core.TSTrue
	opts.EmitDecoratorMetadata = core.TSTrue
	opts.SourceMap = core.TSUnknown
	opts.InlineSourceMap = core.TSUnknown
	out := transpile.TranspileModule(ctx, text, transpile.Options{CompilerOptions: opts, FileName: fileName, ReportDiagnostics: true})
	return out.OutputText, convert(out.Diagnostics)
}
