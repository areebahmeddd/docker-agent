package main

import (
	"go/ast"
	"path/filepath"
	"strings"

	"github.com/dgageot/rubocop-go/cop"
)

// RuntimeSessionScoped enforces that every runtime event struct carrying a
// SessionID field also implements the SessionScoped interface, i.e. exposes
// a `GetSessionID() string` method on its pointer receiver.
//
// SessionScoped is the discriminator the persistence pipeline uses to keep
// sub-agent events out of the parent session's transcript:
//
//	if scoped, ok := event.(SessionScoped); ok && scoped.GetSessionID() != sess.ID {
//	    return // forwarded sub-agent event for a different session — drop
//	}
//
// An event that carries SessionID but skips the method silently bypasses the
// filter — the type assertion fails, the conditional short-circuits, and
// the event is persisted into whatever session happens to be on the parent
// observer when it arrives. That contaminates transcripts with sub-agent
// chatter and there is no signal at the call site to suggest the rule was
// missed.
//
// The cop runs on pkg/runtime/event.go and reports any *Event struct with
// a SessionID field whose pointer type lacks GetSessionID().
type RuntimeSessionScoped struct {
	cop.Meta
}

// NewRuntimeSessionScoped returns a fully configured RuntimeSessionScoped cop.
func NewRuntimeSessionScoped() *RuntimeSessionScoped {
	return &RuntimeSessionScoped{Meta: cop.Meta{
		CopName:     "Lint/RuntimeSessionScoped",
		CopDesc:     "runtime events with a SessionID field must implement GetSessionID() (SessionScoped)",
		CopSeverity: cop.Error,
	}}
}

func (c *RuntimeSessionScoped) Check(p *cop.Pass) {
	if !isRuntimeEventGo(p.Filename()) {
		return
	}

	withSessionID := eventStructsWithSessionID(p)
	if len(withSessionID) == 0 {
		return
	}
	implementsScoped := pointerReceiversWithMethod(p, "GetSessionID")

	for typeName, typeSpec := range withSessionID {
		if !implementsScoped[typeName] {
			p.Report(typeSpec.Name,
				"%s carries a SessionID field but does not implement GetSessionID() (SessionScoped); "+
					"sub-agent events of this type would bypass the persistence-observer session filter",
				typeName)
		}
	}
}

// isRuntimeEventGo reports whether filename is the canonical
// pkg/runtime/event.go that defines all runtime event types.
func isRuntimeEventGo(filename string) bool {
	slash := filepath.ToSlash(filename)
	return strings.HasSuffix(slash, "/pkg/runtime/event.go") || slash == "pkg/runtime/event.go"
}

// eventStructsWithSessionID maps EventTypeName -> its declaring *ast.TypeSpec
// for every top-level struct type whose name ends in "Event" and that
// declares (or transitively re-declares via its own field — embedded fields
// are out of scope here) a SessionID field.
func eventStructsWithSessionID(p *cop.Pass) map[string]*ast.TypeSpec {
	out := map[string]*ast.TypeSpec{}
	for _, decl := range p.File.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(ts.Name.Name, "Event") {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			for _, fld := range st.Fields.List {
				for _, name := range fld.Names {
					if name.Name == "SessionID" {
						out[ts.Name.Name] = ts
					}
				}
			}
		}
	}
	return out
}

// pointerReceiversWithMethod returns the set of type names T for which the
// file declares `func (* T) <method>(...)`. Used to detect interface
// satisfaction without invoking the type checker.
func pointerReceiversWithMethod(p *cop.Pass, method string) map[string]bool {
	with := map[string]bool{}
	p.ForEachFunc(func(fn *ast.FuncDecl) {
		if fn.Name.Name != method || fn.Recv == nil || len(fn.Recv.List) != 1 {
			return
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			return
		}
		id, ok := star.X.(*ast.Ident)
		if !ok {
			return
		}
		with[id.Name] = true
	})
	return with
}
