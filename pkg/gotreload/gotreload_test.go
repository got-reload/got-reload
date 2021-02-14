package gotreload

import (
	"bytes"
	"context"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"os/exec"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"golang.org/x/tools/go/packages"
)

const cmdPath = "github.com/got-reload/hot-reload/cmd/hot-reload"

var (
	testFile1 = `
package fake

import (
	"bytes"
	"errors"
)

type (
	t1 int
	T2 int
	t3 struct {
		t3f1 int
		T3f2 int
		T3f3 *t3
		T3f4 uint32
	}
	T4 struct {
		t4f1 int
		T4f2 int
	}
	t5 interface {
		t5m1() int
		T5m2(t3) int
		T5m3(x t3) int
		T5m4() t3
	}
	T6 interface {
		t6f1() int
		T6f2() int
	}
	uint32 int
	T7     struct {
		t3
		T4
	}
)

var (
	v1            int
	V2            int
	v3            t1
	V4            t1
	v5            T2
	V5            T2
	v6            bytes.Buffer
	V7            bytes.Reader
	v8            t3
	V9            t3
	V10           uint32
	v13, V14, v15 t3
	// Test a type defined in another file
	v16 f2t1
)

func f1(a1 int, a2, a3 t1, a4 T2) (int, t1, error) {
	type (
		T8 struct {
			t8f1 int
			T8f2 int
		}
		t9 struct {
			t9f1 int
			T9f2 int
		}
	)

	v1 = V2
	v3 = V4
	v8.t3f1 = V9.T3f2
	var v11 t3
	if v1 == V2 {
		v11.t3f1 = v1
	}
	if v1 == V2 {
		v11.T3f2 = v8.t3f1
	}

	f1v1 := T8{}
	f1v2 := t9{}
	f1v1.t8f1 = f1v2.t9f1

	return v1, v3, errors.New("an error")
}

func F2() (ret_named t1) {
	var v12 t1
	return v12
}

func (r *t3) t3m1(a5 t5) (ret2_named int) {
	a5.t5m1()
	a5.T5m2(t3{})
	r.t3f1 = r.T3f2
	ret2_named = 0
	return
}

func (r T4) T4m1() int {
	r.t4f1 = r.T4f2
	return 0
}

func (r T4) t5m1() {
}

// Named receiver with unnamed arg
func (r T4) t4m2(int) int {
	return 0
}

// Unnamed receiver with used arg
func (T4) t4m3(a6 int) int {
	return 0
}
`

	testFile2 = `
package fake

// Test a type defined in another file
var f2v1 t1

type (
	f2t1 struct {
		f2t1f1 int
	}
)
`

	// Parse this and print it out to figure out what to generate for the
	// rewritten functions.
	targetFile = `
package target

func GRL_f1(a1 int, a2, a3 GRL_t1, a4 T2) (int, GRL_t1, error) {
	return GRLf_f1(a1, a2, a3, a4)
}

var GRLf_f1 = func(a1 int, a2, a3 GRL_t1, a4 T2) (int, GRL_t1, error) {
	v1 = V2
	v3 = V4
	v8.t3f1 = V9.T3f2
	var v11 t3
	if v1 == V2 {
		v11 = v1
	}
	if v1 == V2 {
		v11 = v8.t3f1
	}
	return v1, v3, errors.New("an error")
}

func GRLset_f1(f func(a1 int, a2, a3 GRL_t1, a4 T2) (int, GRL_t1, error)) {
	GRLf_f1 = f
}

func Register(pkgName, ident string, val reflect.Value) {}

var pkgName, ident string
var val reflect.Value
func init() { 
	rewriter.Register("pkg_name_literal", "ident_literal", reflect.ValueOf(GRLset_f1)) 
}
`
)

func init() {
	newFile, err := format.Source([]byte(testFile1))
	if err != nil {
		panic(err)
	}
	testFile1 = string(newFile)
}

