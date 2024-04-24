package gotreload

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/got-reload/got-reload/pkg/extract"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

const (
	thisPackageName = "gotreload" // Should probably use reflection for this.

	// Used to mark variables, fields, etc as exported.
	exportPrefix = "GRL_"
	// Used to generate argument names.
	syntheticArgPrefix = "GRLarg_"
	// Used for stub function variable names.
	stubPrefix = "GRLf_"
	// Used for set functions.
	setPrefix = "GRLset_"
)

type (
	Rewriter struct {
		Config packages.Config
		Pkgs   []*packages.Package
		// Keys are PkgPath & setter name.  We use PkgPath as the key instead of
		// a *packages.Package because we need to be able to find this across
		// different instances of Rewriter, where pointer values will be
		// different, but package import paths, which are just strings, will be
		// the same.
		NewFunc map[string]map[string]*ast.FuncLit

		// Per-package supplemental information.  Used only in initial rewrite.
		Info map[*packages.Package]*Info
	}

	Info struct {
		Registrations []byte
	}
)

func (r *Rewriter) Load(paths ...string) error {
	// Make sure the mode has at least what the parser needs
	r.Config.Mode |= packages.NeedName |
		packages.NeedFiles |
		packages.NeedCompiledGoFiles |
		packages.NeedImports |
		packages.NeedDeps |
		packages.NeedTypes |
		packages.NeedSyntax |
		packages.NeedTypesInfo
	r.NewFunc = map[string]map[string]*ast.FuncLit{}
	r.Info = map[*packages.Package]*Info{}

	pkgs, err := packages.Load(&r.Config, paths...)
	if err != nil {
		return err
	}
	packages.PrintErrors(pkgs)
	r.Pkgs = pkgs

	return nil
}

