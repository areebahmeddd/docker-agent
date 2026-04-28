package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dgageot/rubocop-go/cop"
)

// ConfigPackageName enforces that files under pkg/config/<dir>/ declare a
// package name that matches their directory:
//
//   - pkg/config/vN/      → package vN
//   - pkg/config/latest/  → package latest
//   - pkg/config/types/   → package types
//
// This catches a class of copy-paste bugs that occur when a "latest" version
// is frozen into a numbered vN directory: the package clause is easy to
// forget, and the broken state remains compilable as long as importers use
// an explicit alias.
type ConfigPackageName struct{}

func (*ConfigPackageName) Name() string { return "Lint/ConfigPackageName" }
func (*ConfigPackageName) Description() string {
	return "Files under pkg/config/<dir>/ must declare package <dir>"
}
func (*ConfigPackageName) Severity() cop.Severity { return cop.Error }

// configDirRe matches files under pkg/config/<dir>/. The captured group is
// the directory name immediately under pkg/config/. It accepts both
// absolute and relative paths.
var configDirRe = regexp.MustCompile(`(?:^|/)pkg/config/([^/]+)/[^/]+\.go$`)

func (c *ConfigPackageName) Check(fset *token.FileSet, file *ast.File) []cop.Offense {
	filename := fset.Position(file.Package).Filename
	normalized := filepath.ToSlash(filename)

	m := configDirRe.FindStringSubmatch(normalized)
	if m == nil {
		return nil
	}

	dir := m[1]

	// pkg/config/<dir> contains the parsers/dispatcher; skip files that live
	// directly in pkg/config/.
	if strings.HasSuffix(dir, ".go") {
		return nil
	}

	expected := dir
	got := file.Name.Name
	if got == expected {
		return nil
	}
	// Black-box test packages (<dir>_test) are a legitimate Go convention.
	if strings.HasSuffix(filename, "_test.go") && got == expected+"_test" {
		return nil
	}

	return []cop.Offense{cop.NewOffense(c, fset, file.Name.Pos(), file.Name.End(),
		fmt.Sprintf("file in pkg/config/%s/ must declare package %s, got %s", dir, expected, got))}
}
