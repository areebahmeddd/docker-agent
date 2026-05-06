package main

import (
	"go/ast"

	"github.com/dgageot/rubocop-go/cop"
)

// SlogContextual enforces that callers use the *Context variant of every
// slog top-level helper whenever a context.Context is reachable in the
// enclosing function.
//
// Go 1.21's `log/slog` exposes DebugContext / InfoContext / WarnContext /
// ErrorContext so that handlers can read request-scoped values from the
// active context — most importantly the OpenTelemetry trace/span IDs the
// project's handler stamps on every record. The bare Debug / Info / Warn /
// Error helpers drop the context, so any context-aware handler ends up
// emitting records that cannot be correlated with the surrounding trace.
//
// The rule fires when the cop can syntactically see a context name in the
// call's enclosing function:
//
//   - a parameter or named result whose type is `context.Context`;
//   - a local var declared from a context-producing expression (e.g.
//     `ctx := cmd.Context()`, `ctx := context.Background()`, or the first
//     return of `context.WithCancel(...)` / `context.WithTimeout(...)`).
//
// Function literals inherit context names from every enclosing function,
// so a `defer func() { slog.Error(...) }()` inside a function that holds
// a ctx is flagged.
//
// Convention: declare the context near the top of the function so that
// every later log statement sees it. Helpers without an in-scope context
// are intentionally not flagged: rewriting them would force callers to
// thread a context through APIs that don't otherwise need one.
//
// Per-line suppression: `//rubocop:disable Lint/SlogContextual`.
type SlogContextual struct {
	cop.Meta
}

// NewSlogContextual returns a fully configured SlogContextual cop.
func NewSlogContextual() *SlogContextual {
	return &SlogContextual{Meta: cop.Meta{
		CopName:     "Lint/SlogContextual",
		CopDesc:     "use slog.{Level}Context(ctx, …) when a context is in scope",
		CopSeverity: cop.Warning,
	}}
}

// slogContextEquivalent maps a bare slog top-level helper to its
// context-aware variant. Only the four level helpers are listed because
// they are the only ones the codebase calls in their non-Context form;
// slog.Log / slog.LogAttrs already take a context.
var slogContextEquivalent = map[string]string{
	"Debug": "DebugContext",
	"Info":  "InfoContext",
	"Warn":  "WarnContext",
	"Error": "ErrorContext",
}

func (c *SlogContextual) Check(p *cop.Pass) {
	p.ForEachFunc(func(fn *ast.FuncDecl) {
		if fn.Body != nil {
			c.checkScope(p, fn.Type, fn.Body, false)
		}
	})
}

// checkScope reports bare slog calls inside body when a context is reachable
// from any enclosing function. outerHasContext propagates that visibility
// into nested function literals, since closures capture by name.
func (c *SlogContextual) checkScope(p *cop.Pass, typ *ast.FuncType, body *ast.BlockStmt, outerHasContext bool) {
	hasContext := outerHasContext || hasFuncTypeContext(typ) || hasLocalContext(body)
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			c.checkScope(p, x.Type, x.Body, hasContext)
			return false
		case *ast.CallExpr:
			if !hasContext {
				return true
			}
			pkg, level, ok := cop.MatchSelector(x.Fun)
			if !ok || pkg != "slog" {
				return true
			}
			if ctxVariant, ok := slogContextEquivalent[level]; ok {
				p.Report(x,
					"a context is in scope; use slog.%s(ctx, …) instead of slog.%s so handlers (e.g. OpenTelemetry) can read it",
					ctxVariant, level)
			}
		}
		return true
	})
}

// hasFuncTypeContext reports whether typ has any parameter or named result
// declared as context.Context.
func hasFuncTypeContext(typ *ast.FuncType) bool {
	for _, fl := range []*ast.FieldList{typ.Params, typ.Results} {
		if fl == nil {
			continue
		}
		for _, f := range fl.List {
			if !isContextContextType(f.Type) {
				continue
			}
			for _, n := range f.Names {
				if n.Name != "" && n.Name != "_" {
					return true
				}
			}
		}
	}
	return false
}

// hasLocalContext reports whether body locally binds an identifier to a
// context.Context value. Nested function literals are skipped — their
// bindings are inspected separately when checkScope recurses into them.
func hasLocalContext(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch s := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			found = bindsContext(s.Lhs, s.Rhs)
		case *ast.ValueSpec:
			if isContextContextType(s.Type) {
				for _, n := range s.Names {
					if n.Name != "" && n.Name != "_" {
						found = true
						break
					}
				}
			}
			if !found && len(s.Values) > 0 {
				lhs := make([]ast.Expr, len(s.Names))
				for i, n := range s.Names {
					lhs[i] = n
				}
				found = bindsContext(lhs, s.Values)
			}
		}
		return !found
	})
	return found
}

// bindsContext reports whether the assignment binds at least one LHS
// identifier to a context.Context value. A single RHS handles both
// single-value forms (`ctx := f()`) and multi-value returns
// (`ctx, cancel := context.WithCancel(...)`); in the latter case the
// context is always at the first return position.
func bindsContext(lhs, rhs []ast.Expr) bool {
	if len(rhs) == 1 {
		return len(lhs) >= 1 && contextProducer(rhs[0]) && namedIdent(lhs[0])
	}
	for i := 0; i < len(lhs) && i < len(rhs); i++ {
		if contextProducer(rhs[i]) && namedIdent(lhs[i]) {
			return true
		}
	}
	return false
}

func namedIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name != "" && id.Name != "_"
}

// contextProducer reports whether expr is a call whose shape commonly
// yields a context.Context: any call into the `context` package, or any
// zero-arg method named `Context` (the cobra / http.Request convention).
func contextProducer(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	pkg, sel, ok := cop.MatchSelector(call.Fun)
	return ok && (pkg == "context" || (sel == "Context" && len(call.Args) == 0))
}

// isContextContextType reports whether expr is the syntactic type
// `context.Context`. Type aliases and embeddings are out of scope.
func isContextContextType(expr ast.Expr) bool {
	pkg, sel, ok := cop.MatchSelector(expr)
	return ok && pkg == "context" && sel == "Context"
}
