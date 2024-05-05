package gotreload

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"log"
	"os"
	"path/filepath"
	"unicode"
	"unicode/utf8"

	"github.com/got-reload/got-reload/pkg/extract"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

const (
	thisPackageName = "gotreload" // Should probably use reflection for this.

	// Used to generate argument names.
	syntheticArgPrefix = "GRLarg_"
	// Used for stub function variable names.
	stubPrefix = "GRLfvar_"
	// Used for set functions.
	setPrefix = "GRLfset_"

	funcUAddrPrefix   = "GRLuaddr_"
	methodUAddrPrefix = "GRLmaddr_"

	utypePrefix = "GRLt_"
)

type (
	Rewriter struct {
		Config packages.Config
		Pkgs   []*packages.Package
		// Keys are PkgPath & stubVar name.  We use PkgPath as the key instead
		// of a *packages.Package because we need to be able to find this across
		// different instances of Rewriter, where pointer values will be
		// different, but package import paths, which are just strings, will be
		// the same.
		NewFunc map[string]map[string]*ast.FuncLit

		// Per-package supplemental information.  Used only in initial rewrite.
		Info map[*packages.Package]*Info

		funcs    map[*ast.FuncDecl]string
		stubVars map[string]bool
		// var name -> its type's name
		needsAccessor            map[string]string
		needsPublicType          map[string]string
		needsFieldAccessor       map[string]map[*types.Struct]extract.FieldAccessor // field name -> struct type -> stuff
		needsPublicMethodWrapper map[string]bool                                    // not used?
	}

	Info struct {
		Registrations []byte
	}
)

func NewRewriter() *Rewriter {
	r := Rewriter{
		// Make sure the mode has at least what the parser needs
		Config: packages.Config{
			Fset: token.NewFileSet(),
			Mode: packages.NeedName |
				packages.NeedFiles |
				packages.NeedCompiledGoFiles |
				packages.NeedImports |
				packages.NeedDeps |
				packages.NeedTypes |
				packages.NeedSyntax |
				packages.NeedTypesInfo,
			ParseFile: func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
				return parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
			},
		},
		NewFunc: map[string]map[string]*ast.FuncLit{},
		Info:    map[*packages.Package]*Info{},
	}

	return &r
}

func (r *Rewriter) Load(paths ...string) error {
	pkgs, err := packages.Load(&r.Config, paths...)
	if err != nil {
		return err
	}
	packages.PrintErrors(pkgs)
	r.Pkgs = pkgs

	return nil
}

type RewriteMode int

const (
	ModeInvalid RewriteMode = iota
	ModeRewrite
	ModeReload
)

