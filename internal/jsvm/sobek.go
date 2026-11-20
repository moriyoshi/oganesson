//go:build !ekuma

// Package jsvm supplies the JavaScript host boundary used by og.
package jsvm

import (
	"context"
	"errors"

	"github.com/grafana/sobek"
	"github.com/grafana/sobek/parser"
)

const UsesEkuma = false

type (
	Runtime                   = sobek.Runtime
	Value                     = sobek.Value
	Object                    = sobek.Object
	Program                   = sobek.Program
	FunctionCall              = sobek.FunctionCall
	ConstructorCall           = sobek.ConstructorCall
	Callable                  = sobek.Callable
	ArrayBuffer               = sobek.ArrayBuffer
	Promise                   = sobek.Promise
	PromiseRejectionOperation = sobek.PromiseRejectionOperation
	Exception                 = sobek.Exception
	StackOverflowError        = sobek.StackOverflowError
	InterruptedError          = sobek.InterruptedError
	CompilerSyntaxError       = sobek.CompilerSyntaxError
	StackFrame                = sobek.StackFrame
	Symbol                    = sobek.Symbol
)

const (
	FLAG_FALSE             = sobek.FLAG_FALSE
	FLAG_TRUE              = sobek.FLAG_TRUE
	PromiseStatePending    = sobek.PromiseStatePending
	PromiseStateRejected   = sobek.PromiseStateRejected
	PromiseRejectionReject = sobek.PromiseRejectionReject
	PromiseRejectionHandle = sobek.PromiseRejectionHandle
)

var (
	New                  = sobek.New
	Compile              = sobek.Compile
	MustCompile          = sobek.MustCompile
	AssertFunction       = sobek.AssertFunction
	AssertConstructor    = sobek.AssertConstructor
	Null                 = sobek.Null
	Undefined            = sobek.Undefined
	IsNull               = sobek.IsNull
	IsUndefined          = sobek.IsUndefined
	NewSymbol            = sobek.NewSymbol
	UncapFieldNameMapper = sobek.UncapFieldNameMapper
)

func CompileCommonJS(name, source string, loader func(string) ([]byte, error)) (*Program, error) {
	parsed, err := sobek.Parse(name, source, parser.WithSourceMapLoader(loader))
	if err != nil {
		return nil, err
	}
	return sobek.CompileAST(parsed, false)
}

func WithContext(vm *Runtime, ctx context.Context, reason error, f func() error) error {
	stop := context.AfterFunc(ctx, func() { vm.Interrupt(reason) })
	defer stop()
	return f()
}

func AsPromise(v Value) (*Promise, bool) {
	if v == nil {
		return nil, false
	}
	p, ok := v.Export().(*Promise)
	return p, ok
}

func SameObject(a, b *Object) bool { return a == b }

func WrapArrayBuffer(vm *Runtime, b []byte) ArrayBuffer {
	return vm.NewArrayBuffer(b)
}

func IsInterrupted(err error) bool {
	var ie *InterruptedError
	return errors.As(err, &ie)
}
