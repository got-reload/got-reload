package hotreload

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

const cmdPath = "github.com/huckridgesw/hot-reload/cmd/hot-reload"

var (
	// Define the file.  Things to include: exported and unexported function
	// and method.  Struct with exported & unexported field.
	testFile = `
package dummy

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
)

var (
	v1  int
	V2  int
	v3  t1
	V4  t1
	v5  T2
	V5  T2
	v6  other_package.T1
	V7  other_package.T2
	v8  t3
	V9  t3
	V10 uint32
)

func f1() int {
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
	return 0
}

func F2() t1 {
	return t1{}
}

func (r t3) t3m1() int {
	r.t3f1 = r.T2f2
	return 0
}

func (r T4) T4m1() int {
	r.t4f1 = r.T4f2
	return 0
}

func (r T4) t5m1() int {
	return 0
}
`
)

func init() {
	newFile, err := format.Source([]byte(testFile))
	if err != nil {
		panic(err)
	}
	testFile = string(newFile)
}

func TestCompileParse(t *testing.T) {
	Convey("Given a testfile", t, func() {
		// Parse it
		node, err := Parse("dummy", testFile)

		Convey("It should parse correctly", func() {
			So(err, ShouldBeNil)
			So(node, ShouldNotBeNil)

			Convey("When rewritten", func() {
				newNode := Rewrite(node)

				Convey("It should rewrite correctly", func() {
					So(err, ShouldBeNil)
					So(newNode, ShouldNotBeNil)

					// Verify the rewrite
					Convey("t1 should map to YY_t1", func() {
						types := getTypes(newNode)
						So(types, ShouldContainKey, "YY_t1")
						So(types, ShouldContainKey, "T2")
						So(types, ShouldContainKey, "YY_t3")
						So(types["YY_t3"].(*ast.StructType).Fields.List[0].Names[0].Name, ShouldEqual, "YY_t3f1")
						So(types["YY_t3"].(*ast.StructType).Fields.List[1].Names[0].Name, ShouldEqual, "T3f2")
						So(types["YY_t3"].(*ast.StructType).Fields.List[2].Names[0].Name, ShouldEqual, "T3f3")
						So(types["YY_t3"].(*ast.StructType).Fields.List[3].Names[0].Name, ShouldEqual, "T3f4")
						So(types["YY_t3"].(*ast.StructType).Fields.List[3].Type.(*ast.Ident).Name,
							ShouldEqual, "YY_uint32")
						So(types, ShouldContainKey, "T4")
						So(types["T4"].(*ast.StructType).Fields.List[0].Names[0].Name,
							ShouldEqual, "YY_t4f1")
						So(types, ShouldContainKey, "YY_t5")
						So(types["YY_t5"].(*ast.InterfaceType).Methods.List[0].Names[0].Name,
							ShouldEqual, "YY_t5m1")
						So(types["YY_t5"].(*ast.InterfaceType).Methods.List[1].
							Type.(*ast.FuncType).Params.List[0].Type.(*ast.Ident).Name,
							ShouldEqual, "YY_t3")
						So(types["YY_t5"].(*ast.InterfaceType).Methods.List[2].
							Type.(*ast.FuncType).Params.List[0].Type.(*ast.Ident).Name,
							ShouldEqual, "YY_t3")
						So(types["YY_t5"].(*ast.InterfaceType).Methods.List[3].
							Type.(*ast.FuncType).Results.List[0].Type.(*ast.Ident).Name,
							ShouldEqual, "YY_t3")
						So(types, ShouldContainKey, "T6")
						So(types, ShouldContainKey, "YY_uint32")

						vars := getVars(newNode)
						So(vars, ShouldContainKey, "YY_v1")
						So(vars, ShouldContainKey, "YY_v3")
						So(vars["YY_v3"].(*ast.Ident).Name, ShouldEqual, "YY_t1")
						So(vars, ShouldContainKey, "V4")
						So(vars["V4"].(*ast.Ident).Name, ShouldEqual, "YY_t1")
						So(vars, ShouldContainKey, "YY_v5")
						So(vars["YY_v5"].(*ast.Ident).Name, ShouldEqual, "T2")
						So(vars, ShouldContainKey, "YY_v6")
						So(vars, ShouldContainKey, "YY_v8")
						So(vars["YY_v8"].(*ast.Ident).Name, ShouldEqual, "YY_t3")
						So(vars, ShouldContainKey, "V10")
						So(vars["V10"].(*ast.Ident).Name, ShouldEqual, "YY_uint32")
						So(vars, ShouldContainKey, "v11")
						So(vars, ShouldNotContainKey, "YY_v11")

						funcs := getFuncs(newNode)
						So(funcs, ShouldContainKey, "YY_f1")
						So(funcs, ShouldContainKey, "F2")
						So(funcs, ShouldContainKey, "YY_t3m1")
						So(funcs, ShouldContainKey, "T4m1")
						So(funcs, ShouldContainKey, "YY_t5m1")

						fset := token.NewFileSet()
						var buf bytes.Buffer
						err = format.Node(&buf, fset, newNode)
						So(err, ShouldBeNil)
						fmt.Printf("%s", buf.String())

						ast.Print(fset, newNode)

						// t1 => YY_t1
						// t3 => YY_t3
						// t3.t3f1 => YY_t3f1
						// T4.t4f1 => YY_t4f1
						// # t5 is an interface
						// t5 => YY_t5
						// t5m1 => YY_t5m1
						//   question: What to do with random methods in other packages called t5m1?
						//   answer: Nothing.  If they're in other packages and
						//     they're unexported, they don't matter to us.
						// v1 => YY_v1
						// v3 t1 => YY_v3 YY_t1
						// V4 t1 => V4 YY_t1
						// v5 => YY_v5
						// f1 => YY_f1
						// t3.t3m1 => YY_t3.YY_t3m1
					})
				})
			})
		})
	})
}

func TestFirstCompile(t *testing.T) {
	// Define the file.  Include everything from TestCompileParse.
	// Run the Go compiler with the source filter
	// Verify the output
}

func TestReloadParse(t *testing.T) {
	// Define the file.  Include everything from TestCompileParse.
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

func getFuncs(node ast.Node) map[string]*ast.FuncType {
	funcs := map[string]*ast.FuncType{}

	depth := 0
	f := func(n ast.Node) bool {
		if n == nil {
			depth--
			return true
		}

		depth++
		switch n := n.(type) {
		case *ast.FuncDecl:
			funcs[n.Name.Name] = n.Type
		}

		return true
	}

	ast.Inspect(node, f)
	return funcs
}
