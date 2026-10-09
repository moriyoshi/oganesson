package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"

	"github.com/microsoft/TypeScript/tsc/ogapi"
)

func runCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	_ = fs.Parse(args)
	dir, err := filepath.Abs(projectDir(fs))
	if err != nil {
		return err
	}
	if err := writeTypes(dir); err != nil {
		return err
	}
	diags, err := ogapi.Check(ctx, filepath.Join(dir, "tsconfig.json"), dir)
	if err != nil {
		return err
	}
	errors := 0
	for _, d := range diags {
		// TS 7 removed baseUrl, but Next projects commonly still set it;
		// Oganesson keeps honoring it, so this is only a warning.
		if d.Code == 5102 {
			d.Category = "warning"
		}
		if d.Category == "error" {
			errors++
		}
		rel, _ := filepath.Rel(dir, d.File)
		fmt.Printf("%s:%d:%d: %s TS%d: %s\n", rel, d.Line, d.Column, d.Category, d.Code, d.Message)
	}
	if errors > 0 {
		return fmt.Errorf("%d type error(s)", errors)
	}
	return nil
}
