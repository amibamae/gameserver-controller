package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestEveryActionIsDispatched keeps pilotActions and runWithSettings in step:
// each listed action must have its own branch, and each branch must be listed.
// The pilot once accepted handoff and register-ssh-key while its error message
// still named only the seven older actions.
func TestEveryActionIsDispatched(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	dispatched := dispatchedPilotActions(t, file)
	var listed []string
	for _, action := range pilotActions {
		listed = append(listed, action.name)
	}
	slices.Sort(dispatched)
	slices.Sort(listed)
	if !slices.Equal(dispatched, listed) {
		t.Fatalf("runWithSettings dispatches %v, pilotActions lists %v", dispatched, listed)
	}
}

func dispatchedPilotActions(t *testing.T, file *ast.File) []string {
	t.Helper()
	var dispatched []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runWithSettings" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch expr := node.(type) {
			case *ast.SwitchStmt:
				dispatched = appendSwitchActions(t, dispatched, expr)
			case *ast.BinaryExpr:
				// history is handled before the switch: if action == "history".
				if ident, ok := expr.X.(*ast.Ident); ok && ident.Name == "action" && expr.Op == token.EQL {
					dispatched = append(dispatched, stringLiteral(t, expr.Y))
				}
			}
			return true
		})
	}
	return dispatched
}

func appendSwitchActions(t *testing.T, actions []string, statement *ast.SwitchStmt) []string {
	t.Helper()
	tag, ok := statement.Tag.(*ast.Ident)
	if !ok || tag.Name != "action" {
		return actions
	}
	for _, clause := range statement.Body.List {
		for _, expr := range clause.(*ast.CaseClause).List {
			actions = append(actions, stringLiteral(t, expr))
		}
	}
	return actions
}

func stringLiteral(t *testing.T, expr ast.Expr) string {
	t.Helper()
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		t.Fatalf("action is compared with a non-literal %T", expr)
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestUnknownActionNamesEveryAction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runWithSettings([]string{"bogus"}, &stdout, &stderr, runtimeSettings{})
	if err == nil || !strings.HasPrefix(err.Error(), `unknown action "bogus"; expected `) {
		t.Fatalf("runWithSettings error = %v, want an unknown-action error", err)
	}
	for _, action := range pilotActions {
		if !strings.Contains(err.Error(), action.name) {
			t.Errorf("unknown-action error %q does not name %q", err, action.name)
		}
	}
	if !strings.HasSuffix(err.Error(), ", or "+pilotActions[len(pilotActions)-1].name) {
		t.Errorf("unknown-action error %q should end with \", or %s\"", err, pilotActions[len(pilotActions)-1].name)
	}
}

func TestUsagePrintsEveryAction(t *testing.T) {
	var out bytes.Buffer
	if err := usage(&out); err == nil {
		t.Fatal("usage returned no error")
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != len(pilotActions)+1 || lines[0] != "Supervised Minecraft server pilot" {
		t.Fatalf("usage printed %d lines, want a title and %d actions:\n%s", len(lines), len(pilotActions), out.String())
	}
	for i, action := range pilotActions {
		if lines[i+1] != "  "+action.usage || !strings.HasPrefix(action.usage, "gsc-pilot "+action.name) {
			t.Errorf("usage line %d = %q, want the %s usage", i+1, lines[i+1], action.name)
		}
	}
}
