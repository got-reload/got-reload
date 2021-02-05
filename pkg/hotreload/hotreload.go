package hotreload

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ast/astutil"
)

func Rewrite(node ast.Node) ast.Node {
	// This (obviously) only "knows" things we've seen in this file, and even
	// then, only if they have a valid Obj pointer, which not everything does.
	knownObjects := map[*ast.Object]string{}

	// This depth thing, both how it's calculated, and its meaning within a
	// parse tree, is kind of a guess.
	depth := 0

	var pkg string

	// Scan all type definitions and variable declarations
	//
	// TODO: structs with embedded types with no names might be a problem?
	pre := func(c *astutil.Cursor) bool {
		depth++
		switch n := c.Node().(type) {
		case *ast.File:
			pkg = n.Name.Name
		case *ast.TypeSpec:
			yyExport(knownObjects, &n.Name.Name, n.Name.Obj)
		case *ast.StructType:
			for _, field := range n.Fields.List {
				for _, ident := range field.Names {
					yyExport(knownObjects, &ident.Name, ident.Obj)
				}
			}
		case *ast.ValueSpec:
			// Only look at variable *names* at the top level.  This is at best a
			// guess and may work only with my test package.
			if depth <= 3 {
				for _, ident := range n.Names {
					// fmt.Printf("%d: var %s\n", depth, ident.Name)
					yyExport(knownObjects, &ident.Name, ident.Obj)
				}
			}
			// Have to look at variable *types* at all levels.
			switch n := n.Type.(type) {
			case *ast.Ident:
				// Make sure the type name isn't "int" or other predeclared types.
				//
				// Not sure what to do yet of somebody redefines "int" as some
				// other type.  It might "just work"?
				if o := types.Universe.Lookup(n.Name); o == nil {
					yyExport(knownObjects, &n.Name, n.Obj)
				}
			}
		case *ast.InterfaceType:
			for _, method := range n.Methods.List {
				yyExport(knownObjects, &method.Names[0].Name, method.Names[0].Obj)
			}
		case *ast.FuncDecl:
			origName := n.Name.Name
			if origName == "init" ||
				(pkg == "main" && origName == "main") {
				return false
			}
			yyExport(knownObjects, &n.Name.Name, n.Name.Obj)

			// Inserted nodes are skipped during traversal, but we can do this
			// here anyway because the later bit where we find all references to
			// known objects is in a different Apply call, which won't care that
			// they were inserted.
			newVar, setFunc := rewriteFunc(origName, n)
			c.InsertAfter(setFunc)
			c.InsertAfter(newVar)
		}
		return true
	}
	post := func(*astutil.Cursor) bool {
		depth--
		return true
	}
	node = astutil.Apply(node, pre, post)

	// Find all references to all known objects and replace them
	pre = func(c *astutil.Cursor) bool {
		switch n := c.Node().(type) {
		case *ast.Ident:
			if n.Obj == nil {
				// if o := types.Universe.Lookup(n.Name); o == nil {
				// 	// This is also a bit of a guess.
				// 	yyExport(knownObjects, &n.Name, nil)
				// }
			} else if newName, ok := knownObjects[n.Obj]; ok {
				n.Name = newName
			}
		// FIXME: This is a bit of a guess.  And probably wrong.  The *compiler*
		// could probably figure out what the type of n.X is.  (X is the bit
		// before the dot in the expression `someVar.someField` or
		// `someVar.someMethod` or
		// `some[0].complicated().expression.someField`).  Not sure that the
		// *parser* can, with the information it has at its disposal.  Maybe?
		case *ast.SelectorExpr:
			yyExport(knownObjects, &n.Sel.Name, nil)
		}
		return true
	}

	return astutil.Apply(node, pre, nil)
}

func yyExport(knownObjects map[*ast.Object]string, name *string, o *ast.Object) {
	if !ast.IsExported(*name) {
		*name = "YY_" + *name
		if _, ok := knownObjects[o]; o != nil && !ok {
			knownObjects[o] = *name
			// o.Name = "YY_" + o.Name
		}
	}
}

