package gotreload

import (
	"bytes"
	"context"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path"
	"reflect"
	"regexp"
	"testing"

	"github.com/got-reload/got-reload/pkg/extract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/traefik/yaegi/interp"
	"golang.org/x/tools/go/packages"
)

const cmdPath = "github.com/got-reload/got-reload/cmd/got-reload"

var (
	testFile1 = mustFormat(`
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

// Variadic (use of "...") function
func F3 (a, b, c int, i ...int) int {
	return i[0]
}
`)

	testFile2 = mustFormat(`
package fake

// Test a type defined in another file
var f2v1 t1

type (
	f2t1 struct {
		f1 float32
		f2t1f1 int
	}
	f2t2 struct {
		f1 float32
		f2t2f1 int
	}
)
`)

	// Parse this and print out its AST to figure out what to generate for the
	// rewritten functions.
	targetFile = mustFormat(`
package target

import "sync"
import "context"

func target_func(arg ...int) int {
	return GRLfvar_target_func(arg...)
}

var GRLfvar_target_func = func(arg ...int) int {
	return arg[0]
}

func GRLfset_target_func(f func(arg ...int) int) {
	GRLfvar_target_func = f
}

type T10 struct {
	unexported_var int
}

func (r T10) unexported_method() int {
	return GRLfvar_unexported_method()
}

type t11 struct {
	unexported_var int
	ExportedVar float32
}

var unexported_var int

func GRLuaddr_unexported_var() *int { return &unexported_var }

type GRLt_t11 = t11

type t1 struct {
	f1 int
}

var v3, V4 int

var v5 sync.Mutex

var v7 = new(float32)

func F() { 
	v3 = V4
	*GRLuaddr_v3() = V4
	V4 = v3
	V4 = *GRLuaddr_v3()
	V4 = v3 + 5
	V4 = *GRLuaddr_v3() + 5

	var v_t1 t1

	v_t1.f1 = 5
	*v_t1.GRLmaddr_t1_f1() = 5
	V4 = v_t1.f1
	V4 = *v_t1.GRLmaddr_t1_f1()

	fmt.Printf("%v, %p, %v, %p", v3, &v3, V4, &V4)
	fmt.Printf("%v, %p, %v, %p", *GRLuaddr_v3(), GRLuaddr_v3(), V4, &V4)

	v5.Lock()
	GRLuaddr_v5().Lock()

	*v7 += 0.1
	**GRLuaddr_v7() += 0.1

}

type ContextAlias = context.Context

func F7(ctx ContextAlias) {
	var ctx2 ContextAlias
	_ = ctx2
	<-ctx.Done()
}

func F7_rewrite(ctx ContextAlias) {
	var ctx2 ContextAlias
	_ = ctx2
	<-ctx.Done()
}

// Type name needs work; this type and the next method need to be in the
// grl_registrations.go file.
type GRLt_xlat_t11 struct {
	GRL_field_unexported_var int
	ExportedVar float32
}
func (r *GRLt_xlat_t11) GRL_Xlat() *t11 {
	return &t11{
		unexported_var: r.GRL_field_unexported_var,
		ExportedVar: r.ExportedVar,
	}
}

func example() {
	unexported_var = 1 // test set
	if unexported_var == 0 { // test get
	}

	GRLuset_unexported_var(1) // test set
	if GRLuget_unexported_var() == 0 { // test get
	}

	var v T10
	dummy := v.unexported_var
	dummy2 := *v.GRLuaddr_unexported_var()

	v.unexported_var = 1
	v.GRLuset_unexported_var(1)
	*v.GRLuaddr_unexported_var() = 1

	if v.unexported_var == 0 {}
	if v.GRLuget_unexported_var() == 0 {}
	if *v.GRLuaddr_unexported_var() == 0 {}


	var v GRLt_t11
	var vE GRLt_t11
	v2 := t11{
		unexported_var: 5,
		ExportedVar: 6.0,
	}
	// The above should translate to the below (in real life, it should
	// translate recursively). Not sure how this interacts with stuff you're
	// not supposed to copy, like sync.Mutex. That might only be a linter
	// error, though.
	v3 := *(GRLt_xlat_t11{
		GRL_field_unexported_var: 5,
		ExportedVar: 6.0,
	}.Xlat())
}

`)
)

