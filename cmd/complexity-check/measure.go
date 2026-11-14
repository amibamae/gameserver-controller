package main

import (
	"go/ast"
	"go/token"
	"strings"
)

// allowMarker in a function's doc comment exempts it from the limits. The
// text after the marker must say why, as CLAUDE.md requires.
const allowMarker = "complexity-check:allow"

// funcMetrics are the measurements of one function or method.
type funcMetrics struct {
	Name       string
	StartLine  int
	EndLine    int
	Complexity int
	Nesting    int
	BoolOps    int
	BoolLine   int
	Allowed    bool
}

// measureFile measures every function declared in file.
func measureFile(fset *token.FileSet, file *ast.File) []funcMetrics {
	var out []funcMetrics
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		out = append(out, measureFunc(fset, fn))
	}
	return out
}

func measureFunc(fset *token.FileSet, fn *ast.FuncDecl) funcMetrics {
	m := funcMetrics{
		Name:       funcName(fn),
		StartLine:  fset.Position(fn.Pos()).Line,
		EndLine:    fset.Position(fn.End()).Line,
		Complexity: cyclomatic(fn.Body),
		Nesting:    maxNesting(fn.Body),
		Allowed:    hasComplexityException(fn.Doc),
	}
	m.BoolOps, m.BoolLine = largestCondition(fset, fn.Body)
	return m
}

func hasComplexityException(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, line := range strings.Split(doc.Text(), "\n") {
		marker, reason, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && marker == allowMarker && strings.TrimSpace(reason) != "" {
			return true
		}
	}
	return false
}

// funcName is "Name" for a function and "Type.Name" for a method.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return receiverType(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func receiverType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverType(t.X)
	case *ast.IndexExpr:
		return receiverType(t.X)
	case *ast.IndexListExpr:
		return receiverType(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

// cyclomatic counts 1 plus every decision point: if, for and range loops,
// non-default switch and select cases, and each && or ||. Function literals
// count toward the function that contains them, as gocyclo does.
func cyclomatic(body *ast.BlockStmt) int {
	count := 1
	ast.Inspect(body, func(node ast.Node) bool {
		count += decisionPoints(node)
		return true
	})
	return count
}

func decisionPoints(node ast.Node) int {
	switch n := node.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
		return 1
	case *ast.CaseClause:
		return boolToInt(n.List != nil)
	case *ast.CommClause:
		return boolToInt(n.Comm != nil)
	case *ast.BinaryExpr:
		return boolToInt(isLogical(n.Op))
	}
	return 0
}

func isLogical(op token.Token) bool { return op == token.LAND || op == token.LOR }

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
