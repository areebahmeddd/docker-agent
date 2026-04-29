package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/dgageot/rubocop-go/cop"
)

// RuntimeEventRegistry enforces that pkg/runtime/client.go registers every
// runtime event type defined in pkg/runtime/event.go.
//
// The remote-runtime client decodes incoming JSON events by reading the
// "type" discriminator and looking it up in a static registry built in
// pkg/runtime/client.go:
//
//	registry: map[string]func() Event{
//	    "user_message": func() Event { return &UserMessageEvent{} },
//	    …
//	}
//
// When an event type appears in the JSON stream but not in the registry the
// client logs a single Debug-level "invalid_type" line and silently drops
// the event. That is essentially invisible: a sub-agent's
// MessageAddedEvent, ModelFallbackEvent or SubSessionCompletedEvent
// reaching a remote frontend would never be rendered, with no visible
// error.
//
// The cop runs on pkg/runtime/client.go (the registry's home) and
// cross-references every &XxxEvent{Type: "yyy"} composite literal it
// finds in pkg/runtime/event.go. Each event type whose Type-string is
// missing from the registry is reported with a diagnostic anchored on
// the registry map literal, where the fix lives.
type RuntimeEventRegistry struct {
	cop.Meta
}

// NewRuntimeEventRegistry returns a fully configured RuntimeEventRegistry cop.
func NewRuntimeEventRegistry() *RuntimeEventRegistry {
	return &RuntimeEventRegistry{Meta: cop.Meta{
		CopName:     "Lint/RuntimeEventRegistry",
		CopDesc:     "pkg/runtime/client.go must register every runtime event type",
		CopSeverity: cop.Error,
	}}
}

func (c *RuntimeEventRegistry) Check(p *cop.Pass) {
	if !isRuntimeClientGo(p.Filename()) {
		return
	}

	// Sibling event.go is the source of truth for the set of emitted events.
	eventGo := filepath.Join(filepath.Dir(p.Filename()), "event.go")
	emitted, err := emittedEventTypes(eventGo)
	if err != nil || len(emitted) == 0 {
		return
	}

	registryNode, registered := registryEntries(p)
	if registryNode == nil {
		return
	}

	var missing []string
	for typeName, typeStr := range emitted {
		if got, ok := registered[typeStr]; !ok {
			missing = append(missing, typeName+" (Type: "+strconv.Quote(typeStr)+")")
		} else if got != typeName {
			// The registry key is correct but it constructs the wrong
			// concrete type. That is a different bug from "missing entry"
			// so we keep it as a separate diagnostic anchored on the
			// registry map for clarity.
			p.Report(registryNode,
				"registry key %q constructs %s but %s emits Type=%q",
				typeStr, got, typeName, typeStr)
		}
	}
	if len(missing) == 0 {
		return
	}
	slices.Sort(missing)
	p.Report(registryNode,
		"pkg/runtime/client.go is missing registry entries for: %s", strings.Join(missing, ", "))
}

// isRuntimeClientGo reports whether filename is the canonical
// pkg/runtime/client.go that owns the event-decoder registry.
func isRuntimeClientGo(filename string) bool {
	slash := filepath.ToSlash(filename)
	return strings.HasSuffix(slash, "/pkg/runtime/client.go") || slash == "pkg/runtime/client.go"
}

// emittedEventTypes returns a map of EventTypeName -> wire-format Type
// string for every &XxxEvent{Type: "yyy"} composite literal in the file
// at path (typically pkg/runtime/event.go). Constructors that don't
// initialise the Type field are skipped — a separate cop could enforce
// that they always do.
func emittedEventTypes(path string) (map[string]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	emitted := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		id, ok := cl.Type.(*ast.Ident)
		if !ok || !strings.HasSuffix(id.Name, "Event") {
			return true
		}
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, ok := kv.Key.(*ast.Ident)
			if !ok || k.Name != "Type" {
				continue
			}
			lit, ok := kv.Value.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			emitted[id.Name] = val
		}
		return true
	})
	return emitted, nil
}

// registryEntries finds the registry map literal in pkg/runtime/client.go and
// returns it together with a typeString -> EventTypeName mapping built from
// its `"type-string": func() Event { return &XxxEvent{} }` entries.
//
// The registry is identified syntactically as the only map[string]func() Event
// composite literal in the file. Returning the literal itself lets the
// caller anchor diagnostics on the map so the diff is obvious.
func registryEntries(p *cop.Pass) (ast.Node, map[string]string) {
	var found *ast.CompositeLit
	registered := map[string]string{}
	ast.Inspect(p.File, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if !isEventRegistryType(cl.Type) {
			return true
		}
		found = cl
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			klit, ok := kv.Key.(*ast.BasicLit)
			if !ok || klit.Kind != token.STRING {
				continue
			}
			key, err := strconv.Unquote(klit.Value)
			if err != nil {
				continue
			}
			if name, ok := returnedEventType(kv.Value); ok {
				registered[key] = name
			}
		}
		return false
	})
	if found == nil {
		return nil, nil
	}
	return found, registered
}

// isEventRegistryType reports whether expr describes the registry's value
// type, i.e. `map[string]func() Event`. The check is syntactic: the cop
// runs without type information.
func isEventRegistryType(expr ast.Expr) bool {
	mt, ok := expr.(*ast.MapType)
	if !ok {
		return false
	}
	keyID, ok := mt.Key.(*ast.Ident)
	if !ok || keyID.Name != "string" {
		return false
	}
	ft, ok := mt.Value.(*ast.FuncType)
	if !ok {
		return false
	}
	if ft.Params != nil && len(ft.Params.List) != 0 {
		return false
	}
	if ft.Results == nil || len(ft.Results.List) != 1 {
		return false
	}
	rid, ok := ft.Results.List[0].Type.(*ast.Ident)
	return ok && rid.Name == "Event"
}

// returnedEventType pulls "FooEvent" out of a `func() Event { return &FooEvent{} }`
// literal. Returns "" if the closure has any other shape, in which case the
// cop simply ignores the entry (an unrelated map happening to share the
// registry's value type would otherwise produce noise).
func returnedEventType(expr ast.Expr) (string, bool) {
	fl, ok := expr.(*ast.FuncLit)
	if !ok || fl.Body == nil {
		return "", false
	}
	for _, stmt := range fl.Body.List {
		ret, ok := stmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			continue
		}
		ue, ok := ret.Results[0].(*ast.UnaryExpr)
		if !ok || ue.Op != token.AND {
			continue
		}
		cl, ok := ue.X.(*ast.CompositeLit)
		if !ok {
			continue
		}
		id, ok := cl.Type.(*ast.Ident)
		if !ok {
			continue
		}
		return id.Name, true
	}
	return "", false
}
