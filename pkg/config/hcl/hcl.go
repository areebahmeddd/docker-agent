// Package hcl provides an HCL → YAML converter for docker-agent configuration
// files. The HCL surface mirrors the YAML schema with a few conventions:
//
//   - Top-level keyed maps (agents, models, providers, mcps, rag) are written
//     as labeled blocks, e.g. `agent "root" { ... }` becomes
//     `agents: { root: { ... } }`.
//   - Inside an agent, `command "name" { ... }` becomes
//     `commands: { name: { ... } }`.
//   - Toolsets use the label as the `type` field:
//     `toolset "mcp" { ... }` becomes `toolsets: [{ type: mcp, ... }]`.
//   - Multi-line strings should use heredocs. Because HCL templates expand
//     `${...}` interpolation, any literal `${...}` (such as
//     `${shell({cmd: "..."})}`) must be escaped as `$${...}`.
//
// The converter does not validate the resulting document against the
// configuration schema; that is left to the existing YAML/JSON loader.
package hcl

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// LooksLikeHCL reports whether the given bytes look like an HCL document
// rather than a YAML one. The detection is heuristic and is intended for
// callers that do not have a filename hint to rely on (for example, OCI
// artifacts). It looks for top-level labeled blocks of the docker-agent
// HCL schema, e.g. `agent "..." {`, which are not valid YAML.
func LooksLikeHCL(data []byte) bool {
	for line := range strings.Lines(string(data)) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}
		for _, kw := range topLevelHCLKeywords {
			if strings.HasPrefix(trimmed, kw+" \"") || strings.HasPrefix(trimmed, kw+" {") {
				return true
			}
		}
		// The first non-comment, non-blank line is not an HCL block opener;
		// assume YAML.
		return false
	}
	return false
}

// topLevelHCLKeywords lists the block names that may legitimately appear at
// the top level of a docker-agent HCL document.
var topLevelHCLKeywords = []string{
	"agent",
	"model",
	"provider",
	"mcp",
	"rag",
	"metadata",
	"permissions",
}

// ToYAML parses an HCL document and returns an equivalent YAML document
// that can be fed to the existing docker-agent config loader.
func ToYAML(data []byte, filename string) ([]byte, error) {
	m, err := ToMap(data, filename)
	if err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encoding HCL config to YAML: %w", err)
	}
	return out, nil
}

// ToMap parses an HCL document and returns a generic map that mirrors the
// structure of the equivalent YAML document.
func ToMap(data []byte, filename string) (map[string]any, error) {
	parser := hclparse.NewParser()
	file, diags := parser.ParseHCL(data, filename)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parsing HCL %s: %s", filename, diags.Error())
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return nil, fmt.Errorf("HCL file %s is not native syntax", filename)
	}
	out, diags := convertBody(body)
	if diags.HasErrors() {
		return nil, fmt.Errorf("converting HCL %s: %s", filename, diags.Error())
	}
	return out, nil
}

type blockMode int

const (
	// modeMapByLabel: block has 1 label; output as a map keyed by the label.
	modeMapByLabel blockMode = iota
	// modeListLabelAsField: block has 1 label; output as a list with the label
	// injected as a named field of each entry.
	modeListLabelAsField
	// modeSingleton: block has 0 labels and may appear at most once.
	modeSingleton
	// modeList: block has 0 labels; multiple occurrences are appended to a list.
	modeList
)

type blockRule struct {
	mode       blockMode
	outKey     string
	labelField string // only used for modeListLabelAsField
}

