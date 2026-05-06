package main

import (
	"go/ast"
	"slices"

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

// slogLevels is the set of slog top-level helpers whose Context-aware
// sibling (e.g. Debug → DebugContext) should be preferred when a context
// is in scope. slog.Log / slog.LogAttrs already take a context.
var slogLevels = map[string]bool{
	"Debug": true,
	"Info":  true,
	"Warn":  true,
	"Error": true,
}

func (c *SlogContextual) Check(p *cop.Pass) {
	p.ForEachFunc(func(fn *ast.FuncDecl) {
		if fn.Body != nil {
			c.checkFunc(p, fn.Type, fn.Body, false)
		}
	})
}

// checkFunc reports bare slog calls inside body when a context is reachable
// from any enclosing function. outerHasContext propagates that visibility
// into nested function literals, since closures capture by name.
func (c *SlogContextual) checkFunc(p *cop.Pass, typ *ast.FuncType, body *ast.BlockStmt, outerHasContext bool) {
	hasContext := outerHasContext || signatureDeclaresContext(typ) || bodyDeclaresContext(body)
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			c.checkFunc(p, x.Type, x.Body, hasContext)
			return false
		case *ast.CallExpr:
			if hasContext {
				c.reportIfBareSlog(p, x)
			}
		}
		return true
	})
}

// reportIfBareSlog reports an offense when call is `slog.<Level>(...)`
// for one of the four level helpers that have a Context-aware sibling.
func (c *SlogContextual) reportIfBareSlog(p *cop.Pass, call *ast.CallExpr) {
	pkg, level, ok := cop.MatchSelector(call.Fun)
	if !ok || pkg != "slog" || !slogLevels[level] {
		return
	}
	p.Report(call,
		"a context is in scope; use slog.%s(ctx, …) instead of slog.%s so handlers (e.g. OpenTelemetry) can read it",
		level+"Context", level)
}

// signatureDeclaresContext reports whether typ declares any parameter or
// named result of type context.Context.
func signatureDeclaresContext(typ *ast.FuncType) bool {
	for _, fl := range []*ast.FieldList{typ.Params, typ.Results} {
		if fl == nil {
			continue
		}
		for _, f := range fl.List {
			if !isContextContextType(f.Type) {
				continue
			}
			// Anonymous parameter (e.g., `func(context.Context)`) has no names
			// but still declares a context in scope.
			if len(f.Names) == 0 {
				return true
			}
			for _, n := range f.Names {
				if namedIdent(n) {
					return true
				}
			}
		}
	}
	return false
}

// bodyDeclaresContext reports whether body locally binds an identifier to
// a context.Context value. Nested function literals are skipped — their
// bindings are inspected separately when checkFunc recurses into them.
func bodyDeclaresContext(body *ast.BlockStmt) bool {
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
			found = valueSpecDeclaresContext(s)
		}
		return !found
	})
	return found
}

// valueSpecDeclaresContext reports whether s declares a context.Context,
// either explicitly via its declared type (`var ctx context.Context`) or
// implicitly via its initializer (`var ctx = context.Background()`).
func valueSpecDeclaresContext(s *ast.ValueSpec) bool {
	if isContextContextType(s.Type) {
		for _, n := range s.Names {
			if namedIdent(n) {
				return true
			}
		}
	}
	if len(s.Values) == 0 {
		return false
	}
	lhs := make([]ast.Expr, len(s.Names))
	for i, n := range s.Names {
		lhs[i] = n
	}
	return bindsContext(lhs, s.Values)
}

// bindsContext reports whether the assignment binds at least one LHS
// identifier to a context.Context value. A single RHS covers both
// single-value forms (`ctx := f()`) and multi-value returns
// (`ctx, cancel := context.WithCancel(...)`); we check all LHS positions
// since the context might not be at position 0 (e.g., `err, ctx := fn()`).
func bindsContext(lhs, rhs []ast.Expr) bool {
	if len(rhs) == 1 {
		// Single RHS: could be single-value or multi-return.
		// For multi-return, check all LHS positions since context
		// might not be at position 0 (e.g., `err, ctx := fn()`).
		return contextProducer(rhs[0]) && slices.ContainsFunc(lhs, namedIdent)
	}
	for i := 0; i < len(lhs) && i < len(rhs); i++ {
		if contextProducer(rhs[i]) && namedIdent(lhs[i]) {
			return true
		}
	}
	return false
}

// namedIdent reports whether e is a named identifier — anonymous (`_`)
// or unset names produce no usable scope binding.
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
// `context.Context`.
func isContextContextType(expr ast.Expr) bool {
	pkg, sel, ok := cop.MatchSelector(expr)
	return ok && pkg == "context" && sel == "Context"
}
