package main

import (
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
	dir, _ := p.PathSegment("pkg/config")
	dirVersion, ok := versionFromDir(dir)
	if !ok {
		return
	}
	expected := strconv.Itoa(dirVersion)

	lit, ok := p.StringConstNodes()["Version"]
	if !ok {
		return
	}
	got, err := strconv.Unquote(lit.Value)
	if err != nil || got == expected {
		return
	}
	p.Report(lit, "Version in pkg/config/v%s/ must be %q, got %q", expected, expected, got)
}
