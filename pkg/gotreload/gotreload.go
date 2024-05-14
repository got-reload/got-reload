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
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/got-reload/got-reload/pkg/extract"
	"github.com/got-reload/got-reload/pkg/util"
	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/go/packages"
)

const (
	thisPackageName = "gotreload" // Should probably use reflection for this.

	// Used to generate argument names.
	syntheticArgPrefix = "GRLarg_"
	syntheticReceiver  = "GRLrecvr"
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
		OutputDir string
		Pwd       string
		Config    packages.Config
		Pkgs      []*packages.Package
		// Keys are PkgPath & stubVar name.  We use PkgPath as the key instead
		// of a *packages.Package because we need to be able to find this across
		// different instances of Rewriter, where pointer values will be
		// different, but package import paths, which are just strings, will be
		// the same.
		NewFunc map[string]map[string]*ast.FuncLit

		// Per-package supplemental information.  Used only in initial rewrite.
		Info map[*packages.Package]*Info

		needsPublicType map[string]string
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
func (r *Rewriter) Rewrite(mode RewriteMode, genContent bool) error {
	for _, pkg := range r.Pkgs {
		if pkg.TypesInfo == nil {
			log.Printf("Pkg %s: No TypesInfo\n", pkg.Name)
			continue
		}
		var err error
		switch mode {
		case ModeRewrite:
			err = r.rewritePkg(pkg, genContent)
		case ModeReload:
			err = r.reloadPkg(pkg)
		default:
			panic(fmt.Sprintf("Internal error: unknown rewrite mode: %v", mode))
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
func (r *Rewriter) rewritePkg(pkg *packages.Package, genContent bool) error {
	if pkg.Name == "main" {
		return fmt.Errorf("Cannot rewrite package \"main\"")
	}
	if pkg.Name == "" {
		return fmt.Errorf("Missing package name: %s %s", pkg.ID, pkg.PkgPath)
	}
	// log.Printf("Pkg: %#v", pkg)

	r.needsPublicType = map[string]string{}
	funcs := map[*ast.FuncDecl]string{}
	stubVars := map[string]bool{}
	imports := extract.NewImportTracker(pkg.Name, pkg.PkgPath)

	type exportRec struct {
		orig string
		new  string
	}
	exported := map[types.Object]exportRec{}
	// Find everything in this package that's at package scope and not exported,
	// and export it, in-place, by adding a GRLx_ prefix.
	for ident, obj := range pkg.TypesInfo.Defs {
		_, isFunc := obj.(*types.Func)
		if ident.IsExported() ||
			ident.Name == "_" ||
			obj == nil ||
			obj.Pkg() != pkg.Types ||
			(obj.Parent() != nil && obj.Parent() != pkg.Types.Scope()) ||
			(ident.Name == "init" && isFunc) {

			continue
		}
		// Skip generic functions and methods.
		if s, ok := obj.Type().(*types.Signature); ok && (s.TypeParams().Len() > 0 || s.RecvTypeParams().Len() > 0) {
			continue
		}
		exported[obj] = exportRec{
			orig: ident.Name,
			new:  "GRLx_" + ident.Name,
		}
		ident.Name = exported[obj].new
	}

	for ident, obj := range pkg.TypesInfo.Uses {
		// Find everything that references any of the above exported objects, and
		// reset their names.
		if rec, ok := exported[obj]; ok {
			ident.Name = rec.new
		}

		// Note "internal" types that need non-internal aliases.
		switch obj := obj.(type) {
		case *types.TypeName:
			if pkg := obj.Pkg(); pkg != nil && util.InternalPkg(pkg.Path()) {
				// FIXME: Does not take into account types with the same name in
				// different internal packages.

				public := utypePrefix + "internal_" + ident.Name
				r.needsPublicType[ident.Name] = public
				// log.Printf("rewrite: Use of: %#v, Obj: %#v, external name: %s", ident, obj, public)
			}
		}
	}

	for ident, obj := range pkg.TypesInfo.Defs {
		if ident.Name == "_" || obj == nil {
			continue
		}
		// log.Printf("Rewrite: Def: %p: %#v, Obj: %p: %#v, Parent: %#v", ident, ident, obj, obj, obj.Parent())

		// Look at struct fields and methods in interfaces.
		if obj.Parent() == nil {
			switch obj := obj.(type) {
			// Struct field names
			case *types.Var:

			// Method names in interfaces. Might be wrong and/or simpler to do
			// with Defs as for struct types, above.
			case *types.Func:
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

		if obj, ok := obj.(*types.Func); ok {
			tagForTranslation(pkg, funcs, obj)
		}
	}

	r.stubTopLevelFuncs(pkg, funcs, ModeRewrite)

	// Generate symbol registrations
	// log.Printf("Looking for stubVars for pkg %s", pkg.PkgPath)
	for stubVar := range r.NewFunc[pkg.PkgPath] {
		// log.Printf("stubVar: %s", stubVar)
		stubVars[stubVar] = true
	}

	if genContent {
		// Write new source files for this package, based on the rewritten
		// syntax tree generated by rewritePkg (called indirectly via r.Rewrite,
		// above).
		//
		// Get newDir from the first file in the package
		outputFileName := strings.TrimPrefix(pkg.Fset.Position(pkg.Syntax[0].Pos()).Filename, r.Pwd+"/")
		newDir := filepath.Join(r.OutputDir, filepath.Dir(outputFileName))

		registrationSource, err := extract.GenContent(newDir+"/grl_unknown.go",
			pkg.Name, pkg.PkgPath, pkg.Types,
			stubVars, r.needsPublicType, imports)
		if err != nil {
			return fmt.Errorf("Failed generating symbol registration for %q at %s: %w", pkg.Name, pkg.PkgPath, err)
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
			// log.Printf("reload: I see: Obj: %#v", c.Node())
			switch n := c.Node().(type) {
			case *ast.Ident:
				if publicType, ok := r.needsPublicType[n.Name]; ok {
					switch c.Parent().(type) {
					case *ast.ValueSpec:
						// log.Printf("Setting public type: %s to %s.%s", n.Name, pkg.Name, publicType)
						c.Replace(&ast.Ident{Name: publicType})
					case *ast.SelectorExpr:
						// log.Printf("reload: publictype: I see: Obj: %#v, parent: %#v", c.Node(), c.Parent())
						replace[c.Parent()] = &ast.Ident{Name: publicType}
					}
				}
			}
			return true
		}
		post := func(c *astutil.Cursor) bool {
			if r, ok := replace[c.Node()]; ok {
				c.Replace(r)
				delete(replace, c.Node())
			}
			return true
		}
		file = astutil.Apply(file, pre, post).(*ast.File)
		if len(replace) > 0 {
			panic(fmt.Sprintf("Internal error: replace map should be used up & empty, but it has %d values in it: %v",
				len(replace), replace))
		}

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
	file := FindFile(pkg, obj.Pos())
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
	// log.Printf("Warning: Cannot find FuncDecl for %s\n", obj.FullName())
}

func FindFile(pkg *packages.Package, pos token.Pos) *ast.File {
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

					stubName, newVar, newInit, funcLit := rewriteFunc(pkg.PkgPath, name, n)
					if newVar != nil {
						c.InsertAfter(newInit)
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
func rewriteFunc(pkgPath, name string, node *ast.FuncDecl) (string, *ast.GenDecl, *ast.FuncDecl, *ast.FuncLit) {
	// Don't rewrite generic functions, i.e., functions with type parameters
	if node.Type.TypeParams != nil {
		return "", nil, nil, nil
	}

	newVarType := copyFuncType(node.Type)

	var newArgs []ast.Expr

	recvrVarOffset := 0

	// Process the receiver for a method definition
	if node.Recv != nil {
		// Note that we have a receiver
		recvrVarOffset++

		var receiverName string
		if len(node.Recv.List[0].Names) == 0 || node.Recv.List[0].Names[0].Name == "_" {
			// If the function has no receiver name, or it's "_", we have to generate one.
			receiverName = syntheticReceiver
			node.Recv.List[0].Names = []*ast.Ident{{Name: receiverName}}
		} else {
			receiverName = node.Recv.List[0].Names[0].Name
		}

		// Add receiver name to the front of the function call arglist
		newArgs = append(newArgs, &ast.Ident{Name: receiverName})

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

	// Copy all formal arguments into the arglist of the function call,
	// replacing missing args and "_" args with synthetic arg names.
	argN := 0
	for i, argField := range node.Type.Params.List {
		if len(argField.Names) == 0 {
			newName := fmt.Sprintf("%s%d", syntheticArgPrefix, argN)
			argN++
			ident := &ast.Ident{Name: newName}
			argField.Names = []*ast.Ident{ident}
			newArgs = append(newArgs, ident)

			// Update newVarType too
			newVarType.Params.List[recvrVarOffset+i].Names = argField.Names
		} else {
			for _, argName := range argField.Names {
				if argName.Name == "_" {
					// Update the argname in place
					argName.Name = fmt.Sprintf("%s%d", syntheticArgPrefix, argN)
					// log.Printf("set argName to %s", argNameIdent.Name)
				}
				newArgs = append(newArgs, &ast.Ident{Name: argName.Name})
				argN++
			}
		}
	}
	// log.Printf("rewriteFunc: %s: newArgs: %d, %v", name, len(newArgs), newArgs)

	name = stubPrefix + name
	// Define the new body of the function/method to just call the stub.
	stubCall := &ast.CallExpr{
		Fun:      &ast.Ident{Name: name},
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

	// Define the var stub with the new arglist
	//
	// var <name> <func-type>
	newVar := &ast.GenDecl{
		Tok: token.VAR,
		Specs: []ast.Spec{
			&ast.ValueSpec{
				Names: []*ast.Ident{{Name: name}},
				Type:  newVarType,
			}}}

	// Assign the old body from the function/method to the stub var in an init
	// function.
	//
	// func init() { <name> = <function-literal> }
	newInit := &ast.FuncDecl{
		Name: &ast.Ident{Name: "init"},
		Type: &ast.FuncType{Params: &ast.FieldList{}},
		Body: &ast.BlockStmt{
			List: []ast.Stmt{
				&ast.AssignStmt{
					Lhs: []ast.Expr{&ast.Ident{Name: name}},
					Tok: token.ASSIGN,
					Rhs: []ast.Expr{funcLit}}}}}

	// Replace the node's body with the new body in-place.
	//
	// func <real-name><signature> { <real-body> }
	//
	// =>
	//
	// func <real-name><signature> { <call-stub-var> }
	node.Body = body
	return name, newVar, newInit, funcLit
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
	pkg, node := r.FuncNode(pkgPath, stubVar)
	if pkg == nil || node == nil {
		return "", fmt.Errorf("No stubVar found for %s:%s", pkgPath, stubVar)
	}
	b := &bytes.Buffer{}
	// log.Printf("NewFunc[%s][%s]", pkgPath, stubVar)
	format.Node(b, pkg.Fset, node)
	return b.String(), nil
}

func (r *Rewriter) FuncNode(pkgPath, stubVar string) (*packages.Package, *ast.FuncLit) {
	for _, pkg := range r.Pkgs {
		if pkg.PkgPath != pkgPath {
			continue
		}
		// log.Printf("NewFunc[%s][%s]", pkgPath, stubVar)
		return pkg, r.NewFunc[pkgPath][stubVar]
	}
	return nil, nil
}