var findWhitespace = regexp.MustCompile(`[ \n\t]+`)

// Change all multiple space runs with a single space
func filterWhitespace[T []byte | string](w T) string {
	switch thing := interface{}(w).(type) {
	case string:
		return findWhitespace.ReplaceAllLiteralString(thing, " ")
	case []byte:
		return string(findWhitespace.ReplaceAllLiteral(thing, []byte{' '}))
	default:
		panic("Unknown type in filterWhitespace")
	}
}

func mustFormat(src string) string {
	res, err := format.Source([]byte(src))
	if err != nil {
		panic(err)
	}
	return string(res)
}

func TestSampleFuncRewrites(t *testing.T) {
	i := interp.New(interp.Options{})
	require.NotNil(t, i)

	// Verify basic Go syntax, haha
	var v int = 1
	GRLuaddr_v := func() *int { return &v }
	*GRLuaddr_v() = 2
	assert.Equal(t, 2, v)
	*GRLuaddr_v() += 2
	assert.Equal(t, 4, v)

	var f = new(float64)
	var f2 float64
	GRLuaddr_f := func() **float64 { return &f }
	**GRLuaddr_f() = 2.0
	assert.Equal(t, 2.0, *f)
	**GRLuaddr_f() += 2.0
	assert.Equal(t, 4.0, *f)
	*GRLuaddr_f() = &f2
	f2 = 5
	assert.Equal(t, 5.0, **GRLuaddr_f())

	// .../pkg_name/pkg_name => symbol => value
	// type Exports map[string]map[string]reflect.Value
	i.Use(interp.Exports{
		"pkg/pkg": {
			"V":          reflect.ValueOf(&v).Elem(),
			"GRLuaddr_v": reflect.ValueOf(GRLuaddr_v),
		},
	})
	i.ImportUsed()

	t.Run(`import . "pkg"`, func(t *testing.T) {
		_, err := i.Eval(`import . "pkg"`)
		require.NoError(t, err)
	})

	t.Run("V", func(t *testing.T) {
		val, err := i.Eval("V")
		require.NoError(t, err)
		assert.IsType(t, int(0), val.Interface())
		assert.Equal(t, v, val.Interface().(int))
	})
	t.Run("GRLuaddr_v()", func(t *testing.T) {
		val, err := i.Eval("GRLuaddr_v()")
		require.NoError(t, err)
		assert.IsType(t, new(int), val.Interface())
		assert.Equal(t, &v, val.Interface().(*int))
	})
	t.Run("*GRLuaddr_v()", func(t *testing.T) {
		val, err := i.Eval("*GRLuaddr_v()")
		require.NoError(t, err)
		assert.IsType(t, int(0), val.Interface())
		assert.Equal(t, v, val.Interface().(int))
	})
	t.Run("*GRLuaddr_v() = 3", func(t *testing.T) {
		_, err := i.Eval("*GRLuaddr_v() = 3")
		require.NoError(t, err)
		assert.Equal(t, v, 3)
	})
	t.Run("*GRLuaddr_v() += 2", func(t *testing.T) {
		_, err := i.Eval("*GRLuaddr_v() += 2")
		require.NoError(t, err)
		assert.Equal(t, v, 5)
	})
}

func TestEvalInPackage(t *testing.T) {
	i := interp.New(interp.Options{})
	require.NotNil(t, i)

	var v int = 8
	i.Use(interp.Exports{
		"pkg/pkg": {
			"V": reflect.ValueOf(&v).Elem(),
		},
	})
	i.ImportUsed()

	// Can you refer to V without a package identifier?
	_, err := i.Eval(`import . "pkg"; func init() { V = 9 }`)
	require.NoError(t, err)
	assert.Equal(t, 9, v)

	// Can you do it again in the same interpreter?
	_, err = i.Eval(`import . "pkg"; func init() { V = 10 }`)
	require.NoError(t, err)
	assert.Equal(t, 10, v)
}

var notExported int = 5

