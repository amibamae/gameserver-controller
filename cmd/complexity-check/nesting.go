package main

import (
	"go/ast"
	"go/token"
)

// maxNesting is the deepest chain of nested control statements (if, for,
// range, switch, type switch, select) in body. An else-if is a sibling of its
// if, not a level deeper.
func maxNesting(body *ast.BlockStmt) int {
	return blockDepth(body.List, 0)
}

func blockDepth(stmts []ast.Stmt, depth int) int {
	deepest := depth
	for _, stmt := range stmts {
		deepest = max(deepest, stmtDepth(stmt, depth))
	}
	return deepest
}

func stmtDepth(stmt ast.Stmt, depth int) int {
	switch s := stmt.(type) {
	case *ast.IfStmt:
		return ifDepth(s, depth)
	case *ast.ForStmt:
		return blockDepth(s.Body.List, depth+1)
	case *ast.RangeStmt:
		return blockDepth(s.Body.List, depth+1)
	case *ast.SwitchStmt:
		return clausesDepth(s.Body, depth+1)
	case *ast.TypeSwitchStmt:
		return clausesDepth(s.Body, depth+1)
	case *ast.SelectStmt:
		return clausesDepth(s.Body, depth+1)
	case *ast.BlockStmt:
		return blockDepth(s.List, depth)
	case *ast.LabeledStmt:
		return stmtDepth(s.Stmt, depth)
	}
	return depth
}

// ifDepth measures an if and its else branch; an else-if chain stays at the
// depth of the first if.
func ifDepth(s *ast.IfStmt, depth int) int {
	deepest := blockDepth(s.Body.List, depth+1)
	switch e := s.Else.(type) {
	case *ast.IfStmt:
		deepest = max(deepest, ifDepth(e, depth))
	case *ast.BlockStmt:
		deepest = max(deepest, blockDepth(e.List, depth+1))
	}
	return deepest
}

func clausesDepth(body *ast.BlockStmt, depth int) int {
	deepest := depth
	for _, clause := range body.List {
		switch c := clause.(type) {
		case *ast.CaseClause:
			deepest = max(deepest, blockDepth(c.Body, depth))
		case *ast.CommClause:
			deepest = max(deepest, blockDepth(c.Body, depth))
		}
	}
	return deepest
}

// largestCondition returns the most && and || operators in any single if or
// for condition, return value, or assigned value in body, and its line.
func largestCondition(fset *token.FileSet, body *ast.BlockStmt) (int, int) {
	most, line := 0, 0
	ast.Inspect(body, func(node ast.Node) bool {
		for _, expr := range conditionExprs(node) {
			if count := logicalOperators(expr); count > most {
				most, line = count, fset.Position(expr.Pos()).Line
			}
		}
		return true
	})
	return most, line
}

func conditionExprs(node ast.Node) []ast.Expr {
	switch n := node.(type) {
	case *ast.IfStmt:
		return []ast.Expr{n.Cond}
	case *ast.ForStmt:
		if n.Cond != nil {
			return []ast.Expr{n.Cond}
		}
	case *ast.ReturnStmt:
		return n.Results
	case *ast.AssignStmt:
		return n.Rhs
	}
	return nil
}

// logicalOperators counts && and || in expr, not looking inside function
// literals, which are measured as their own conditions.
func logicalOperators(expr ast.Expr) int {
	count := 0
	ast.Inspect(expr, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BinaryExpr:
			count += boolToInt(isLogical(n.Op))
		}
		return true
	})
	return count
}
