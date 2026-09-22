package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strconv"
	"testing"
)

// routePatternsFromMain returns the patterns registered on each ServeMux in
// main.go, keyed by the variable the mux was assigned to.
//
// Read from the source rather than from a running server because the route table
// is built inline in main() with three dozen dependencies, and the property under
// test is a property of the pattern *strings*, not of the handlers behind them.
func routePatternsFromMain(t *testing.T) map[string][]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	byMux := map[string][]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Handle" {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			// A pattern that is not a literal is invisible to this test, and it
			// fails loudly rather than pretending it was checked.
			t.Fatalf("route on %s is registered with a non-literal pattern; this test cannot see it", receiver.Name)
		}
		pattern, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatalf("unquote pattern %s: %v", literal.Value, err)
		}
		byMux[receiver.Name] = append(byMux[receiver.Name], pattern)
		return true
	})

	if len(byMux) == 0 {
		t.Fatal("found no ServeMux registrations in main.go; the extraction is broken, so this test would pass vacuously")
	}
	total := 0
	for _, patterns := range byMux {
		total += len(patterns)
	}
	// Guards against the same vacuous pass if the parsing silently stops
	// matching after a refactor.
	if total < 40 {
		t.Fatalf("only found %d route patterns in main.go, expected the full table", total)
	}
	return byMux
}

// TestRoutePatternsDoNotShadowEachOther is the regression test for a defect that
// took the API down at startup: a new route was registered at
// "/v1/users/invites/{inviteID}" alongside the pre-existing
// "/v1/users/{userID}/external-identities", and neither is more specific than
// the other. Go's ServeMux panics on that, in main(), before the server ever
// listens -- the container crash-looped with
// "panic: pattern ... conflicts with pattern ...".
//
// Nothing else in the suite could see it: every handler test builds its own
// httptest server around a single handler, so the route table was only ever
// exercised by the deployed binary. This test registers the real patterns on a
// fresh mux, which is the same conflict check the runtime performs.
//
// It is a source-level tripwire, not a behavioural test, and its blind spot is
// patterns registered anywhere other than a literal `mux.Handle("...", ...)`
// call in main.go -- a helper function that registers routes would not be seen.
// The non-literal case fails the test explicitly rather than slipping through.
func TestRoutePatternsDoNotShadowEachOther(t *testing.T) {
	byMux := routePatternsFromMain(t)

	for name, patterns := range byMux {
		t.Run(name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, pattern := range patterns {
				if seen[pattern] {
					t.Errorf("%s registers %q twice; ServeMux panics on a duplicate registration", name, pattern)
					continue
				}
				seen[pattern] = true
			}

			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Errorf("%s would refuse to start: %v", name, recovered)
					}
				}()
				mux := http.NewServeMux()
				for _, pattern := range patterns {
					mux.Handle(pattern, http.NotFoundHandler())
				}
			}()
		})
	}
}