func TestCompileParse(t *testing.T) {
	Convey("Given a testfile", t, func() {
		cwd, err := os.Getwd()
		So(err, ShouldBeNil)
		// t.Logf("Getwd: %s, %v", cwd, err)

		// Parse it
		r := &Rewriter{
			Config: packages.Config{
				// Logf: t.Logf,
				// This is handy for knowing what is being parsed and by what name.
				ParseFile: func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
					// t.Logf("ParseFile: %s\n", filename)
					return parser.ParseFile(fset, filename, src, 0)
				},
				// Replace the fake t1 & t2.go with our testfile data.
				Overlay: map[string][]byte{
					cwd + "/fake/t1.go": []byte(testFile1),
					cwd + "/fake/t2.go": []byte(testFile2),
				},
			},
		}

		err = r.Load("./fake")

		Convey("It should parse correctly", func() {
			So(err, ShouldBeNil)
			So(r.Pkgs, ShouldNotBeNil)

			Convey("When rewritten", func() {
				err = r.Rewrite(false)

				Convey("It should rewrite correctly", func() {
					So(err, ShouldBeNil)
					So(r.Pkgs, ShouldNotBeNil)
					pkg := r.Pkgs[0]

					Convey("Types, variables, functions, and function bodies should translate correctly", func() {
						file := pkg.Syntax[0]

						// Do some diagnostics (maybe) even if tests fail.
						defer func() {
							if false {
								// General diagnostics.
								var buf bytes.Buffer

								// Formatted output file
								err = format.Node(&buf, pkg.Fset, file)
								So(err, ShouldBeNil)
								Printf("%s", buf.String())
								// // File AST
								// buf = bytes.Buffer{}
								// ast.Fprint(&buf, pkg.Fset, file, ast.NotNilFilter)
								// Printf("%s", buf.String())

								// What should the rewritten f1 look like, ast-wise?
								targetNode, err := parser.ParseFile(pkg.Fset, "target", targetFile, 0)
								So(err, ShouldBeNil)
								So(targetNode, ShouldNotBeNil)
								buf = bytes.Buffer{}
								ast.Fprint(&buf, pkg.Fset, targetNode, ast.NotNilFilter)
								Printf("%s", buf.String())
							}
						}()

						types := getTypes(file)
						So(types, ShouldContainKey, exportPrefix+"t1")
						So(types, ShouldContainKey, "T2")
						So(types, ShouldContainKey, exportPrefix+"t3")
						So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[0].Names[0].Name,
							ShouldEqual, exportPrefix+"t3f1")
						So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[1].Names[0].Name,
							ShouldEqual, "T3f2")
						So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[2].Names[0].Name,
							ShouldEqual, "T3f3")
						So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[3].Names[0].Name,
							ShouldEqual, "T3f4")
						So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[3].Type.(*ast.Ident).Name,
							ShouldEqual, exportPrefix+"uint32")
						So(types, ShouldContainKey, "T4")
						So(types["T4"].(*ast.StructType).Fields.List[0].Names[0].Name,
							ShouldEqual, exportPrefix+"t4f1")
						So(types, ShouldContainKey, exportPrefix+"t5")
						So(types[exportPrefix+"t5"].(*ast.InterfaceType).Methods.List[0].Names[0].Name,
							ShouldEqual, exportPrefix+"t5m1")
						So(types[exportPrefix+"t5"].(*ast.InterfaceType).Methods.List[1].
							Type.(*ast.FuncType).Params.List[0].Type.(*ast.Ident).Name,
							ShouldEqual, exportPrefix+"t3")
						So(types[exportPrefix+"t5"].(*ast.InterfaceType).Methods.List[2].
							Type.(*ast.FuncType).Params.List[0].Type.(*ast.Ident).Name,
							ShouldEqual, exportPrefix+"t3")
						So(types[exportPrefix+"t5"].(*ast.InterfaceType).Methods.List[3].
							Type.(*ast.FuncType).Results.List[0].Type.(*ast.Ident).Name,
							ShouldEqual, exportPrefix+"t3")
						So(types, ShouldContainKey, "T6")
						So(types, ShouldContainKey, exportPrefix+"uint32")

						vars := getVars(file)
						So(vars, ShouldContainKey, exportPrefix+"v1")
						So(vars, ShouldContainKey, exportPrefix+"v3")
						So(vars[exportPrefix+"v3"].(*ast.Ident).Name, ShouldEqual, exportPrefix+"t1")
						So(vars, ShouldContainKey, "V4")
						So(vars["V4"].(*ast.Ident).Name, ShouldEqual, exportPrefix+"t1")
						So(vars, ShouldContainKey, exportPrefix+"v5")
						So(vars[exportPrefix+"v5"].(*ast.Ident).Name, ShouldEqual, "T2")
						So(vars, ShouldContainKey, exportPrefix+"v6")
						So(vars, ShouldContainKey, exportPrefix+"v8")
						So(vars[exportPrefix+"v8"].(*ast.Ident).Name, ShouldEqual, exportPrefix+"t3")
						So(vars, ShouldContainKey, "V10")
						So(vars["V10"].(*ast.Ident).Name, ShouldEqual,
							exportPrefix+"uint32")
						So(vars, ShouldContainKey, "v11")
						So(vars, ShouldNotContainKey, exportPrefix+"v11")

						funcs := getFuncs(file)
						So(funcs, ShouldContainKey, exportPrefix+"f1")
						So(funcs[exportPrefix+"f1"].Body.List[0], ShouldHaveSameTypeAs, &ast.ReturnStmt{})
						So(funcs, ShouldContainKey, setPrefix+"f1")
						So(funcs, ShouldContainKey, "F2")
						So(funcs, ShouldContainKey, setPrefix+"F2")
						So(funcs, ShouldContainKey, exportPrefix+"t3m1")
						So(funcs, ShouldContainKey, "T4m1")
						So(funcs, ShouldContainKey, exportPrefix+"t5m1")
					})
				})
			})
		})
	})
}