// Updates node in place, and returns newVar and setFunc
func rewriteFunc(name string, node *ast.FuncDecl) (*ast.GenDecl, *ast.FuncDecl) {
	newVarType := copyFuncType(node.Type)

	var newArgs []ast.Expr

	n := 0
	recvrVarOffset := 0

	// Process the receiver for a method definition
	if node.Recv != nil {
		// Note that we have a receiver
		recvrVarOffset++

		var receiverName string
		if len(node.Recv.List[0].Names) == 0 {
			// If the function has no receiver name, we have to generate one.
			receiverName = fmt.Sprintf("YYarg_%d", n)
			n++
			node.Recv.List[0].Names = []*ast.Ident{{Name: receiverName}}
		} else {
			receiverName = node.Recv.List[0].Names[0].Name
		}

		// Add receiver name to the front of the function call arglist
		newArgs = append(newArgs, &ast.Ident{Name: receiverName})

		// prepend the receiver & type to the new function type
		newVarType.Params.List = append(
			[]*ast.Field{
				{
					Names: []*ast.Ident{
						{
							Name: receiverName,
						},
					},
					Type: node.Recv.List[0].Type,
				},
			},
			newVarType.Params.List...)

		// Prepend the receiver type to the stub name.
		switch t := node.Recv.List[0].Type.(type) {
		case *ast.Ident:
			name = t.Name + "_" + name
		case *ast.StarExpr:
			name = t.X.(*ast.Ident).Name + "_" + name
		}
	}

	// Copy all formal arguments into the arglist of the function call
	for i, argField := range node.Type.Params.List {
		if len(argField.Names) == 0 {
			newName := fmt.Sprintf("YYarg_%d", n)
			n++
			newIdent := &ast.Ident{Name: newName}
			argField.Names = []*ast.Ident{newIdent}
			newArgs = append(newArgs, newIdent)

			// Update newVarType too
			newVarType.Params.List[recvrVarOffset+i].Names = argField.Names
		} else {
			for _, argName := range argField.Names {
				newArgs = append(newArgs, &ast.Ident{Name: argName.Name})
			}
		}
	}

	// Define the new body of the function/method to just call the stub.
	stubCall := &ast.CallExpr{
		Fun: &ast.Ident{
			Name: "YYf_" + name,
		},
		Args: newArgs,
	}
	var body *ast.BlockStmt
	if node.Type.Results == nil {
		// If the function has no return type, then just call the stub.
		body = &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: stubCall,
				},
			},
		}
	} else {
		// Add a "return" statement to the stub call.
		body = &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ReturnStmt{
					Results: []ast.Expr{
						stubCall,
					},
				},
			},
		}
	}

	// Define the stub with the new arglist, and old body from the
	// function/method.
	newVar := &ast.GenDecl{
		Tok: token.VAR,
		Specs: []ast.Spec{
			&ast.ValueSpec{
				Names: []*ast.Ident{
					{
						Name: "YYf_" + name,
					},
				},
				Values: []ast.Expr{
					&ast.FuncLit{
						Type: newVarType,
						Body: node.Body,
					},
				},
			},
		},
	}

	// Define the Set function
	setFunc := &ast.FuncDecl{
		Name: &ast.Ident{
			Name: "YYSet_" + name,
		},
		Type: &ast.FuncType{
			Params: &ast.FieldList{
				List: []*ast.Field{
					{
						Names: []*ast.Ident{
							{
								Name: "f",
							},
						},
						Type: newVarType,
					},
				},
			},
		},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.AssignStmt{
					Lhs: []ast.Expr{
						&ast.Ident{
							Name: "YYf_" + name,
						},
					},
					Tok: token.ASSIGN,
					Rhs: []ast.Expr{
						&ast.Ident{
							Name: "f",
						},
					},
				},
			},
		},
	}

	// Replace the node's body with the new body in-place.
	node.Body = body
	return newVar, setFunc
}

func copyFuncType(t *ast.FuncType) *ast.FuncType {
	t2 := *t
	params := *(t.Params)
	t2.Params = &params
	t2.Params.List = make([]*ast.Field, len(t.Params.List))
	for i, field := range t.Params.List {
		nField := *field
		nField.Names = make([]*ast.Ident, len(field.Names))
		for i, ident := range field.Names {
			nIdent := *ident
			nField.Names[i] = &nIdent
		}
		t2.Params.List[i] = &nField
	}
	return &t2
}
