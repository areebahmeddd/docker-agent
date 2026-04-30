package main

import (
	"go/ast"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dgageot/rubocop-go/cop"
)

// ConfigVersionsRegistered enforces that pkg/config/versions.go registers
// every config-version package that exists on disk.
//
// pkg/config/versions.go is the dispatch table for parsing and upgrading: it
// builds the parser map by calling `Register` on each of v0…vN and `latest`.
// When a new version is frozen (or an old one removed) the file must be
// updated in lock-step, otherwise:
//
//   - a newly added vN/ has no parser and produces "unsupported config
//     version" at runtime, which is invisible at compile time because
//     versions.go does not import the new package, and
//   - a removed vN/ would leave behind a dangling Register call and fail
//     the build — already caught by the compiler — so this cop focuses on
//     the silent-failure direction.
//
// The cop only inspects pkg/config/versions.go and reports any package that
// exists under pkg/config/ but is not registered.
type ConfigVersionsRegistered struct {
	cop.Meta
}

// NewConfigVersionsRegistered returns a fully configured
// ConfigVersionsRegistered cop.
func NewConfigVersionsRegistered() *ConfigVersionsRegistered {
	return &ConfigVersionsRegistered{Meta: cop.Meta{
		CopName:     "Lint/ConfigVersionsRegistered",
		CopDesc:     "pkg/config/versions.go must register every pkg/config/vN and pkg/config/latest package",
		CopSeverity: cop.Error,
	}}
}

func (c *ConfigVersionsRegistered) Check(p *cop.Pass) {
	if !p.FileMatches("pkg/config/versions.go") {
		return
	}

	want, err := versionPackagesOnDisk(filepath.Dir(p.Filename()))
	if err != nil || len(want) == 0 {
		return
	}
	got := p.SelectorReceivers("Register")

	var missing []string
	for _, name := range want {
		if !got[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	slices.Sort(missing)

	// Anchor the diagnostic on the function declaration so the message points
	// at the registry rather than at the package clause.
	p.Report(registryAnchor(p),
		"pkg/config/versions.go is missing Register call(s) for: %s", strings.Join(missing, ", "))
}

// versionPackagesOnDisk lists the package directories under pkg/config/ that
// the dispatch table is expected to register: every vN/ and latest/.
func versionPackagesOnDisk(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "latest" {
			names = append(names, name)
			continue
		}
		if _, ok := versionFromDir(name); ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names, nil
}

// registryAnchor picks the AST node used to position the offense. Preferring
// the `versions` function declaration keeps the diagnostic close to the
// dispatch table; if that function is absent (unexpected), the file's
// package clause is used as a fallback.
func registryAnchor(p *cop.Pass) ast.Node {
	var anchor ast.Node = p.File.Name
	p.ForEachFunc(func(fn *ast.FuncDecl) {
		if fn.Name.Name == "versions" {
			anchor = fn.Name
		}
	})
	return anchor
}