// blockRules describes how each known block name is rendered in the YAML
// output. Block names not listed here fall back to defaults: 0-label blocks
// become singletons under the same key; 1-label blocks become maps keyed by
// the label under the same key.
var blockRules = map[string]blockRule{
	// Top-level keyed maps (and equivalents inside agents).
	"agent":    {mode: modeMapByLabel, outKey: "agents"},
	"model":    {mode: modeMapByLabel, outKey: "models"},
	"provider": {mode: modeMapByLabel, outKey: "providers"},
	"mcp":      {mode: modeMapByLabel, outKey: "mcps"},
	"rag":      {mode: modeMapByLabel, outKey: "rag"},
	"command":  {mode: modeMapByLabel, outKey: "commands"},
	// `shell "name" { ... }` is used inside script toolsets as a map of
	// scripted shell commands.
	"shell": {mode: modeMapByLabel, outKey: "shell"},

	// Toolsets are a list with the label encoded as the `type` field.
	"toolset": {mode: modeListLabelAsField, outKey: "toolsets", labelField: "type"},

	// Singletons.
	"permissions":       {mode: modeSingleton, outKey: "permissions"},
	"metadata":          {mode: modeSingleton, outKey: "metadata"},
	"hooks":             {mode: modeSingleton, outKey: "hooks"},
	"fallback":          {mode: modeSingleton, outKey: "fallback"},
	"cache":             {mode: modeSingleton, outKey: "cache"},
	"structured_output": {mode: modeSingleton, outKey: "structured_output"},
	"skills":            {mode: modeSingleton, outKey: "skills"},
	"lifecycle":         {mode: modeSingleton, outKey: "lifecycle"},
	"remote":            {mode: modeSingleton, outKey: "remote"},
	"oauth":             {mode: modeSingleton, outKey: "oauth"},
	"api_config":        {mode: modeSingleton, outKey: "api_config"},
	"rag_config":        {mode: modeSingleton, outKey: "rag_config"},
	"thinking_budget":   {mode: modeSingleton, outKey: "thinking_budget"},
	"task_budget":       {mode: modeSingleton, outKey: "task_budget"},
	"defer":             {mode: modeSingleton, outKey: "defer"},
	"fusion":            {mode: modeSingleton, outKey: "fusion"},
	"reranking":         {mode: modeSingleton, outKey: "reranking"},
	"chunking":          {mode: modeSingleton, outKey: "chunking"},
	"database":          {mode: modeSingleton, outKey: "database"},

	// 0-label blocks aggregated into lists.
	"post_edit":               {mode: modeList, outKey: "post_edit"},
	"strategy":                {mode: modeList, outKey: "strategies"},
	"routing":                 {mode: modeList, outKey: "routing"},
	"hook":                    {mode: modeList, outKey: "hooks"},
	"pre_tool_use":            {mode: modeList, outKey: "pre_tool_use"},
	"post_tool_use":           {mode: modeList, outKey: "post_tool_use"},
	"session_start":           {mode: modeList, outKey: "session_start"},
	"session_end":             {mode: modeList, outKey: "session_end"},
	"permission_request":      {mode: modeList, outKey: "permission_request"},
	"tool_response_transform": {mode: modeList, outKey: "tool_response_transform"},
}

// lookupRule returns the conversion rule for a block, falling back to a
// sensible default when the block name is not registered.
func lookupRule(name string, labels int) blockRule {
	if r, ok := blockRules[name]; ok {
		return r
	}
	if labels == 1 {
		return blockRule{mode: modeMapByLabel, outKey: name}
	}
	return blockRule{mode: modeSingleton, outKey: name}
}

func convertBody(body *hclsyntax.Body) (map[string]any, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	out := map[string]any{}

	// Iterate attributes in source order for deterministic error reporting.
	attrNames := make([]string, 0, len(body.Attributes))
	for name := range body.Attributes {
		attrNames = append(attrNames, name)
	}
	sort.Slice(attrNames, func(i, j int) bool {
		return body.Attributes[attrNames[i]].Range().Start.Byte <
			body.Attributes[attrNames[j]].Range().Start.Byte
	})

	for _, name := range attrNames {
		attr := body.Attributes[name]
		val, attrDiags := convertExpr(attr.Expr)
		diags = append(diags, attrDiags...)
		if existing, ok := out[name]; ok {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate key",
				Detail:   fmt.Sprintf("Key %q is set multiple times in the same block.", name),
				Subject:  attr.Range().Ptr(),
			})
			_ = existing
			continue
		}
		out[name] = val
	}

	for _, block := range body.Blocks {
		blockDiags := mergeBlock(out, block)
		diags = append(diags, blockDiags...)
	}

	return out, diags
}

