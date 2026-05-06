package main

import (
	"go/ast"
	"strings"

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
// The rule fires when the cop can statically see a context name visible
// in the call's enclosing function:
//
//   - a parameter or named result whose type is `context.Context`;
//   - a local var declared from a context-producing expression (e.g.
//     `ctx := cmd.Context()`, `ctx := context.Background()`, or the first
//     return of `context.WithCancel(...)` / `context.WithTimeout(...)`).
//
// Function literals inherit context names from every enclosing function,
// so a `defer func() { slog.Error(...) }()` placed inside a function
// that holds a ctx is flagged.
//
// Convention: always declare the context near the top of the function so
// that every later log statement sees it. Helpers without an in-scope
// context (e.g. `applyTheme()` in cmd/root/run.go) are intentionally not
// flagged: rewriting them would force callers to thread a context
// through APIs that don't otherwise need one.
//
// Per-line suppression is provided centrally by the runner: annotate the
// line with `//rubocop:disable Lint/SlogContextual` to opt out — the
// canonical use is a deliberate "no context here" log inside a helper
// where `context.TODO()` would be more misleading than informative.
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
		if fn.Body == nil {
			return
		}
		c.checkScope(p, fn.Type, fn.Body, false)
	})
}

// checkScope reports bare slog calls inside body when a context name is
// visible in the enclosing function or any outer function literal.
// outerHasContext propagates that visibility into nested literals.
func (c *SlogContextual) checkScope(p *cop.Pass, typ *ast.FuncType, body *ast.BlockStmt, outerHasContext bool) {
	hasContext := outerHasContext || hasContextInFuncType(typ) || hasContextBindingInBlock(body)

	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			c.checkScope(p, x.Type, x.Body, hasContext)
			return false
		case *ast.CallExpr:
			if hasContext {
				c.maybeReport(p, x)
			}
		}
		return true
	})
}

// maybeReport reports an offense when call is `slog.<Level>(...)` for one
// of the four level helpers that have a Context-aware sibling.
func (c *SlogContextual) maybeReport(p *cop.Pass, call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "slog" {
		return
	}
	ctxVariant, ok := slogContextEquivalent[sel.Sel.Name]
	if !ok {
		return
	}
	p.Report(call,
		"a context is in scope; use slog.%s(ctx, …) instead of slog.%s so handlers (e.g. OpenTelemetry) can read it",
		ctxVariant, sel.Sel.Name)
}

// hasContextInFuncType reports whether typ has any parameter or named
// result whose syntactic type is `context.Context`.
func hasContextInFuncType(typ *ast.FuncType) bool {
	return fieldListHasContext(typ.Params) || fieldListHasContext(typ.Results)
}

func fieldListHasContext(fl *ast.FieldList) bool {
	if fl == nil {
		return false
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
	return false
}

// hasContextBindingInBlock reports whether body locally binds at least one
// identifier to a context.Context value, using purely syntactic shape
// recognition. Nested function literals are skipped — their bindings are
// inspected separately when checkScope recurses into them.
func hasContextBindingInBlock(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch s := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			if assignBindsContext(s.Lhs, s.Rhs) {
				found = true
			}
		case *ast.ValueSpec:
			if isContextContextType(s.Type) {
				for _, n := range s.Names {
					if n.Name != "" && n.Name != "_" {
						found = true
						return false
					}
				}
			}
			if len(s.Values) > 0 {
				lhs := make([]ast.Expr, len(s.Names))
				for i, n := range s.Names {
					lhs[i] = n
				}
				if assignBindsContext(lhs, s.Values) {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// assignBindsContext reports whether an assignment binds at least one LHS
// identifier to a context.Context value, using purely syntactic shape
// recognition.
func assignBindsContext(lhs, rhs []ast.Expr) bool {
	if len(rhs) == 1 && len(lhs) >= 1 {
		// Multi-value RHS like `ctx, cancel := context.WithCancel(parent)`:
		// the first return of context.With* is the derived Context.
		if call, ok := rhs[0].(*ast.CallExpr); ok && isContextWithCall(call) {
			return identName(lhs[0]) != ""
		}
	}
	for i := 0; i < len(lhs) && i < len(rhs); i++ {
		if isContextProducingExpr(rhs[i]) && identName(lhs[i]) != "" {
			return true
		}
	}
	return false
}

func identName(e ast.Expr) string {
	id, ok := e.(*ast.Ident)
	if !ok || id.Name == "" || id.Name == "_" {
		return ""
	}
	return id.Name
}

// isContextProducingExpr reports whether expr is a syntactic shape that
// commonly yields a context.Context: any call into the `context` package,
// or any zero-arg method named `Context` (the cobra/http convention).
func isContextProducingExpr(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if id, ok := sel.X.(*ast.Ident); ok && id.Name == "context" {
		return true
	}
	return sel.Sel.Name == "Context" && len(call.Args) == 0
}

// isContextWithCall reports whether call is `context.With<Something>(...)`,
// the family that returns (Context, CancelFunc) and friends.
func isContextWithCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "context" {
		return false
	}
	return strings.HasPrefix(sel.Sel.Name, "With")
}

// isContextContextType reports whether expr is the syntactic type
// `context.Context`. Type aliases and embeddings are out of scope.
func isContextContextType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "context" && sel.Sel.Name == "Context"
}