func TestCompileParse(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	// t.Logf("Getwd: %s, %v", cwd, err)

	rewrite := func(src string) (*Rewriter, *packages.Package, *ast.File, string, map[string]ast.Expr, string) {
		t.Helper()
		// log.Printf("rewrite: package fake; %s", src)
		r := NewRewriter()

		// r.Config.Logf = t.Logf
		// // This is handy for knowing what is being parsed and by what name.
		// r.Config.ParseFile = func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
		// 	t.Logf("ParseFile: %s\n", filename)
		// 	return parser.ParseFile(fset, filename, src, 0)
		// }

		// Replace the fake t1 with our testfile data.
		path := path.Dir(cwd) + "/fake"
		r.Config.Overlay = map[string][]byte{
			path + "/t1.go": []byte("package fake; " + src),
			path + "/t2.go": []byte("package fake"),
		}

		err = r.Load("../fake")
		require.NoError(t, err)
		require.NotNil(t, r.Pkgs)

		err = r.Rewrite(ModeRewrite)
		require.NoError(t, err)
		require.NotNil(t, r.Pkgs)
		require.NotZero(t, len(r.Pkgs))
		pkg := r.Pkgs[0]
		// require.NotZero(t, len(r.Info))
		// require.Contains(t, r.Info, pkg)
		// require.NotNil(t, r.Info[pkg])
		// require.NotNil(t, r.Info[pkg].Registrations)

		require.NotZero(t, len(pkg.Syntax))
		file := pkg.Syntax[0]
		types := getTypes(file)
		var registrations string
		if r.Info[pkg] != nil && len(r.Info[pkg].Registrations) > 0 {
			registrations = string(r.Info[pkg].Registrations)
		}

		b := &bytes.Buffer{}
		err = format.Node(b, pkg.Fset, file)
		require.NoError(t, err)
		output := b.String()

		return r, pkg, file, output, types, registrations
	}

	rewriteTrim := func(src string) (*Rewriter, *packages.Package, *ast.File, string, map[string]ast.Expr, string) {
		r, pkg, file, output, types, registrations := rewrite(src)
		output = filterWhitespace(output)
		registrations = filterWhitespace(registrations)
		return r, pkg, file, output, types, registrations
	}

	r, _, _, _, types, registrations := rewriteTrim("type t1 int")

	// Types, variables, functions, and function bodies should translate correctly
	assert.Contains(t, types, "t1")
	assert.Contains(t, r.needsPublicType, "t1")
	assert.Equal(t, "GRLt_t1", r.needsPublicType["t1"])
	assert.Contains(t, registrations, "type GRLt_t1 = t1")

	r, _, _, output, _, registrations := rewriteTrim("func f(a int) int { return a }")
	assert.Contains(t, output, `func f(a int) int { return GRLfvar_f(a) }`)
	assert.Contains(t, output, `var GRLfvar_f = func(a int) int { return a }`)
	assert.Contains(t, r.stubVars, "GRLfvar_f")
	assert.Contains(t, registrations, `"GRLfvar_f": reflect.ValueOf(&GRLfvar_f).Elem()`)
	// log.Printf("registrations:\n%s", registrations)

	r, _, _, _, _, registrations = rewriteTrim(`type t1 int; var v3 t1`)
	assert.Contains(t, registrations, `func GRLuaddr_v3() *t1 { return &v3 }`)

	r, pkg, _, output, _, registrations := rewriteTrim(`
import (
	"fmt"
	"sync"
	"context"
)

type t1 int
type t2 struct {
	*t1
	f1 t1
	F2 int
	f3 []int
	f4 []*int
	f5 []t1
	f6 []*t1
	f7 *[]*t1
	f8 *[]*context.Context
}
var v3 t1
var V4 t1

var v5 = new(float32)

var v6 sync.Mutex

var v7 = new(float32)

type M sync.Mutex

type ContextAlias = context.Context

var v8 = []int{}

var v9 = (interface{})(2)

func F() { v3 = V4 }
func F2() { 
	V4 = v3 
	V4 = v3 + 5
	V4 = 6 + v3
}
func F3() {
	var v_t2 t2
	V4 = v_t2.f1
}
func F4() {
	fmt.Printf("%v %p %v %p", v3, &v3, V4, &V4)
}
func F5() {
	v6.Lock()
}
func F6() {
	*v7 += 0.1
}
func F7(ctx ContextAlias) {
	<-ctx.Done()

	var ctx2 ContextAlias
	_ = ctx2
}
func F8(a int, b float32) (int, float32) {
	return a, b
}
`)
	assert.Contains(t, registrations, "func GRLuaddr_v3() *t1 { return &v3 }")
	assert.Contains(t, registrations, "func GRLuaddr_v5() **float32 { return &v5 }")
	assert.Contains(t, registrations, "func GRLuaddr_v6() *sync.Mutex { return &v6 }")
	assert.Contains(t, registrations, "func GRLuaddr_v8() *[]int { return &v8 }")
	assert.Contains(t, registrations, "func GRLuaddr_v9() *interface{} { return &v9 }")
	assert.Contains(t, registrations, `"sync"`)
	assert.Contains(t, registrations, `"M": reflect.ValueOf((*M)(nil))`)
	assert.Contains(t, registrations, `"ContextAlias": reflect.ValueOf((*ContextAlias)(nil))`)
	assert.Contains(t, registrations, "func GRLuaddr_v8() *[]int { return &v8 }")
	assert.Contains(t, registrations, "func (r *t2) GRLmaddr_t2_t1() **t1 { return &r.t1 }")
	assert.Contains(t, registrations, "func (r *t2) GRLmaddr_t2_f3() *[]int { return &r.f3 }")
	assert.Contains(t, registrations, "func (r *t2) GRLmaddr_t2_f4() *[]*int { return &r.f4 }")
	assert.Contains(t, registrations, "func (r *t2) GRLmaddr_t2_f5() *[]t1 { return &r.f5 }")
	assert.Contains(t, registrations, "func (r *t2) GRLmaddr_t2_f6() *[]*t1 { return &r.f6 }")
	assert.Contains(t, registrations, "func (r *t2) GRLmaddr_t2_f7() **[]*t1 { return &r.f7 }")
	assert.Contains(t, registrations, "func (r *t2) GRLmaddr_t2_f8() **[]*context.Context { return &r.f8 }")
	// log.Printf("registrations: %s", registrations)

	r.Rewrite(ModeReload)

	funcEquals := func(stubVar, funcValue string) {
		t.Helper()
		output := formatNode(t, pkg.Fset, r.NewFunc[r.Pkgs[0].PkgPath][stubVar])
		assert.Equal(t, funcValue, output)
	}

	funcEquals("GRLfvar_F", "func() { *GRLuaddr_v3() = V4 }")
	funcEquals("GRLfvar_F2", "func() { V4 = *GRLuaddr_v3() V4 = *GRLuaddr_v3() + 5 V4 = 6 + *GRLuaddr_v3() }")
	funcEquals("GRLfvar_F3", "func() { var v_t2 GRLt_t2 V4 = *v_t2.GRLmaddr_t2_f1() }")
	funcEquals("GRLfvar_F4", `func() { fmt.Printf("%v %p %v %p", *GRLuaddr_v3(), GRLuaddr_v3(), V4, &V4) }`)
	funcEquals("GRLfvar_F5", "func() { GRLuaddr_v6().Lock() }")
	funcEquals("GRLfvar_F6", "func() { **GRLuaddr_v7() += 0.1 }")
	funcEquals("GRLfvar_F7", "func(ctx ContextAlias) { <-ctx.Done() var ctx2 ContextAlias _ = ctx2 }")
	funcEquals("GRLfvar_F8", "func(a int, b float32) (int, float32) { return a, b }")

	if false {
		// What should the rewritten target_func() look like, ast-wise?
		fs := token.NewFileSet()
		targetNode, err := parser.ParseFile(fs, "target", targetFile, parser.SkipObjectResolution)
		require.NoError(t, err)
		require.NotNil(t, targetNode)
		buf := bytes.Buffer{}
		ast.Fprint(&buf, fs, targetNode, ast.NotNilFilter)
		t.Logf("target:\n%s", buf.String())
	}

	r, pkg, _, output, _, registrations = rewrite(`
import (
	"github.com/got-reload/got-reload/pkg/fake/dup1/dup"
)

var X dup.I
`)
	dupPkg := pkg.Imports["github.com/got-reload/got-reload/pkg/fake/dup1/dup"]
	byt, err := extract.GenContent(
		pkg.Name, dupPkg.Name, dupPkg.PkgPath, dupPkg.Types,
		nil, nil, nil, nil, nil)
	require.NoError(t, err)
	// t.Logf("dup registrations:\n%s", byt)
	registrations = filterWhitespace(byt)
	assert.Contains(t, registrations, `"I": reflect.ValueOf((*dup.I)(nil)),`)
	assert.Contains(t, registrations, `WF func() dup_0.SomeType`)

	// assert.NotContains(registrations, "T2 = T2")
	// assert.Contains(registrations, "type GRLt_t3 = t3")

	// assert.Contains(registrations, "func GRLuaddr_v1() *int { return &v1 }")

	// So(registrations, ShouldContainSubstring, "var GRLfvar_f1 = f1")

	// assert.Contains(registrations, "func (r *t3) GRLmaddr_t3_t3f1() *int { return &r.t3f1 }")

	// Printf("%s", string(r.Info[pkg].Registrations))

	// So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[0].Names[0].Name,
	// 	ShouldEqual, exportPrefix+"t3f1")
	// So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[1].Names[0].Name,
	// 	ShouldEqual, "T3f2")
	// So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[2].Names[0].Name,
	// 	ShouldEqual, "T3f3")
	// So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[3].Names[0].Name,
	// 	ShouldEqual, "T3f4")
	// So(types[exportPrefix+"t3"].(*ast.StructType).Fields.List[3].Type.(*ast.Ident).Name,
	// 	ShouldEqual, exportPrefix+"uint32")
	// So(types, ShouldContainKey, "T4")
	// So(types["T4"].(*ast.StructType).Fields.List[0].Names[0].Name,
	// 	ShouldEqual, exportPrefix+"t4f1")
	// So(types, ShouldContainKey, exportPrefix+"t5")
	// So(types[exportPrefix+"t5"].(*ast.InterfaceType).Methods.List[0].Names[0].Name,
	// 	ShouldEqual, exportPrefix+"t5m1")
	// So(types[exportPrefix+"t5"].(*ast.InterfaceType).Methods.List[1].
	// 	Type.(*ast.FuncType).Params.List[0].Type.(*ast.Ident).Name,
	// 	ShouldEqual, exportPrefix+"t3")
	// So(types[exportPrefix+"t5"].(*ast.InterfaceType).Methods.List[2].
	// 	Type.(*ast.FuncType).Params.List[0].Type.(*ast.Ident).Name,
	// 	ShouldEqual, exportPrefix+"t3")
	// So(types[exportPrefix+"t5"].(*ast.InterfaceType).Methods.List[3].
	// 	Type.(*ast.FuncType).Results.List[0].Type.(*ast.Ident).Name,
	// 	ShouldEqual, exportPrefix+"t3")
	// So(types, ShouldContainKey, "T6")
	// So(types, ShouldContainKey, exportPrefix+"uint32")

	// vars := getVars(file)
	// So(vars, ShouldContainKey, exportPrefix+"v1")
	// So(vars, ShouldContainKey, exportPrefix+"v3")
	// So(vars[exportPrefix+"v3"].(*ast.Ident).Name, ShouldEqual, exportPrefix+"t1")
	// So(vars, ShouldContainKey, "V4")
	// So(vars["V4"].(*ast.Ident).Name, ShouldEqual, exportPrefix+"t1")
	// So(vars, ShouldContainKey, exportPrefix+"v5")
	// So(vars[exportPrefix+"v5"].(*ast.Ident).Name, ShouldEqual, "T2")
	// So(vars, ShouldContainKey, exportPrefix+"v6")
	// So(vars, ShouldContainKey, exportPrefix+"v8")
	// So(vars[exportPrefix+"v8"].(*ast.Ident).Name, ShouldEqual, exportPrefix+"t3")
	// So(vars, ShouldContainKey, "V10")
	// So(vars["V10"].(*ast.Ident).Name, ShouldEqual,
	// 	exportPrefix+"uint32")
	// So(vars, ShouldContainKey, "v11")
	// So(vars, ShouldNotContainKey, exportPrefix+"v11")

	// funcs := getFuncs(file)
	// So(funcs, ShouldContainKey, exportPrefix+"f1")
	// So(funcs[exportPrefix+"f1"].Body.List[0], ShouldHaveSameTypeAs, &ast.ReturnStmt{})
	// So(funcs, ShouldContainKey, setPrefix+"f1")
	// So(funcs, ShouldContainKey, "F2")
	// So(funcs, ShouldContainKey, setPrefix+"F2")
	// So(funcs, ShouldContainKey, exportPrefix+"t3m1")
	// So(funcs, ShouldContainKey, "T4m1")
	// So(funcs, ShouldContainKey, exportPrefix+"t5m1")
}

