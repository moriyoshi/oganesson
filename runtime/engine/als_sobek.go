//go:build !ekuma

package engine

import (
	"errors"
	"fmt"
	"strings"

	"github.com/moriyoshi/oganesson/internal/jsvm"
)

type asyncContext struct{ current *frame }

// Bound recursion to a similar call depth as V8.
const maxCallStackSize = 15000

func newAsyncContext(vm *jsvm.Runtime) *asyncContext {
	vm.SetMaxCallStackSize(maxCallStackSize)
	vm.SetFieldNameMapper(jsvm.UncapFieldNameMapper())
	c := &asyncContext{}
	vm.SetAsyncContextTracker(c)
	return c
}

func (c *asyncContext) Grab() any        { return c.current }
func (c *asyncContext) Resumed(data any) { c.current, _ = data.(*frame) }
func (c *asyncContext) Exited()          { c.current = nil }

func (c *asyncContext) capture() *frame { return c.current }
func (c *asyncContext) enter(f *frame)  { c.current = f }
func (c *asyncContext) runIn(f *frame, fn func() error) error {
	prev := c.current
	c.current = f
	defer func() { c.current = prev }()
	return fn()
}

// stackOverflow returns Node's message for Sobek's stack overflow.
func stackOverflow(err error) error {
	var so *jsvm.StackOverflowError
	if errors.As(err, &so) {
		// The innermost frames show the recursion.
		lines := strings.Split(so.String(), "\n")
		if len(lines) > 40 {
			lines = append(lines[:40], fmt.Sprintf("\t... %d more frames", len(lines)-40))
		}
		return fmt.Errorf("RangeError: Maximum call stack size exceeded\n%s", strings.Join(lines, "\n"))
	}
	return nil
}