// Rewrite rewrites the ASTs in r.Pkgs in place.  mode==ModeReload is used to
// add package prefixes to all mentioned variables, and make other changes.
func (r *Rewriter) Rewrite(mode RewriteMode) error {
	for _, pkg := range r.Pkgs {
		if pkg.TypesInfo == nil {
			log.Printf("Pkg %s: No TypesInfo\n", pkg.Name)
			continue
		}
		var err error
		switch mode {
		case ModeRewrite:
			err = r.rewritePkg(pkg)
		case ModeReload:
			err = r.reloadPkg(pkg)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// rewritePkg rewrites the syntax tree for all source files for a package
// we're filtering, and also generates and writes a "registration" file for
// said package.
func (r *Rewriter) rewritePkg(pkg *packages.Package) error {
	if pkg.Name == "main" {
		return fmt.Errorf("Cannot rewrite package \"main\"")
	}
	if pkg.Name == "" {
		return fmt.Errorf("Missing package name: %s %s", pkg.ID, pkg.PkgPath)
	}
	// log.Printf("Pkg: %#v", pkg)

	definedInThisPackage := map[types.Object]bool{}
	r.funcs = map[*ast.FuncDecl]string{}
	r.stubVars = map[string]bool{}
	// This tags unexported variables to generate accessor functions. We
	// *could* just generate new variables with the address of the unexported
	// variable, but we need to generate accessor methods for struct fields
	// later (see needsFieldAccessor), so we might as well just keep things
	// simple and do accessor functions here too.
	r.needsAccessor = map[string]string{}
	// This stores structs and their fields that need public accessor
	// functions. The method name needs to contain the type name because we
	// index them by name in the generated Exports map. So e.g. if struct S1
	// has field f1 and struct S2 has field f1, we need to be able to
	// distinguish them just by name, so e.g. GRLf_f1 would not be enough, they
	// need to be GRLf_S1f1 and GRLf_S2f1.
	r.needsFieldAccessor = map[string]map[*types.Struct]extract.FieldAccessor{}
	r.needsPublicMethodWrapper = map[string]bool{}
	// needsPublicFuncWrapper := map[string]*types.Signature{}
	// needsPublicFuncWrapper := map[string]string{} // ident name => stubPrefix + ident.Name
	r.needsPublicType = map[string]string{}
	imports := extract.NewImportTracker(pkg.Name, pkg.PkgPath)
	for ident, obj := range pkg.TypesInfo.Defs {
		if ident.Name == "_" || obj == nil {
			continue
		}
		// log.Printf("Def: %p: %#v, Obj: %p: %#v, Parent: %#v", ident, ident, obj, obj, obj.Parent())
		if typeName, ok := obj.(*types.TypeName); ok {
			// This doesn't work and may be wrong?
			// if typeName.Pkg().Path() != pkg.PkgPath {
			// 	continue
			// }
			// log.Printf("Def path: %s: %s", typeName.Name(), typeName.Pkg().Path())

			if named, ok := typeName.Type().(*types.Named); ok {
				if s, ok := named.Underlying().(*types.Struct); ok {
					for i := 0; i < s.NumFields(); i++ {
						field := s.Field(i)
						if field.Exported() {
							continue
						}
						fieldTypeName := types.TypeString(field.Type(), func(p *types.Package) string {
							return imports.GetAlias(p.Name(), p.Path())
						})
						fieldName := field.Name()
						methodName := methodUAddrPrefix + typeName.Name() + "_" + fieldName
						// log.Printf("Tagging struct %s.%s for an addr method: %s", typeName.Name(), fieldName, methodName)
						if r.needsFieldAccessor[fieldName] == nil {
							r.needsFieldAccessor[fieldName] = map[*types.Struct]extract.FieldAccessor{}
						}
						r.needsFieldAccessor[fieldName][s] = extract.FieldAccessor{
							Var:       field,
							RType:     typeName.Name(),
							AddrName:  methodName,
							FieldType: fieldTypeName,
						}
					}
				}
			}
		}

		// Look at struct fields and methods in interfaces.
		if obj.Parent() == nil {
			switch obj := obj.(type) {
			// Struct field names
			case *types.Var:

			// Method names in interfaces. Might be wrong and/or simpler to do
			// with Defs as for struct types, above.
			case *types.Func:
				if !obj.Exported() {
					// Probably not quite right? Need the type of the struct the
					// field is in, and the type of the field.
					r.needsPublicMethodWrapper[ident.Name] = true
				}
				tagForTranslation(pkg, r.funcs, obj)

			default:
				return fmt.Errorf("Internal error: No parent: %#v\n", obj)
			}
			continue
		}
		if obj.Parent() != pkg.Types.Scope() {
			// Skip any item (variable, type, etc) not at package scope.
			continue
		}
		if !obj.Exported() {
			switch obj := obj.(type) {
			case *types.Var:
				// log.Printf("%s: type: %#v", ident.Name, obj.Type())
				r.needsAccessor[ident.Name] = types.TypeString(obj.Type(), func(p *types.Package) string {
					return imports.GetAlias(p.Name(), p.Path())
				})
			case *types.TypeName:
				public := utypePrefix + ident.Name
				r.needsPublicType[ident.Name] = public
			case *types.Func:
				// needsPublicFuncWrapper[ident.Name] = obj.Type().(*types.Signature)
				// needsPublicFuncWrapper[ident.Name] = stubPrefix + ident.Name
			}
		}
		if obj, ok := obj.(*types.Func); ok {
			tagForTranslation(pkg, r.funcs, obj)
		}
		definedInThisPackage[obj] = true
	}

	r.stubTopLevelFuncs(pkg, r.funcs, ModeRewrite)

	// Generate symbol registrations
	// log.Printf("Looking for stubVars for pkg %s", pkg.PkgPath)
	for stubVar := range r.NewFunc[pkg.PkgPath] {
		// log.Printf("stubVar: %s", stubVar)
		r.stubVars[stubVar] = true
	}
	registrationSource, err := extract.GenContent(pkg.Name, pkg.PkgPath, pkg.Types,
		r.stubVars,
		r.needsAccessor, r.needsPublicType, r.needsFieldAccessor, imports)
	if err != nil {
		return fmt.Errorf("Failed generating symbol registration for %q at %s: %w", pkg.Name, pkg.PkgPath, err)
	}

	if registrationSource != nil {
		// log.Printf("generated grl_register.go: %s", string(registrationSource))
		r.Info[pkg] = &Info{Registrations: registrationSource}
	}

	// b := bytes.Buffer{}
	// err = format.Node(&b, pkg.Fset, file)
	// if err != nil {
	// 	return fmt.Errorf("Error formatting updated file: %w", err)
	// }
	// log.Printf("Updated file: %s", b.String())
	return nil
}

// reloadPkg rewrites the syntax tree for all source files for a package
// we're filtering, for "reload" mode.
func (r *Rewriter) reloadPkg(pkg *packages.Package) error {
	if pkg.Name == "main" {
		return fmt.Errorf("Cannot rewrite package \"main\"")
	}
	if pkg.Name == "" {
		return fmt.Errorf("Missing package name: %s %s", pkg.ID, pkg.PkgPath)
	}
	// log.Printf("Pkg: %#v", pkg)

	for _, file := range pkg.Syntax {
		// b := &bytes.Buffer{}
		// err := format.Node(b, pkg.Fset, file)
		// if err != nil {
		// 	panic(err)
		// }
		// log.Printf("reload: before\n%s", b.String())

		replace := map[ast.Node]ast.Node{}
		pre := func(c *astutil.Cursor) bool {
			switch n := c.Node().(type) {
			case *ast.Ident:
				if _, ok := r.needsAccessor[n.Name]; ok {
					switch parent := c.Parent().(type) {
					case *ast.ValueSpec:
					case *ast.AssignStmt, *ast.BinaryExpr, *ast.CallExpr, *ast.StarExpr:
						// Transform <var1> into *funcUAddrPrefix+<var1> in
						// assignments, expressions, and function call arguments.
						c.Replace(
							&ast.StarExpr{
								X: &ast.CallExpr{
									Fun: &ast.Ident{
										Name: funcUAddrPrefix + n.Name,
									},
								},
							})

					case *ast.UnaryExpr:
						// Similar to the above, except without the StarExpr to
						// deref the pointer returned by the function.
						replace[parent] =
							&ast.CallExpr{
								Fun: &ast.Ident{
									Name: funcUAddrPrefix + n.Name,
								},
							}

					case *ast.SelectorExpr:
						replace[parent] =
							&ast.SelectorExpr{
								X: &ast.CallExpr{
									Fun: &ast.Ident{
										Name: funcUAddrPrefix + n.Name,
									},
								},
								Sel: &ast.Ident{
									Name: parent.Sel.Name,
								},
							}

					case *ast.Field:
						// ignore

					default:
						buf := bytes.Buffer{}
						ast.Fprint(&buf, pkg.Fset, parent, ast.NotNilFilter)
						log.Printf("path %s:\n%s", n.Name, buf.String())
						panic(fmt.Sprintf("Unknown node type in expression involving %s", n.Name))
					}
				}
				if _, ok := r.needsFieldAccessor[n.Name]; ok {
					pos := pkg.Fset.Position(n.Pos())
					// log.Printf("%s: needsFieldAccessor? %s, %#v; parent: %#v; Uses: %#v", pos, n.Name, n, par, pkg.TypesInfo.Uses[n])
					if se, ok := c.Parent().(*ast.SelectorExpr); ok {
						if selIdent, ok := se.X.(*ast.Ident); ok {
							if named, ok := pkg.TypesInfo.Uses[selIdent].Type().(*types.Named); ok {
								if s, ok := named.Underlying().(*types.Struct); ok {
									// log.Printf("%s: setting replacement: %s, %#v; parent: %#v; Uses: %#v", pos, n.Name, n, c.Parent(), pkg.TypesInfo.Uses[n])
									replace[c.Parent()] =
										&ast.StarExpr{
											X: &ast.CallExpr{
												Fun: &ast.SelectorExpr{
													X: &ast.Ident{
														Name: selIdent.Name,
													},
													Sel: &ast.Ident{
														Name: r.needsFieldAccessor[n.Name][s].AddrName,
													},
												}}}
								} else {
									log.Printf("%s: s not found for %s; underlying: %#v", pos, n.Name, named.Underlying())
								}
							} else {
								log.Printf("%s: types.Named not found for %s", pos, n.Name)
							}
						} else {
							log.Printf("%s: SelectorExpr not found for %s", pos, n.Name)
						}
					}
				}
				if publicType, ok := r.needsPublicType[n.Name]; ok {
					switch c.Parent().(type) {
					case *ast.ValueSpec:
						// log.Printf("Setting public type: %s to %s.%s", n.Name, pkg.Name, publicType)
						c.Replace(&ast.Ident{Name: publicType})
					}
				}
			}
			return true
		}
		post := func(c *astutil.Cursor) bool {
			if r, ok := replace[c.Node()]; ok {
				c.Replace(r)
			}
			return true
		}
		file = astutil.Apply(file, pre, post).(*ast.File)

		// b = &bytes.Buffer{}
		// err = format.Node(b, pkg.Fset, file)
		// if err != nil {
		// 	panic(err)
		// }
		// log.Printf("reload: after\n%s", b.String())
	}

	// b := bytes.Buffer{}
	// err = format.Node(&b, pkg.Fset, file)
	// if err != nil {
	// 	return fmt.Errorf("Error formatting updated file: %w", err)
	// }
	// log.Printf("Updated file: %s", b.String())
	return nil
}

// not used
func makeExported(s string) string {
	r, sz := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[sz:]
}

// assureExported takes unexported identifiers, adds a prefix to export them,
// and marks the associated object for later use.
// func assureExported(exported map[types.Object]bool, ident *ast.Ident, obj types.Object) {
// 	if obj.Exported() {
// 		return
// 	}
// 	ident.Name = exportPrefix + ident.Name
// 	exported[obj] = true
// }

// tagForTranslation finds the function/method declaration obj is in, and tags
// it for translation.
func tagForTranslation(pkg *packages.Package, funcs map[*ast.FuncDecl]string, obj *types.Func) {
	// log.Printf("Func: %#v\n", obj)
	file := findFile(pkg, obj.Pos())
	if file == nil {
		log.Printf("Warning: Cannot find file for %s\n", obj.Name())
		return
	}

	astNodePath, exact := astutil.PathEnclosingInterval(file, obj.Pos(), obj.Pos())
	// log.Printf("func %s: exact: %v, %#v\n", obj.Name(), exact, path)

	if astNodePath == nil || !exact {
		log.Printf("Warning: Cannot find exact path for %s\n", obj.Name())
		return
	}

	var funcDecl *ast.FuncDecl
	var ok bool
	for _, node := range astNodePath {
		// position := pkg.Fset.Position(item.Pos())
		funcDecl, ok = node.(*ast.FuncDecl)
		if ok {
			// log.Printf("func %s: marking for translation\n", obj.Name())
			funcs[funcDecl] = obj.Name()
			// log.Printf("Tagging %s for translation", obj.Name())
			// log.Printf("Found FuncDecl for %s: %s, %d:%d\n",
			// 	obj.Name(), filename, position.Line, position.Column)
			return
		}
		// log.Printf("Did not find FuncDecl for %s: %s, %d:%d\n",
		// 	obj.Name(), filename, position.Line, position.Column)
	}
	// Not (necessarily) a fatal error?
	log.Printf("Warning: Cannot find FuncDecl for %s\n", obj.FullName())
}

func findFile(pkg *packages.Package, pos token.Pos) *ast.File {
	objFileName := pkg.Fset.Position(pos).Filename
	for _, file := range pkg.Syntax {
		if objFileName == pkg.Fset.Position(file.Pos()).Filename {
			return file
		}
	}
	return nil
}

// stubTopLevelFuncs finds all the top-level functions and methods and stubs
// them out. It also saves a pointer to the syntax tree of the function
// literal, for later use.
func (r *Rewriter) stubTopLevelFuncs(pkg *packages.Package, funcs map[*ast.FuncDecl]string, mode RewriteMode) {
	for _, file := range pkg.Syntax {
		pre := func(c *astutil.Cursor) bool {
			switch n := c.Node().(type) {
			case *ast.FuncDecl:
				if name, ok := funcs[n]; ok {
					// Skip all init() functions.  (We don't need to skip
					// main.main() because rewriting "main" is an error higher up.)
					if name == "init" {
						return false
					}

					// log.Printf("Translating %s\n", name)

					stubName, newVar, funcLit := rewriteFunc(pkg.PkgPath, name, n)
					if newVar != nil {
						c.InsertAfter(newVar)

						// Track stubVar => new function
						if r.NewFunc[pkg.PkgPath] == nil {
							r.NewFunc[pkg.PkgPath] = map[string]*ast.FuncLit{}
						}
						// log.Printf("Storing %s: %s", pkg.PkgPath, stubPrefix+name)
						r.NewFunc[pkg.PkgPath][stubName] = funcLit
					}
				}
			}
			return true
		}

		// Result ignored because we do not replace the whole file.
		_ = astutil.Apply(file, pre, nil)
	}
}

// Print prints the rewritten files to a tree rooted in the given path.
//
// Not currently used ... but does look handy?
func (r *Rewriter) Print(root string) error {
	for _, pkg := range r.Pkgs {
		for i, file := range pkg.CompiledGoFiles {
			// Print file.  Not sure how to map from file to the appropriate item
			// in pkg.Syntax.  Maybe we'll get lucky and they're in the same
			// order?
			//
			// This example code assumes that.

			buf := bytes.Buffer{}
			format.Node(&buf, pkg.Fset, pkg.Syntax[i])
			// FIXME: joining root and file this way may be wrong.  Not sure
			// what's going to be in file at this point.  I think they're
			// actually absolute paths, so this is definitely wrong.
			os.WriteFile(filepath.Join(root, file), buf.Bytes(), 0600)
		}
	}
	return nil
}

// Updates node in place, and returns newVar and setFunc to be added to the
// AST after node.
//
// We're doing AST generation so things get a little Lisp-y.
func rewriteFunc(pkgPath, name string, node *ast.FuncDecl) (string, *ast.GenDecl, *ast.FuncLit) {
	// Don't rewrite generic functions, i.e., functions with type parameters
	if node.Type.TypeParams != nil {
		return "", nil, nil
	}

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
			receiverName = fmt.Sprintf("%s%d", syntheticArgPrefix, n)
			n++
			node.Recv.List[0].Names = []*ast.Ident{{Name: receiverName}}
		} else {
			receiverName = node.Recv.List[0].Names[0].Name
		}

		// Add receiver name to the front of the function call arglist
		newArgs = append(newArgs, newIdent(receiverName))

		// prepend the receiver & type to the new function type
		newVarType.Params.List = append(
			[]*ast.Field{{
				Names: []*ast.Ident{{Name: receiverName}},
				Type:  node.Recv.List[0].Type,
			}},
			newVarType.Params.List...)

		// Prepend the receiver type to the stub name.
		switch t := node.Recv.List[0].Type.(type) {
		case *ast.Ident:
			name = t.Name + "_" + name
		case *ast.StarExpr:
			name = t.X.(*ast.Ident).Name + "_" + name
		}
	}

	// See if we need to add '...' to the end of the stub call.
	var ellipsisPos token.Pos
	if l := len(node.Type.Params.List); l > 0 {
		if _, needsEllipsis := node.Type.Params.List[l-1].Type.(*ast.Ellipsis); needsEllipsis {
			// Exact value doesn't matter (so far as I can tell), just needs to
			// be non-zero.
			ellipsisPos = 1
		}
	}

	// Copy all formal arguments into the arglist of the function call
	for i, argField := range node.Type.Params.List {
		if len(argField.Names) == 0 {
			newName := fmt.Sprintf("%s%d", syntheticArgPrefix, n)
			n++
			ident := newIdent(newName)
			argField.Names = []*ast.Ident{ident}
			newArgs = append(newArgs, ident)

			// Update newVarType too
			newVarType.Params.List[recvrVarOffset+i].Names = argField.Names
		} else {
			for _, argName := range argField.Names {
				newArgs = append(newArgs, newIdent(argName.Name))
			}
		}
	}
	// log.Printf("rewriteFunc: %s: newArgs: %d, %v", name, len(newArgs), newArgs)

	name = stubPrefix + name
	// Define the new body of the function/method to just call the stub.
	stubCall := &ast.CallExpr{
		Fun:      newIdent(name),
		Args:     newArgs,
		Ellipsis: ellipsisPos,
	}
	var body *ast.BlockStmt
	if node.Type.Results == nil {
		// If the function has no return type, then just call the stub.
		body = &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ExprStmt{
					X: stubCall,
				}}}
	} else {
		// Add a "return" statement to the stub call.
		body = &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.ReturnStmt{
					Results: []ast.Expr{
						stubCall,
					}}}}
	}

	funcLit := &ast.FuncLit{
		Type: newVarType,
		Body: node.Body,
	}

	// Define the stub with the new arglist, and old body from the
	// function/method.
	newVar := &ast.GenDecl{
		Tok: token.VAR,
		Specs: []ast.Spec{
			&ast.ValueSpec{
				Names:  []*ast.Ident{{Name: name}},
				Values: []ast.Expr{funcLit}}}}

	// Replace the node's body with the new body in-place.
	node.Body = body
	return name, newVar, funcLit
}