func _TestFirstCompile(t *testing.T) {
	const data = `package first

    func f1() int {
        return 9
    }`

	// TODO: can't use this method because I haven't published this code yet.
	// Go get can't find the executable.
	// Ensure we have the binary for the source filter
	// cmd := exec.CommandContext(context.Background(), "go", "get", cmdPath)
	// cmd.Stderr = os.Stderr
	// cmd.Stdout = os.Stdout
	// if err := cmd.Run(); err != nil {
	// 	t.Fatalf("Failed to get source filter binary: %v", err)
	// }
	// Define the file.
	file, err := ioutil.TempFile("", "hotreload-*.go")
	if err != nil {
		t.Fatalf("Failed to create temporary source code file: %v", err)
	}
	fileName := file.Name()
	t.Logf("Using temporary file %s", fileName)
	if _, err := file.Write([]byte(data)); err != nil {
		t.Fatalf("Failed writing into temporary file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Failed closing temporary file: %v", err)
	}
	// Run the Go compiler with the source filter
	cmd := exec.CommandContext(context.Background(), "go", "build", "-toolexec", "hot-reload", fileName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to get source filter binary: %v", err)
	}
	// Verify the output
}

func TestReloadParse(t *testing.T) {
	// Define the file.
	// Do the initial parse
	// Verify the "real" function runs when called
	// Change a function
	// Do the subsequent parse
	// Reinstall(?) the new function
	// Verify the new function runs when called
}

// FIXME: This should look at top-level types only
func getTypes(node ast.Node) map[string]ast.Expr {
	types := map[string]ast.Expr{}

	f := func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.TypeSpec:
			types[n.Name.Name] = n.Type
		}

		return true
	}

	ast.Inspect(node, f)
	return types
}

func getVars(node ast.Node) map[string]ast.Expr {
	vars := map[string]ast.Expr{}

	depth := 0
	f := func(n ast.Node) bool {
		if n == nil {
			depth--
			return true
		}

		depth++
		switch n := n.(type) {
		case *ast.ValueSpec:
			for _, ident := range n.Names {
				// fmt.Printf("%s, %d\n", ident.Name, depth)
				vars[ident.Name] = n.Type
			}
		}

		return true
	}

	ast.Inspect(node, f)
	return vars
}

func getFuncs(node ast.Node) map[string]*ast.FuncDecl {
	funcs := map[string]*ast.FuncDecl{}

	depth := 0
	f := func(n ast.Node) bool {
		if n == nil {
			depth--
			return true
		}

		depth++
		switch n := n.(type) {
		case *ast.FuncDecl:
			funcs[n.Name.Name] = n
		}

		return true
	}

	ast.Inspect(node, f)
	return funcs
}
