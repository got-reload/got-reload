package hotreload

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ast/astutil"
)

func Parse(fileName string, src interface{}) (ast.Node, error) {
	fset := token.NewFileSet()
	return parser.ParseFile(fset, fileName, src, parser.ParseComments)
}

func Rewrite(node ast.Node) ast.Node {
	knownObjects := map[*ast.Object]string{}

	// This depth thing, both how it's calculated, and its meaning within a
	// parse tree, is kind of a guess.
	depth := 0

	// Scan all type definitions and variable declarations
	pre := func(c *astutil.Cursor) bool {
		depth++
		switch n := c.Node().(type) {
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
			yyExport(knownObjects, &n.Name.Name, n.Name.Obj)
			newBody, newVar, setFunc := rewriteFunc(origName, n)
			n.Body = newBody
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
			if newName, ok := knownObjects[n.Obj]; ok {
				n.Name = newName
			}
		// FIXME: This is a bit of a guess.  I guess?  The *compiler* could
		// probably figure out what the type of n.X is.  (X is the bit before
		// the dot in the expression `someVar.someField` or `someVar.someMethod`
		// or `some[0].complicated().expression.someField`).  Not sure that the
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

func rewriteFunc(name string, node *ast.FuncDecl) (*ast.BlockStmt, *ast.GenDecl, *ast.FuncDecl) {
	body := &ast.BlockStmt{
		List: []ast.Stmt{
			&ast.ReturnStmt{
				Results: []ast.Expr{
					&ast.CallExpr{
						Fun: &ast.Ident{
							Name: "YYf_" + name,
							// args??
						},
					},
				},
			},
		},
	}

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
						Type: node.Type,
						Body: node.Body,
					},
				},
			},
		},
	}

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
						Type: node.Type,
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

	return body, newVar, setFunc
}