func formatNode(t *testing.T, fset *token.FileSet, node ast.Node) string {
	if node == nil || node == (*ast.FuncLit)(nil) {
		return ""
	}

	t.Helper()
	b := &bytes.Buffer{}
	err := format.Node(b, fset, node)
	require.NoError(t, err)
	return filterWhitespace(b.String())
}

// // Do some diagnostics (maybe) even if tests fail.
// defer func() {
// 	if false {
// 		// General diagnostics.
// 		var buf bytes.Buffer

// 		// Formatted output file
// 		err = format.Node(&buf, pkg.Fset, file)
// 		require.NoError(err)
// 		t.Logf("%s", buf.String())
// 		// // File AST
// 		// buf = bytes.Buffer{}
// 		// ast.Fprint(&buf, pkg.Fset, file, ast.NotNilFilter)
// 		// Printf("%s", buf.String())

// 		// What should the rewritten target_func() look like, ast-wise?
// 		targetNode, err := parser.ParseFile(pkg.Fset, "target", targetFile, 0)
// 		require.NoError(err)
// 		require.NotNil(targetNode)
// 		buf = bytes.Buffer{}
// 		ast.Fprint(&buf, pkg.Fset, targetNode, ast.NotNilFilter)
// 		t.Logf("target: %s", buf.String())
// 	}
// }()

