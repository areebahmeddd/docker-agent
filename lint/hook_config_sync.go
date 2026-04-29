package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/dgageot/rubocop-go/cop"
)

// HookConfigSync enforces that the EventXxx constants in pkg/hooks/types.go
// stay in lock-step with the HooksConfig fields in pkg/config/latest/types.go.
//
// The two are coupled: every hook event the runtime knows how to dispatch
// has to be configurable in the agent YAML, and every YAML field has to
// correspond to an event the runtime actually fires. The mapping is by
// snake-case wire string:
//
//	pkg/hooks/types.go        : EventPreToolUse EventType = "pre_tool_use"
//	pkg/config/latest/types.go: PreToolUse []HookMatcherConfig `json:"pre_tool_use,…"`
//
// Drift in either direction is silently broken at runtime:
//
//   - A new EventXxx constant without a matching HooksConfig field means
//     the YAML schema cannot express that hook — users have no way to
//     register one, and the event fires with no listeners.
//   - A new HooksConfig field without a matching EventXxx constant means
//     the runtime parses the YAML but never dispatches the event — the
//     hook is wired up but inert.
//
// Neither failure mode produces a build error or a runtime warning. The
// cop runs on pkg/config/latest/types.go (where the diagnostic anchors
// on the HooksConfig type spec) and parses pkg/hooks/types.go from disk
// for the source of truth.
type HookConfigSync struct {
	cop.Meta
}

// NewHookConfigSync returns a fully configured HookConfigSync cop.
func NewHookConfigSync() *HookConfigSync {
	return &HookConfigSync{Meta: cop.Meta{
		CopName:     "Lint/HookConfigSync",
		CopDesc:     "EventXxx constants in pkg/hooks/types.go must match HooksConfig fields in pkg/config/latest",
		CopSeverity: cop.Error,
	}}
}

func (c *HookConfigSync) Check(p *cop.Pass) {
	if !isLatestConfigTypesGo(p.Filename()) {
		return
	}

	hookEvents, err := readHookEventConstants(hooksTypesGoPath(p.Filename()))
	if err != nil || len(hookEvents) == 0 {
		return
	}

	cfgFields, hooksConfigSpec := readHooksConfigFields(p)
	if hooksConfigSpec == nil {
		// Schema didn't ship HooksConfig (or this isn't the right
		// types.go) — nothing meaningful the cop can say.
		return
	}

	// Build reverse map for diagnostics.
	cfgByJSON := map[string]string{} // wire-string -> Go field name
	for goName, jsonName := range cfgFields {
		cfgByJSON[jsonName] = goName
	}

	// Direction 1: every event must have a config field.
	var missingFields []string
	for constName, wire := range hookEvents {
		if _, ok := cfgByJSON[wire]; !ok {
			missingFields = append(missingFields, constName+"="+strconv.Quote(wire))
		}
	}
	if len(missingFields) > 0 {
		slices.Sort(missingFields)
		p.Report(hooksConfigSpec.Name,
			"HooksConfig is missing field(s) for hook event(s): %s", strings.Join(missingFields, ", "))
	}

	// Direction 2: every config field must have an event constant.
	wireSet := map[string]string{} // wire -> const name
	for n, w := range hookEvents {
		wireSet[w] = n
	}
	var orphanFields []string
	for goName, jsonName := range cfgFields {
		if _, ok := wireSet[jsonName]; !ok {
			orphanFields = append(orphanFields, goName+" json:"+strconv.Quote(jsonName))
		}
	}
	if len(orphanFields) > 0 {
		slices.Sort(orphanFields)
		p.Report(hooksConfigSpec.Name,
			"HooksConfig field(s) without a matching EventXxx constant in pkg/hooks/types.go: %s",
			strings.Join(orphanFields, ", "))
	}
}

// isLatestConfigTypesGo reports whether filename is the canonical
// pkg/config/latest/types.go that owns HooksConfig.
func isLatestConfigTypesGo(filename string) bool {
	slash := filepath.ToSlash(filename)
	return strings.HasSuffix(slash, "/pkg/config/latest/types.go") ||
		slash == "pkg/config/latest/types.go"
}

// hooksTypesGoPath returns the location of pkg/hooks/types.go relative to
// the repository root inferred from the latest config file's path. The
// cop walks up from `.../pkg/config/latest/types.go` to find the repo root.
func hooksTypesGoPath(latestTypesGo string) string {
	// .../pkg/config/latest/types.go -> .../pkg/hooks/types.go
	return filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(latestTypesGo))), "hooks", "types.go")
}

// readHookEventConstants parses the file at path and returns a map of
// EventXxx constant name -> string value for every const whose name
// starts with "Event" and whose value is a string literal.
func readHookEventConstants(path string) (map[string]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
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
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Event") {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				out[name.Name] = val
			}
		}
	}
	return out, nil
}

// readHooksConfigFields scans the file in p for the HooksConfig type and
// returns the field-name -> json-tag-name map together with the type spec
// itself (so the caller can anchor diagnostics on it).
func readHooksConfigFields(p *cop.Pass) (map[string]string, *ast.TypeSpec) {
	fields := map[string]string{}
	var spec *ast.TypeSpec
	for _, decl := range p.File.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range gd.Specs {
			ts, ok := s.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "HooksConfig" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			spec = ts
			for _, fld := range st.Fields.List {
				if fld.Tag == nil {
					continue
				}
				raw, err := strconv.Unquote(fld.Tag.Value)
				if err != nil {
					continue
				}
				jsonTag, ok := reflect.StructTag(raw).Lookup("json")
				if !ok {
					continue
				}
				name, _, _ := strings.Cut(jsonTag, ",")
				if name == "" || name == "-" {
					continue
				}
				for _, n := range fld.Names {
					fields[n.Name] = name
				}
			}
		}
	}
	return fields, spec
}
