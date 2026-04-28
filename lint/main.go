// Package main runs project-specific linting cops using rubocop-go.
//
// Usage: go run ./lint ./...
package main

import (
	"fmt"
	"os"

	"github.com/dgageot/rubocop-go/config"
	"github.com/dgageot/rubocop-go/cop"
	"github.com/dgageot/rubocop-go/runner"
)

func main() {
	cop.Register(&ConfigVersionImport{})
	cop.Register(&ConfigPackageName{})
	cop.Register(&ConfigVersionConstant{})
	cop.Register(&LatestImportsPredecessor{})
	cops := cop.All()
	fmt.Printf("Inspecting Go files with %d cop(s)\n", len(cops))

	cfg := config.DefaultConfig()
	r := runner.New(cops, cfg, os.Stdout)

	paths := os.Args[1:]
	if len(paths) == 0 {
		paths = []string{"."}
	}

	offenseCount, err := r.Run(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if offenseCount > 0 {
		os.Exit(1)
	}
}