// Needs *a lot* of updating.
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
	file, err := os.CreateTemp("", "hotreload-*.go")
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

type testType struct {
	f1 int
	F2 int
}

func (tt *testType) m1() int {
	return tt.f1
}

func (tt *testType) m2() int {
	return tt.F2
}

func TestValueOfMethod(t *testing.T) {
	v := reflect.ValueOf((*testType).m1)
	tt := testType{f1: 5}

	// Does ValueOf a method do what I think it does?
	t.Run("ValueOf", func(t *testing.T) {
		require.NotNil(t, v)
		assert.IsType(t, (*testType).m1, v.Interface())
		assert.IsType(t, func(*testType) int { return 0 }, v.Interface())
	})

	t.Run("eval ValueOf", func(t *testing.T) {
		i := interp.New(interp.Options{})
		i.Use(interp.Exports{
			"pkg/pkg": {
				"Tt":          reflect.ValueOf(&tt).Elem(),
				"TestType":    reflect.ValueOf((*testType)(nil)),
				"TestType_m1": reflect.ValueOf((*testType).m1),
			},
		})
		i.ImportUsed()

		_, err := i.Eval(`import . "pkg"`)
		require.NoError(t, err)

		res, err := i.Eval("TestType_m1(&Tt)")
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, 5, res.Interface())
	})

	t.Run("create struct instance", func(t *testing.T) {
		i := interp.New(interp.Options{})
		i.Use(interp.Exports{
			"pkg/pkg": {
				"Tt":          reflect.ValueOf(&tt).Elem(),
				"TestType":    reflect.ValueOf((*testType)(nil)),
				"TestType_m1": reflect.ValueOf((*testType).m1),
				"TestType_m2": reflect.ValueOf((*testType).m2),
			},
		})
		i.ImportUsed()

		_, err := i.Eval(`import . "pkg"`)
		require.NoError(t, err)

		// expected to fail: reflect: reflect.Value.Set using value obtained
		// using unexported field
		require.NotPanics(t, func() {
			_, err = i.Eval("t2 := TestType{f1: 5} ; TestType_m1(&t2)")
		})
		require.Error(t, err)
	})
}

func _TestTypeAlias(t *testing.T) {
	type (
		I = int
		F = float32
	)

	i := interp.New(interp.Options{})
	i.Use(interp.Exports{
		"pkg/pkg": {
			"I": reflect.ValueOf((*I)(nil)),
			"F": reflect.ValueOf((*F)(nil)),
		},
	})
	i.ImportUsed()

	_, err := i.Eval("func foo(i I) pkg.F { return pkg.F(i) }")
	require.NoError(t, err)

	res, err := i.Eval("foo(5)")
	require.NoError(t, err)
	assert.Equal(t, float32(5.0), res.Interface())
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