// Rewrite rewrites the ASTs in r.Pkgs in place.  addPackage==true is used in
// reload mode, to add package prefixes to all mentioned variables.
func (r *Rewriter) Rewrite(addPackage bool) error {
	for _, pkg := range r.Pkgs {
		if pkg.TypesInfo == nil {
			log.Printf("Pkg %s: No TypesInfo\n", pkg.Name)
			continue
		}
		err := r.rewritePkg(pkg, addPackage)
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Rewriter) rewritePkg(pkg *packages.Package, addPackage bool) error {
	if pkg.Name == "main" {
		return fmt.Errorf("Cannot rewrite package \"main\"")
	}
	if pkg.Name == "" {
		return fmt.Errorf("Missing package name: %s %s", pkg.ID, pkg.PkgPath)
	}
	// log.Printf("Pkg: %#v", pkg)

	exported := map[types.Object]bool{}
	definedInThisPackage := map[types.Object]bool{}
	funcs := map[*ast.FuncDecl]string{}
	for ident, obj := range pkg.TypesInfo.Defs {
		if ident.Name == "_" || obj == nil {
			continue
		}
		// Look at struct fields and methods in interfaces.
		if obj.Parent() == nil {
			switch obj := obj.(type) {
			// Struct field names
			case *types.Var:
				if obj.IsField() {
					assureExported(exported, ident, obj)
				}
			// Method names in interfaces.
			case *types.Func:
				assureExported(exported, ident, obj)
				tagForTranslation(pkg, funcs, obj)
			default:
				return fmt.Errorf("Internal error: No parent: %#v\n", obj)
			}
			continue
		}
		if obj.Parent() != pkg.Types.Scope() {
			// Skip any item (variable, type, etc) not at package scope.
			continue
		}
		assureExported(exported, ident, obj)
		if obj, ok := obj.(*types.Func); ok {
			tagForTranslation(pkg, funcs, obj)
		}
		definedInThisPackage[obj] = true
	}

	r.stubTopLevelFuncs(pkg, funcs)

	// Find all the uses of all the stuff we exported, and export them
	// too.  If addPackage, tag idents defined in this package for adding the
	// package to.
	addPackageIdent := map[*ast.Ident]bool{}
	for ident, obj := range pkg.TypesInfo.Uses {
		if exported[obj] {
			ident.Name = exportPrefix + ident.Name
		}
		if addPackage && definedInThisPackage[obj] {
			// log.Printf("Add package to %s", ident.Name)
			addPackageIdent[ident] = true
		}
	}

	if addPackage {
		// Add a package prefix to all identifiers defined in this package
		// (noted above), aka "reload mode".
		for _, file := range pkg.Syntax {
			pre := func(c *astutil.Cursor) bool {
				switch n := c.Node().(type) {
				case *ast.Ident:
					if !addPackageIdent[n] {
						return true
					}
					// log.Printf("Adding package to %s", n.Name)
					c.Replace(newSelector(pkg.Name, n.Name))
				}
				return true
			}
			file = astutil.Apply(file, pre, nil).(*ast.File)
		}
	} else {
		// Generate symbol registrations, aka "rewrite mode".
		var setters []string
		for setter := range r.NewFunc[pkg.PkgPath] {
			setters = append(setters, setter)
		}
		registrationSource, err := extract.GenContent(pkg.Name, pkg.Name, pkg.PkgPath, pkg.Types, setters, exported)
		if err != nil {
			return fmt.Errorf("Failed generating symbol registration for %s at %s: %w", pkg.Name, pkg.PkgPath, err)
		}

		if registrationSource != nil {
			// log.Printf("generated grl_register.go: %s", string(registrationSource))
			r.Info[pkg] = &Info{Registrations: registrationSource}
		}
	}

	// b := bytes.Buffer{}
	// err = format.Node(&b, pkg.Fset, file)
	// if err != nil {
	// 	return fmt.Errorf("Error formatting updated file: %w", err)
	// }
	// log.Printf("Updated file: %s", b.String())
	return nil
}

// assureExported takes unexported identifiers, adds a prefix to export them,
// and marks the associated object for later use.
func assureExported(exported map[types.Object]bool, ident *ast.Ident, obj types.Object) {
	if obj.Exported() {
		return
	}
	ident.Name = exportPrefix + ident.Name
	exported[obj] = true
}

// tagForTranslation finds the function/method declaration obj is in, and tags
// it for translation.
func tagForTranslation(pkg *packages.Package, funcs map[*ast.FuncDecl]string, obj *types.Func) {
	// log.Printf("Func: %#v\n", obj)
	for _, file := range pkg.Syntax {
		filename := pkg.Fset.Position(file.Pos()).Filename
		objFileName := pkg.Fset.Position(obj.Pos()).Filename
		// log.Printf("Looking at %q vs. %q", filename, objFileName)
		if filename == objFileName {
			path, exact := astutil.PathEnclosingInterval(file, obj.Pos(), obj.Pos())
			// log.Printf("func %s: exact: %v, %#v\n", obj.Name(), exact, path)

			if path != nil && exact {
				var funcDecl *ast.FuncDecl
				var ok bool
				for _, item := range path {
					// position := pkg.Fset.Position(item.Pos())
					funcDecl, ok = item.(*ast.FuncDecl)
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
				log.Printf("Cannot find FuncDecl for %s\n", obj.FullName())
			} else {
				log.Printf("Cannot find exact path for %s\n", obj.Name())
			}
			return
		}
	}
	log.Printf("Cannot find file for %s\n", obj.Name())
}

// stubTopLevelFuncs finds all the top-level functions and methods and stubs
// them out.
func (r *Rewriter) stubTopLevelFuncs(pkg *packages.Package, funcs map[*ast.FuncDecl]string) {
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

					log.Printf("Translating %s\n", name)

					newVar, setFunc := rewriteFunc(pkg.PkgPath, name, n)
					if newVar != nil && setFunc != nil {
						// These are like a stack, so in the emitted source, the
						// current func will come first, then newVar, then setFunc.
						c.InsertAfter(setFunc)
						c.InsertAfter(newVar)

						// Track setFunc => newVar
						if r.NewFunc[pkg.PkgPath] == nil {
							r.NewFunc[pkg.PkgPath] = map[string]*ast.FuncLit{}
						}
						// log.Printf("Storing %s:%s", pkg.PkgPath, setFunc.Name.Name)
						r.NewFunc[pkg.PkgPath][setFunc.Name.Name] =
							newVar.Specs[0].(*ast.ValueSpec).Values[0].(*ast.FuncLit)
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
func rewriteFunc(pkgPath, name string, node *ast.FuncDecl) (*ast.GenDecl, *ast.FuncDecl) {
	// Don't rewrite generic functions, i.e., functions with type parameters
	if node.Type.TypeParams != nil {
		return nil, nil
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

	// Define the new body of the function/method to just call the stub.
	stubCall := &ast.CallExpr{
		Fun:      newIdent(stubPrefix + name),
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

	// Define the stub with the new arglist, and old body from the
	// function/method.
	newVar := &ast.GenDecl{
		Tok: token.VAR,
		Specs: []ast.Spec{
			&ast.ValueSpec{
				Names: []*ast.Ident{{Name: stubPrefix + name}},
				Values: []ast.Expr{
					&ast.FuncLit{
						Type: newVarType,
						Body: node.Body,
					}}}}}

	// Define the Set function
	setFunc := &ast.FuncDecl{
		Name: newIdent(setPrefix + name),
		Type: &ast.FuncType{
			Params: &ast.FieldList{
				List: []*ast.Field{{
					Names: []*ast.Ident{{Name: "f"}},
					Type:  newVarType,
				}}}},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.AssignStmt{
					Lhs: []ast.Expr{newIdent(stubPrefix + name)},
					Tok: token.ASSIGN,
					Rhs: []ast.Expr{newIdent("f")},
				}}}}

	// Replace the node's body with the new body in-place.
	node.Body = body
	return newVar, setFunc
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

func (r *Rewriter) FuncDef(pkgPath, setter string) (string, map[string]string, error) {
	for _, pkg := range r.Pkgs {
		if pkg.PkgPath != pkgPath {
			continue
		}
		funcAst := r.NewFunc[pkgPath][setter]

		// log.Printf("Setter %s ast\n", setter)
		// ast.Print(pkg.Fset, funcAst)

		// Find the file this function is in
		fName := pkg.Fset.Position(funcAst.Pos()).Filename
		file, _, err := r.LookupFile(fName)
		if err != nil {
			return "", nil, err
		}

		// Build a map of import names & paths that this file imports.
		imports := map[string]string{}
		usedImports := map[string]string{}
		for _, imp := range file.Imports {
			impPath := strings.Trim(imp.Path.Value, "\"")
			name := path.Base(impPath)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			imports[name] = impPath

			// Mark all dot-imports as used.  Because we won't know, do we?
			if name == "." {
				usedImports["."] = impPath
			}
		}
		// log.Printf("File %s imports %#v", fName, imports)

		// Traverse the function's AST and find all SelectorExprs that look like
		// package references.  FIXME: This will probably break if somebody
		// shadows a package name (sigh).
		pre := func(c *astutil.Cursor) bool {
			switch n := c.Node().(type) {
			case *ast.SelectorExpr:
				if x, ok := n.X.(*ast.Ident); ok {
					// log.Printf("Looking at selector %#v", x.Name)
					if impPath, ok := imports[x.Name]; ok {
						usedImports[x.Name] = impPath
					}
				}
			}
			return true
		}
		_ = astutil.Apply(funcAst, pre, nil)
		// log.Printf("Setter %s uses %v", setter, usedImports)

		b := &bytes.Buffer{}
		format.Node(b, pkg.Fset, r.NewFunc[pkgPath][setter])
		return b.String(), usedImports, nil
	}
	return "", nil, fmt.Errorf("No setter found for %s:%s", pkgPath, setter)
}
