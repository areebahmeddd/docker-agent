package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/dgageot/rubocop-go/cop"
)

// ConfigVersionImport enforces that config version packages (pkg/config/vN)
// only import their immediate predecessor (pkg/config/v{N-1}) and the shared
// types package (pkg/config/types). This preserves the strict migration chain:
// v0 → v1 → v2 → … → latest.
type ConfigVersionImport struct{}

func (*ConfigVersionImport) Name() string { return "Lint/ConfigVersionImport" }
func (*ConfigVersionImport) Description() string {
	return "Config version packages must only import their immediate predecessor"
}
func (*ConfigVersionImport) Severity() cop.Severity { return cop.Error }

// configVersionRe matches "pkg/config/vN" at the end of an import path.
var configVersionRe = regexp.MustCompile(`pkg/config/v(\d+)$`)

// Check inspects import declarations in config version packages.
func (c *ConfigVersionImport) Check(fset *token.FileSet, file *ast.File) []cop.Offense {
	if len(file.Imports) == 0 {
		return nil
	}

	// Determine which config version package this file belongs to.
	filename := fset.Position(file.Package).Filename
	dirVersion, isVersioned := extractDirVersion(filename)
	dirIsLatest := isLatestDir(filename)

	if !isVersioned && !dirIsLatest {
		return nil
	}

	// Black-box test files (package <dir>_test) live alongside the package
	// but are external to it; they're allowed to import what they please.
	if strings.HasSuffix(file.Name.Name, "_test") {
		return nil
	}

	var offenses []cop.Offense

	for _, imp := range file.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)

		if !strings.Contains(importPath, "pkg/config/") {
			continue
		}

		if strings.HasSuffix(importPath, "pkg/config/types") {
			continue
		}

		if isVersioned {
			offenses = append(offenses, c.checkVersionedImport(fset, imp, importPath, dirVersion)...)
		} else if dirIsLatest {
			offenses = append(offenses, c.checkLatestImport(fset, imp, importPath)...)
		}
	}

	return offenses
}

func (c *ConfigVersionImport) checkVersionedImport(fset *token.FileSet, imp *ast.ImportSpec, importPath string, dirVersion int) []cop.Offense {
	if strings.HasSuffix(importPath, "pkg/config/latest") {
		return []cop.Offense{cop.NewOffense(c, fset, imp.Path.Pos(), imp.Path.End(),
			fmt.Sprintf("config v%d must not import pkg/config/latest", dirVersion))}
	}

	m := configVersionRe.FindStringSubmatch(importPath)
	if m == nil {
		return nil
	}

	importedVersion, _ := strconv.Atoi(m[1])
	expected := dirVersion - 1

	if expected < 0 {
		return []cop.Offense{cop.NewOffense(c, fset, imp.Path.Pos(), imp.Path.End(),
			"config v0 must not import other config version packages")}
	}

	if importedVersion != expected {
		return []cop.Offense{cop.NewOffense(c, fset, imp.Path.Pos(), imp.Path.End(),
			fmt.Sprintf("config v%d must import v%d (its predecessor), not v%d", dirVersion, expected, importedVersion))}
	}

	return nil
}

func (c *ConfigVersionImport) checkLatestImport(fset *token.FileSet, imp *ast.ImportSpec, importPath string) []cop.Offense {
	if configVersionRe.MatchString(importPath) {
		return nil
	}

	return []cop.Offense{cop.NewOffense(c, fset, imp.Path.Pos(), imp.Path.End(),
		"pkg/config/latest should only import config version or types packages, not "+importPath)}
}

func extractDirVersion(filename string) (int, bool) {
	normalized := filepath.ToSlash(filename)

	re := regexp.MustCompile(`(?:^|/)pkg/config/v(\d+)/`)
	m := re.FindStringSubmatch(normalized)
	if m == nil {
		return 0, false
	}

	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return v, true
}

func isLatestDir(filename string) bool {
	normalized := filepath.ToSlash(filename)
	return strings.Contains(normalized, "pkg/config/latest/")
}
