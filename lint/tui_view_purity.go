package main

import (
	"go/ast"
	"go/token"

	"github.com/dgageot/rubocop-go/cop"
)

// TUIViewPurity enforces that `View() string` methods on TUI models do not
// mutate the receiver's fields. This is the Bubble Tea / Elm-Architecture
// purity contract: rendering must be a pure function of state, otherwise
// rendering twice in a row can produce different output and the runtime is
// free to do exactly that (e.g. on resize, on a missed alt-screen redraw).
//
// The cop runs on every Go file under pkg/tui/ and inspects each method
// named View whose signature is `View() string`. Any assignment whose
// left-hand side is `recv.field` is flagged, with a pragmatic exemption
// for slice-cache patterns commonly used by click-zone caches:
//
//	recv.field = nil
//	recv.field = recv.field[:0]
//	recv.field = append(recv.field, …)
//
// Anything else — assigning a literal, a method call result, or a value
// that is also read elsewhere in View — is reported. Such mutations make
// View() effectively part of the state machine, which is exactly what
// Update() exists for.
//
// Per-line suppression is provided centrally by the runner: annotate the
// line with `//rubocop:disable Lint/TUIViewPurity` to opt out.
type TUIViewPurity struct {
	cop.Meta
}

// NewTUIViewPurity returns a fully configured TUIViewPurity cop.
func NewTUIViewPurity() *TUIViewPurity {
	return &TUIViewPurity{Meta: cop.Meta{
		CopName:     "Lint/TUIViewPurity",
		CopDesc:     "View() methods on TUI models must not mutate the receiver",
		CopSeverity: cop.Warning,
	}}
}

func (c *TUIViewPurity) Check(p *cop.Pass) {
	if !p.FileUnder("pkg/tui") {
		return
	}

	p.ForEachFunc(func(fn *ast.FuncDecl) {
		recv, ok := cop.Receiver(fn)
		if !ok || !recv.IsPointer || recv.Name == "" || !isViewMethod(fn) {
			return
		}
		c.checkBody(p, fn.Body, recv.Name)
	})
}

// checkBody walks fn body and reports an offense for every assignment to a
// receiver field that is not part of the slice-cache exemption set.
func (c *TUIViewPurity) checkBody(p *cop.Pass, body *ast.BlockStmt, recv string) {
	if body == nil {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Tok != token.ASSIGN {
			return true
		}
		for i, lhs := range assign.Lhs {
			x, field, ok := cop.MatchSelector(lhs)
			if !ok || x != recv {
				continue
			}
			if i < len(assign.Rhs) && isSliceCachePattern(assign.Rhs[i], recv, field) {
				continue
			}
			p.Report(assign,
				"View() must not mutate %s.%s; move the side effect to Update or compute it in a local variable"+
					" (or annotate the line with //rubocop:disable Lint/TUIViewPurity if it is an intentional click-zone cache)",
				recv, field)
		}
		return true
	})
}

// isViewMethod reports whether fn is exactly `func (...) View() string`.
// The cop intentionally ignores helpers that happen to be called View*
// because they are not part of the Bubble Tea contract.
func isViewMethod(fn *ast.FuncDecl) bool {
	if fn.Name.Name != "View" {
		return false
	}
	if fn.Type.Params != nil && len(fn.Type.Params.List) > 0 {
		return false
	}
	if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	id, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
	return ok && id.Name == "string"
}

// isSliceCachePattern reports whether rhs is one of the recognised
// "slice cache" idioms for the field recv.field:
//
//	nil
//	recv.field[:0]
//	append(recv.field, …)
//
// These are the shapes used by the click-zone caches that several TUI
// components populate during rendering.
func isSliceCachePattern(rhs ast.Expr, recv, field string) bool {
	if id, ok := rhs.(*ast.Ident); ok && id.Name == "nil" {
		return true
	}
	if slc, ok := rhs.(*ast.SliceExpr); ok && cop.IsSelector(slc.X, recv, field) {
		return true
	}
	if call, ok := rhs.(*ast.CallExpr); ok {
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "append" && len(call.Args) > 0 {
			if cop.IsSelector(call.Args[0], recv, field) {
				return true
			}
		}
	}
	return false
}
