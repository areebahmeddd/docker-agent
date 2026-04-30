package main

import (
	"go/ast"
	"reflect"
	"strings"

	"github.com/dgageot/rubocop-go/cop"
)

// ConfigLatestTagConsistency enforces that struct fields under
// pkg/config/latest/ keep their `json` and `yaml` struct tags in sync with
// respect to the omit-when-empty modifier:
//
//   - both tags carry `omitempty` (or `omitzero` on the json side), or
//   - neither tag carries an omit modifier.
//
// Mismatched modifiers silently produce different serialisations depending
// on the format: a value that round-trips through YAML may be lost when the
// same struct is encoded as JSON (or vice-versa). Because pkg/config/latest
// is the work-in-progress schema, such drift is almost always a typo, not
// an intentional design choice.
//
// Frozen versioned packages (pkg/config/vN/) are intentionally exempt: they
// must reproduce the exact wire format that shipped, including any historical
// quirks.
//
// Recognised modifiers:
//   - json: `omitempty`, `omitzero` (the latter introduced in Go 1.24)
//   - yaml: `omitempty`
//
// Fields without a `yaml` tag are skipped — yaml.v3 derives a default name
// from the field, which is a separate concern handled elsewhere.
type ConfigLatestTagConsistency struct {
	cop.Meta
}

// NewConfigLatestTagConsistency returns a fully configured
// ConfigLatestTagConsistency cop.
func NewConfigLatestTagConsistency() *ConfigLatestTagConsistency {
	return &ConfigLatestTagConsistency{Meta: cop.Meta{
		CopName:     "Lint/ConfigLatestTagConsistency",
		CopDesc:     "json and yaml tags in pkg/config/latest must agree on omitempty/omitzero",
		CopSeverity: cop.Error,
	}}
}

func (c *ConfigLatestTagConsistency) Check(p *cop.Pass) {
	dir, _ := p.PathSegment("pkg/config")
	if dir != "latest" {
		return
	}
	// Black-box test files don't ship struct definitions for the wire format.
	if p.IsBlackBoxTest() {
		return
	}

	p.ForEachStructField(func(_ *ast.TypeSpec, field *ast.Field, tag reflect.StructTag) {
		if field.Tag == nil {
			return
		}
		jsonTag, hasJSON := tag.Lookup("json")
		yamlTag, hasYAML := tag.Lookup("yaml")
		if !hasJSON || !hasYAML {
			return
		}
		// Embedded fields use ",inline" on both sides; nothing to compare.
		if isInline(jsonTag) || isInline(yamlTag) {
			return
		}

		jsonOmit, jsonMod := jsonOmitModifier(jsonTag)
		yamlOmit := hasOption(yamlTag, "omitempty")

		if jsonOmit != yamlOmit {
			p.Report(field.Tag,
				"json and yaml tags disagree on omit-when-empty: json has %q, yaml has %q (field %s)",
				modifierLabel(jsonOmit, jsonMod), modifierLabel(yamlOmit, "omitempty"),
				fieldNames(field))
		}
	})
}

// jsonOmitModifier reports whether the json tag opts out of empty values,
// returning the specific modifier that triggered the match (omitempty or
// omitzero). When neither is present, it returns false and "".
func jsonOmitModifier(tag string) (bool, string) {
	switch {
	case hasOption(tag, "omitempty"):
		return true, "omitempty"
	case hasOption(tag, "omitzero"):
		return true, "omitzero"
	default:
		return false, ""
	}
}

// hasOption reports whether the comma-separated options on tag include opt.
// The first comma-separated entry is the field name; only later entries are
// modifiers (`omitempty`, `omitzero`, `inline`, …).
func hasOption(tag, opt string) bool {
	for i, part := range strings.Split(tag, ",") {
		if i == 0 {
			continue
		}
		if part == opt {
			return true
		}
	}
	return false
}

// isInline reports whether the tag carries a ",inline" option, which is used
// by both json/yaml libraries to flatten an embedded struct into the parent.
func isInline(tag string) bool {
	return hasOption(tag, "inline")
}

// modifierLabel renders a human-readable description of the modifier for an
// error message: the modifier name when present, "<none>" otherwise.
func modifierLabel(present bool, name string) string {
	if !present {
		return "<none>"
	}
	return name
}

// fieldNames returns the comma-separated list of field identifiers for use in
// diagnostic messages. Anonymous (embedded) fields fall back to "<embedded>".
func fieldNames(field *ast.Field) string {
	if len(field.Names) == 0 {
		return "<embedded>"
	}
	names := make([]string, 0, len(field.Names))
	for _, n := range field.Names {
		names = append(names, n.Name)
	}
	return strings.Join(names, ", ")
}
