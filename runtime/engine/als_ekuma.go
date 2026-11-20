//go:build ekuma

package engine

import "github.com/moriyoshi/oganesson/internal/jsvm"

type asyncContext struct{ vm *jsvm.Runtime }

func newAsyncContext(vm *jsvm.Runtime) *asyncContext { return &asyncContext{vm: vm} }

func (c *asyncContext) capture() *frame {
	f, _ := c.vm.ContinuationData().(*frame)
	return f
}

func (c *asyncContext) enter(*frame) {
	panic(c.vm.NewTypeError("persistent AsyncLocalStorage scope changes are unavailable on Ekuma"))
}

func (c *asyncContext) runIn(f *frame, fn func() error) (err error) {
	c.vm.WithContinuationData(f, func() { err = fn() })
	return err
}

func stackOverflow(error) error { return nil }