func newSelector(x, sel string) *ast.SelectorExpr {
	return &ast.SelectorExpr{X: newIdent(x), Sel: newIdent(sel)}
}

func newIdent(name string) *ast.Ident {
	return &ast.Ident{Name: name}
}

func newStringLit(s string) *ast.BasicLit {
	return &ast.BasicLit{Kind: token.STRING, Value: fmt.Sprintf("%q", s)}
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

func (r *Rewriter) LookupFile(targetFileName string) (*ast.File, *token.FileSet, error) {
	// log.Printf("LookupFile looking for %s", targetFileName)
	for _, pkg := range r.Pkgs {
		// log.Printf("LookupFile considering package %s", pkg.Name)
		for _, file := range pkg.Syntax {
			name := pkg.Fset.Position(file.Pos()).Filename
			// log.Printf("LookupFile considering file %s", name)
			if targetFileName == name {
				return file, pkg.Fset, nil
			}
		}
	}
	return nil, nil, fmt.Errorf("File %s not found in parsed files", targetFileName)
}

func (r *Rewriter) FuncDef(pkgPath, stubVar string) (string, error) {
	for _, pkg := range r.Pkgs {
		if pkg.PkgPath != pkgPath {
			continue
		}
		b := &bytes.Buffer{}
		// log.Printf("NewFunc[%s][%s]", pkgPath, stubVar)
		format.Node(b, pkg.Fset, r.NewFunc[pkgPath][stubVar])
		return b.String(), nil
	}
	return "", fmt.Errorf("No stubVar found for %s:%s", pkgPath, stubVar)
}
