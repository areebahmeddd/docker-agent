package main

import (
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
type ConfigPackageName struct {
	cop.Meta
}

// NewConfigPackageName returns a fully configured ConfigPackageName cop.
func NewConfigPackageName() *ConfigPackageName {
	return &ConfigPackageName{Meta: cop.Meta{
		CopName:     "Lint/ConfigPackageName",
		CopDesc:     "Files under pkg/config/<dir>/ must declare package <dir>",
		CopSeverity: cop.Error,
	}}
}

func (c *ConfigPackageName) Check(p *cop.Pass) {
	dir, ok := p.PathSegment("pkg/config")
	if !ok {
		return
	}

	got := p.PackageName()
	switch got {
	case dir:
		return
	case dir + "_test":
		// Black-box test packages are a legitimate Go convention.
		if p.IsTestFile() {
			return
		}
	}

	p.Report(p.File.Name, "file in pkg/config/%s/ must declare package %s, got %s", dir, dir, got)
}
