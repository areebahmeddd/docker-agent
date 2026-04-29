package main

import (
	"go/ast"
	"go/token"
	"strconv"

	"github.com/dgageot/rubocop-go/cop"
)

// ConfigVersionConstant enforces that a file under pkg/config/vN/ declaring a
// `const Version = "<value>"` uses "<N>" as its value.
//
// This guards against the common mistake of bumping the directory name
// without bumping the constant (or vice versa) when freezing the
// work-in-progress config and creating a new "latest". A mismatch would
// silently break the parser dispatch in pkg/config/versions.go, which
// registers parsers keyed by `Version`.
//
// Files under pkg/config/latest/ are intentionally exempt: their `Version`
// is the next, work-in-progress value (one greater than the highest vN).
type ConfigVersionConstant struct {
	cop.Meta
}

// NewConfigVersionConstant returns a fully configured ConfigVersionConstant cop.
func NewConfigVersionConstant() *ConfigVersionConstant {
	return &ConfigVersionConstant{Meta: cop.Meta{
		CopName:     "Lint/ConfigVersionConstant",
		CopDesc:     "Version constant in pkg/config/vN/ must equal \"N\"",
		CopSeverity: cop.Error,
	}}
}

func (c *ConfigVersionConstant) Check(p *cop.Pass) {
	dirVersion, ok := versionFromDir(configDir(p.Filename()))
	if !ok {
		return
	}
	expected := strconv.Itoa(dirVersion)

	for _, lit := range versionStringLiterals(p) {
		got, err := strconv.Unquote(lit.Value)
		if err != nil || got == expected {
			continue
		}
		p.Report(lit, "Version in pkg/config/v%s/ must be %q, got %q", expected, expected, got)
	}
}

// versionStringLiterals returns the value literal of every top-level
// `const Version = "<string>"` declaration in the file under inspection.
func versionStringLiterals(p *cop.Pass) []*ast.BasicLit {
	var lits []*ast.BasicLit
	p.ForEachConst(func(gen *ast.GenDecl) {
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != "Version" || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				lits = append(lits, lit)
			}
		}
	})
	return lits
}
