package main

import (
	"go/ast"
	"go/token"
	"strings"

	"github.com/dgageot/rubocop-go/cop"
)

// SlogContextual enforces that callers use the *Context variant of every
// slog top-level helper whenever a context.Context is reachable in scope.
//
// Go 1.21's `log/slog` exposes DebugContext / InfoContext / WarnContext /
// ErrorContext so that handlers can read request-scoped values from the
// active context — most importantly the OpenTelemetry trace/span IDs the
// project's handler stamps on every record. The bare Debug / Info / Warn /
// Error helpers drop the context, so any context-aware handler ends up
// emitting records that cannot be correlated with the surrounding trace.
//
// The rule fires only when the cop can statically see a context name that
// is *already in scope* at the call site:
//
//   - a parameter or named result whose type is `context.Context`;
//   - a local var declared earlier in the same function from a context-
//     producing expression (e.g. `ctx := cmd.Context()`,
//     `ctx := context.Background()`, or the first return of
//     `context.WithCancel(...)` / `context.WithTimeout(...)`).
//
// Function literals inherit the names visible at the position where the
// literal is written, so a `defer func() { slog.Error(...) }()` placed
// after `ctx := cmd.Context()` is flagged.
//
// Helpers without an in-scope context (e.g. `applyTheme()` in the TUI)
// are intentionally not flagged: rewriting them would force callers to
// thread a context through APIs that don't otherwise need one. When that
// trade-off is acceptable, callers can capture `cmd.Context()` into a
// local and the cop will then enforce the `*Context` variant inside.
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

// contextBinding records a context-typed identifier alongside the source
// position at which the identifier becomes visible (after the binding
// statement). Captured names are compared against later positions so the
// cop only treats a name as in scope once its declaration has executed.
type contextBinding struct {
	name string
	pos  token.Pos
}

func (c *SlogContextual) Check(p *cop.Pass) {
	p.ForEachFunc(func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		c.checkScope(p, fn.Type, fn.Body, nil)
	})
}

// checkScope walks body and reports bare slog calls that have a context
// name visible at their position. outer carries the bindings already
// visible from enclosing scopes (parameters of outer functions, locals
// of the enclosing block declared before this function literal, …).
func (c *SlogContextual) checkScope(p *cop.Pass, typ *ast.FuncType, body *ast.BlockStmt, outer []contextBinding) {
	// Parameters and named results are visible from the very first byte
	// of the body.
	bindings := append([]contextBinding(nil), outer...)
	for _, name := range contextNamesInFuncType(typ) {
		bindings = append(bindings, contextBinding{name: name, pos: body.Lbrace})
	}
	// Add every locally-derived context, recorded at the position right
	// after its binding statement so a use on the same line as the
	// declaration is correctly considered in scope.
	bindings = append(bindings, contextBindingsInBlock(body)...)

	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			// Inherit only the names that exist at the position where
			// the literal is written.
			c.checkScope(p, x.Type, x.Body, visibleAt(bindings, x.Pos()))
			return false
		case *ast.CallExpr:
			if hasVisibleAt(bindings, x.Pos()) {
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

// contextNamesInFuncType returns the identifier names declared in fn's
// parameter or named-result lists whose syntactic type is `context.Context`.
func contextNamesInFuncType(typ *ast.FuncType) []string {
	var names []string
	names = appendContextFieldNames(names, typ.Params)
	names = appendContextFieldNames(names, typ.Results)
	return names
}

func appendContextFieldNames(names []string, fl *ast.FieldList) []string {
	if fl == nil {
		return names
	}
	for _, f := range fl.List {
		if !isContextContextType(f.Type) {
			continue
		}
		for _, n := range f.Names {
			if n.Name != "" && n.Name != "_" {
				names = append(names, n.Name)
			}
		}
	}
	return names
}

// contextBindingsInBlock collects locally-declared context-typed
// identifiers from body, recording each at the position right after the
// binding statement so that scope lookups respect declaration order.
// Nested function literals are skipped — their bindings are tracked
// separately when checkScope recurses into them.
func contextBindingsInBlock(body *ast.BlockStmt) []contextBinding {
	if body == nil {
		return nil
	}
	var out []contextBinding
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.AssignStmt:
			for _, name := range collectContextLHSNames(s.Lhs, s.Rhs) {
				out = append(out, contextBinding{name: name, pos: s.End()})
			}
		case *ast.ValueSpec:
			if isContextContextType(s.Type) {
				for _, n := range s.Names {
					if n.Name != "" && n.Name != "_" {
						out = append(out, contextBinding{name: n.Name, pos: s.End()})
					}
				}
			}
			if len(s.Values) > 0 {
				lhs := make([]ast.Expr, len(s.Names))
				for i, n := range s.Names {
					lhs[i] = n
				}
				for _, name := range collectContextLHSNames(lhs, s.Values) {
					out = append(out, contextBinding{name: name, pos: s.End()})
				}
			}
		}
		return true
	})
	return out
}

// collectContextLHSNames returns every LHS identifier name that an
// assignment binds to a context.Context value, using purely syntactic
// shape recognition.
func collectContextLHSNames(lhs, rhs []ast.Expr) []string {
	var names []string
	if len(rhs) == 1 && len(lhs) >= 1 {
		// Multi-value RHS like `ctx, cancel := context.WithCancel(parent)`:
		// the first return of context.With* is the derived Context.
		if call, ok := rhs[0].(*ast.CallExpr); ok && isContextWithCall(call) {
			if name := identName(lhs[0]); name != "" {
				names = append(names, name)
			}
			return names
		}
	}
	for i := 0; i < len(lhs) && i < len(rhs); i++ {
		if !isContextProducingExpr(rhs[i]) {
			continue
		}
		if name := identName(lhs[i]); name != "" {
			names = append(names, name)
		}
	}
	return names
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

// hasVisibleAt reports whether at least one binding from bindings is
// visible at position pos (i.e. its binding.pos < pos).
func hasVisibleAt(bindings []contextBinding, pos token.Pos) bool {
	for _, b := range bindings {
		if b.pos < pos {
			return true
		}
	}
	return false
}

// visibleAt returns the subset of bindings already visible at position pos.
func visibleAt(bindings []contextBinding, pos token.Pos) []contextBinding {
	var out []contextBinding
	for _, b := range bindings {
		if b.pos < pos {
			out = append(out, b)
		}
	}
	return out
}
