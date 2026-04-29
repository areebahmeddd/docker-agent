package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dgageot/rubocop-go/cop"
)

// HookBuiltinsRegistered enforces that every builtin-name constant declared
// under pkg/hooks/builtins/ is wired into the package's Register() function
// in pkg/hooks/builtins/builtins.go.
//
// Each in-process builtin lives in its own file with a name constant and an
// implementation:
//
//	pkg/hooks/builtins/add_date.go     : const AddDate = "add_date"
//	                                     func addDate(...) { ... }
//
// The Register() function in builtins.go wires the constants to the
// implementations:
//
//	r.RegisterBuiltin(AddDate, addDate),
//
// A new builtin file ships its own constant + impl, but the wiring is in a
// different file. Forgetting that step compiles cleanly: the constant is
// just an unused identifier (no error because it is exported), the impl
// is dead code (also no error in another package), and the only signal
// that something is wrong is a runtime "unknown builtin" failure when an
// agent YAML references the new name.
//
// The cop runs on pkg/hooks/builtins/builtins.go (where Register() lives),
// scans every *.go file in the same directory for `const Name = "wire"`
// declarations whose value is a string literal, and reports any whose
// identifier never appears as the first argument of a RegisterBuiltin
// call in the inspected file.
//
// Files named builtins.go itself, *_test.go, and testhelpers_test.go are
// excluded from the constant scan because they are not where new
// builtins land.
type HookBuiltinsRegistered struct {
	cop.Meta
}

// NewHookBuiltinsRegistered returns a fully configured
// HookBuiltinsRegistered cop.
func NewHookBuiltinsRegistered() *HookBuiltinsRegistered {
	return &HookBuiltinsRegistered{Meta: cop.Meta{
		CopName:     "Lint/HookBuiltinsRegistered",
		CopDesc:     "every builtin name constant under pkg/hooks/builtins/ must appear in a RegisterBuiltin call",
		CopSeverity: cop.Error,
	}}
}

func (c *HookBuiltinsRegistered) Check(p *cop.Pass) {
	if !isHookBuiltinsRegisterFile(p.Filename()) {
		return
	}

	declared, err := builtinNameConstants(filepath.Dir(p.Filename()))
	if err != nil || len(declared) == 0 {
		return
	}

	registered, anchor := registerBuiltinIdents(p)
	if anchor == nil {
		return
	}

	var missing []string
	for _, name := range declared {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	slices.Sort(missing)
	p.Report(anchor,
		"pkg/hooks/builtins/builtins.go is missing RegisterBuiltin call(s) for: %s",
		strings.Join(missing, ", "))
}

// isHookBuiltinsRegisterFile reports whether filename is the canonical
// pkg/hooks/builtins/builtins.go that owns the Register() entry point.
func isHookBuiltinsRegisterFile(filename string) bool {
	slash := filepath.ToSlash(filename)
	return strings.HasSuffix(slash, "/pkg/hooks/builtins/builtins.go") ||
		slash == "pkg/hooks/builtins/builtins.go"
}

// builtinNameConstants returns the identifier of every `const Name = "..."`
// declaration in dir whose value is a string literal — but only from
// per-builtin files. builtins.go itself and any _test.go files are
// excluded so the cop doesn't flag them or pick up unrelated test
// constants.
func builtinNameConstants(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		if !strings.HasSuffix(fname, ".go") || strings.HasSuffix(fname, "_test.go") {
			continue
		}
		if fname == "builtins.go" {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, fname), nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					if !ast.IsExported(n.Name) {
						continue
					}
					names = append(names, n.Name)
				}
			}
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	return names, nil
}

// registerBuiltinIdents collects the set of identifiers that appear as the
// first positional argument of a `RegisterBuiltin(<name>, …)` call anywhere
// in the inspected file. It also returns the call expression that the cop
// uses to anchor diagnostics; nil means no Register() body was found and
// the cop should bail out.
func registerBuiltinIdents(p *cop.Pass) (map[string]bool, ast.Node) {
	registered := map[string]bool{}
	var anchor ast.Node
	p.ForEachCall(func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RegisterBuiltin" || len(call.Args) == 0 {
			return
		}
		if anchor == nil {
			anchor = call
		}
		id, ok := call.Args[0].(*ast.Ident)
		if !ok {
			return
		}
		registered[id.Name] = true
	})
	if anchor == nil {
		// Fall back to the file's package clause so the diagnostic still
		// surfaces if Register() was reshaped beyond recognition.
		anchor = p.File.Name
	}
	return registered, anchor
}