func mergeBlock(out map[string]any, block *hclsyntax.Block) hcl.Diagnostics {
	rule := lookupRule(block.Type, len(block.Labels))

	body, diags := convertBody(block.Body)
	if diags.HasErrors() {
		return diags
	}

	switch rule.mode {
	case modeSingleton:
		if len(block.Labels) != 0 {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Unexpected block label",
				Detail:   fmt.Sprintf("Block %q does not take any label.", block.Type),
				Subject:  block.LabelRanges[0].Ptr(),
			}}
		}
		if _, exists := out[rule.outKey]; exists {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Duplicate block",
				Detail:   fmt.Sprintf("Block %q can only appear once in this scope.", block.Type),
				Subject:  block.DefRange().Ptr(),
			}}
		}
		out[rule.outKey] = body

	case modeList:
		if len(block.Labels) != 0 {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Unexpected block label",
				Detail:   fmt.Sprintf("Block %q does not take any label.", block.Type),
				Subject:  block.LabelRanges[0].Ptr(),
			}}
		}
		list, _ := out[rule.outKey].([]any)
		out[rule.outKey] = append(list, body)

	case modeMapByLabel:
		if len(block.Labels) != 1 {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Block label required",
				Detail:   fmt.Sprintf("Block %q expects exactly one label.", block.Type),
				Subject:  block.DefRange().Ptr(),
			}}
		}
		slice, _ := out[rule.outKey].(yaml.MapSlice)
		label := block.Labels[0]
		for _, item := range slice {
			if item.Key == label {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Duplicate block",
					Detail:   fmt.Sprintf("Block %q with label %q is defined more than once.", block.Type, label),
					Subject:  block.LabelRanges[0].Ptr(),
				}}
			}
		}
		slice = append(slice, yaml.MapItem{Key: label, Value: body})
		out[rule.outKey] = slice

	case modeListLabelAsField:
		if len(block.Labels) != 1 {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Block label required",
				Detail:   fmt.Sprintf("Block %q expects exactly one label.", block.Type),
				Subject:  block.DefRange().Ptr(),
			}}
		}
		body[rule.labelField] = block.Labels[0]
		list, _ := out[rule.outKey].([]any)
		out[rule.outKey] = append(list, body)
	}

	return nil
}

func convertExpr(expr hclsyntax.Expression) (any, hcl.Diagnostics) {
	val, diags := expr.Value(&hcl.EvalContext{})
	if diags.HasErrors() {
		return nil, diags
	}
	return ctyToGo(val), nil
}

// ctyToGo recursively converts a cty.Value into the Go primitives used by
// the YAML marshaller (string, int64, float64, bool, []any, map[string]any).
func ctyToGo(val cty.Value) any {
	if !val.IsKnown() || val.IsNull() {
		return nil
	}
	t := val.Type()
	switch {
	case t == cty.String:
		return val.AsString()
	case t == cty.Bool:
		return val.True()
	case t == cty.Number:
		bf := val.AsBigFloat()
		if i, acc := bf.Int64(); acc == big.Exact {
			return i
		}
		f, _ := bf.Float64()
		return f
	case t.IsListType(), t.IsSetType(), t.IsTupleType():
		out := make([]any, 0, val.LengthInt())
		for it := val.ElementIterator(); it.Next(); {
			_, v := it.Element()
			out = append(out, ctyToGo(v))
		}
		return out
	case t.IsObjectType(), t.IsMapType():
		out := map[string]any{}
		for it := val.ElementIterator(); it.Next(); {
			k, v := it.Element()
			out[k.AsString()] = ctyToGo(v)
		}
		return out
	}
	// Fallback for any unexpected type.
	return val.GoString()
}
