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

func TestEvalInPackage(t *testing.T) {
	i := interp.New(interp.Options{BuildTags: []string{"yaegi_test"}})
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
}

var notExported int = 5

func TestCompileParse(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	path := path.Dir(cwd) + "/fake"
	// t.Logf("Getwd: %s, %v", cwd, err)

	rewrite := func(src string) (*Rewriter, *packages.Package, string, string) {
		t.Helper()
		// t.Logf("rewrite: package fake; %s", src)
		r := NewRewriter()

		// r.Config.Logf = t.Logf
		// // This is handy for knowing what is being parsed and by what name.
		// r.Config.ParseFile = func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
		// 	t.Logf("ParseFile: %s\n", filename)
		// 	return parser.ParseFile(fset, filename, src, 0)
		// }

		// Replace the fake t1 with our testfile data.
		r.Config.Overlay = map[string][]byte{
			path + "/t1.go": []byte("package fake; " + src),
			path + "/t2.go": []byte("package fake"),
		}

		err := r.Load("../fake")
		require.NoError(t, err)
		require.NotNil(t, r.Pkgs)

		err = r.Rewrite(ModeRewrite, true)
		require.NoError(t, err)
		require.NotNil(t, r.Pkgs)
		require.NotZero(t, len(r.Pkgs))
		pkg := r.Pkgs[0]

		require.NotZero(t, len(pkg.Syntax))
		file := pkg.Syntax[0]
		var registrations string
		if r.Info[pkg] != nil && len(r.Info[pkg].Registrations) > 0 {
			registrations = string(r.Info[pkg].Registrations)
		}

		b := &bytes.Buffer{}
		err = format.Node(b, pkg.Fset, file)
		require.NoError(t, err)
		output := b.String()
		// t.Logf("output:\n%s", output)

		return r, pkg, output, registrations
	}

	rewriteTrim := func(src string) (*Rewriter, *packages.Package, string, string) {
		r, pkg, output, registrations := rewrite(src)
		output = filterWhitespace(output)
		registrations = filterWhitespace(registrations)
		return r, pkg, output, registrations
	}

	{
		_, _, output, _ := rewriteTrim("type t1 int")
		// t.Logf("output:\n%s", output)

		// Types, variables, functions, and function bodies should translate correctly
		assert.Contains(t, output, "type GRLx_t1 int")
	}

	{
		_, _, output, registrations := rewriteTrim("func f(a int) int { return a }")
		assert.Contains(t, output, `func GRLx_f(a int) int { return GRLfvar_f(a) }`)
		assert.Contains(t, output, `var GRLfvar_f func(a int) int`)
		assert.Contains(t, output, `func init() { GRLfvar_f = func(a int) int { return a } }`)
		assert.Contains(t, registrations, `"GRLfvar_f": reflect.ValueOf(&GRLfvar_f).Elem()`)
		// t.Logf("registrations:\n%s", registrations)
		// t.Logf("output:\n%s", output)
	}

	{
		_, _, output, registrations := rewriteTrim(`type t1 int; var v3 t1`)
		assert.Contains(t, registrations, `"GRLx_v3": reflect.ValueOf(&GRLx_v3).Elem()`)
		assert.Contains(t, output, `type GRLx_t1 int var GRLx_v3 GRLx_t1`)
	}

	funcEquals := func(r *Rewriter, stubVar, expectedFuncValue string) {
		t.Helper()
		pkg := r.Pkgs[0]
		output := formatNode(t, pkg.Fset, r.NewFunc[pkg.PkgPath][stubVar])
		assert.Equal(t, expectedFuncValue, output)
	}

	{
		r, _, output, registrations := rewriteTrim(`
import (
	"fmt"
	"sync"
	"context"
	"sync/atomic"
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
	f9 atomic.Bool
}

func (r *t2) T2_method1() int {
	return 0
}

func (_ *t2) T2_method2() int { return 1 }
func (_ *t2) T2_method3(_, _, _ int) int { return 2 }

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

func F9(_ int, b float32, _ string) float32 { return b }
`)
		assert.Contains(t, output, "var GRLx_v3 GRLx_t1")
		assert.Contains(t, output, "var GRLx_v5 = new(float32)")
		assert.Contains(t, output, "var GRLx_v6 sync.Mutex")
		assert.Contains(t, output, "var GRLx_v8 = []int{}")
		assert.Contains(t, output, "var GRLx_v9 = (interface{})(2)")

		assert.Contains(t, registrations, `"GRLfvar_GRLx_t2_T2_method1": reflect.ValueOf(&GRLfvar_GRLx_t2_T2_method1).Elem(),`)
		assert.Contains(t, output, "var GRLfvar_GRLx_t2_T2_method1 func(r *GRLx_t2) int")
		assert.Contains(t, output, "func init() { GRLfvar_GRLx_t2_T2_method1 = func(r *GRLx_t2) int { return 0 } }")

		assert.Contains(t, output, "var GRLfvar_GRLx_t2_T2_method2 func(GRLrecvr *GRLx_t2) int")
		assert.Contains(t, output, "func init() { GRLfvar_GRLx_t2_T2_method2 = func(GRLrecvr *GRLx_t2) int { return 1 } }")

		assert.Contains(t, output, "func (GRLrecvr *GRLx_t2) T2_method3(GRLarg_0, GRLarg_1, GRLarg_2 int) int { return GRLfvar_GRLx_t2_T2_method3(GRLrecvr, GRLarg_0, GRLarg_1, GRLarg_2) }")
		assert.Contains(t, output, "var GRLfvar_GRLx_t2_T2_method3 func(GRLrecvr *GRLx_t2, _, _, _ int) int")
		assert.Contains(t, output, "func init() { GRLfvar_GRLx_t2_T2_method3 = func(GRLrecvr *GRLx_t2, _, _, _ int) int { return 2 } }")

		assert.Contains(t, registrations, `"M": reflect.ValueOf((*M)(nil))`)
		assert.Contains(t, registrations, `"ContextAlias": reflect.ValueOf((*ContextAlias)(nil))`)
		// t.Logf("registrations: %s", registrations)

		err := r.Rewrite(ModeReload, false)
		require.NoError(t, err)

		funcEquals(r, "GRLfvar_F", "func() { GRLx_v3 = V4 }")
		funcEquals(r, "GRLfvar_F2", "func() { V4 = GRLx_v3 V4 = GRLx_v3 + 5 V4 = 6 + GRLx_v3 }")
		funcEquals(r, "GRLfvar_F3", "func() { var v_t2 GRLx_t2 V4 = v_t2.GRLx_f1 }")
		funcEquals(r, "GRLfvar_F4", `func() { fmt.Printf("%v %p %v %p", GRLx_v3, &GRLx_v3, V4, &V4) }`)
		funcEquals(r, "GRLfvar_F5", "func() { GRLx_v6.Lock() }")
		funcEquals(r, "GRLfvar_F6", "func() { *GRLx_v7 += 0.1 }")
		funcEquals(r, "GRLfvar_F7", "func(ctx ContextAlias) { <-ctx.Done() var ctx2 ContextAlias _ = ctx2 }")
		funcEquals(r, "GRLfvar_F8", "func(a int, b float32) (int, float32) { return a, b }")
		// t.Logf("output: %s", output)
		assert.Contains(t, output, "func F9(GRLarg_0 int, b float32, GRLarg_2 string) float32 { return GRLfvar_F9(GRLarg_0, b, GRLarg_2) }")
		// We don't actually need synthetic arg names (GRLarg_) in the function
		// type, or the initial function literal.
		assert.Contains(t, output, "var GRLfvar_F9 func(_ int, b float32, _ string) float32")
		funcEquals(r, "GRLfvar_F9", "func(_ int, b float32, _ string) float32 { return b }")
	}

	{
		_, pkg, _, registrations := rewriteTrim(`
import (
	"github.com/got-reload/got-reload/pkg/fake/dup1/dup"
)

var X dup.I
`)
		dupPkg := pkg.Imports["github.com/got-reload/got-reload/pkg/fake/dup1/dup"]
		byt, err := extract.GenContent("github.com/got-reload/got-reload/pkg/fake",
			pkg.Name, dupPkg.PkgPath, dupPkg.Types,
			nil, nil, extract.NewImportTracker(pkg.Name, pkg.PkgPath))
		require.NoError(t, err)
		// t.Logf("dup registrations:\n%s", byt)
		registrations = filterWhitespace(byt)
		assert.Contains(t, registrations, `"I": reflect.ValueOf((*dup.I)(nil)),`)
		assert.Contains(t, registrations, `WF func() dup_0.SomeType`)
	}

	{
		_, _, output, _ := rewriteTrim(`
import (
	// reminder that the base pkg is called "fake". It should be referred to
	// w/out a package qualifier. This import is a different package and its
	// identifiers will need a package qualifier.
	"github.com/got-reload/got-reload/pkg/fake/dup3/fake"
)

type s struct {
	f1 fake.T
}
`)
		// t.Logf("output:\n%s", output)
		assert.Contains(t, output, "type GRLx_s struct { GRLx_f1 fake.T }")
	}

	{
		_, _, output, registrations := rewriteTrim(`
import (
	"github.com/got-reload/got-reload/pkg/fake/internal"
	"sync/atomic"
)

type T2 struct {
	f internal.T_thisIsInternal
}

func (t *T2) F(b atomic.Bool) internal.T_thisIsInternal { return t.f }
`)
		// t.Logf("registrations:\n%s", registrations)
		assert.Contains(t, registrations, `"GRLfvar_T2_F": reflect.ValueOf(&GRLfvar_T2_F).Elem()`)

		// t.Logf("output:\n%s", output)
		assert.Contains(t, output, "type T2 struct { GRLx_f internal.T_thisIsInternal }")
		assert.Contains(t, output, "func (t *T2) F(b atomic.Bool) internal.T_thisIsInternal { return GRLfvar_T2_F(t, b) }")
		assert.Contains(t, output, "var GRLfvar_T2_F func(t *T2, b atomic.Bool) internal.T_thisIsInternal")
		assert.Contains(t, output, "func init() { GRLfvar_T2_F = func(t *T2, b atomic.Bool) internal.T_thisIsInternal { return t.GRLx_f } }")

		// Change T2.F and reload
		//
		// This duplicates a subsection of reloader.Start and should probably be
		// refactored.
		newR := NewRewriter()
		newR.Config.Overlay = map[string][]byte{
			path + "/t1.go": []byte(`package fake
import (
	"github.com/got-reload/got-reload/pkg/fake/internal"
	"sync/atomic"
)

type T2 struct {
	f internal.T_thisIsInternal
}

func (t *T2) F(b atomic.Bool) internal.T_thisIsInternal { return t.f + 1 }
`),
			path + "/t2.go": []byte("package fake"),
		}
		newR.Load("../fake")

		err := newR.Rewrite(ModeRewrite, true)
		require.NoError(t, err)
		err = newR.Rewrite(ModeReload, false)
		require.NoError(t, err)
		assert.Contains(t, newR.NewFunc[newR.Pkgs[0].PkgPath], "GRLfvar_T2_F")
		// t.Logf("newR.NewFunc: %#v", newR.NewFunc)

		funcEquals(newR, "GRLfvar_T2_F", "func(t *T2, b atomic.Bool) GRLt_internal_T_thisIsInternal { return t.GRLx_f + 1 }")

		// TODO: There could of course be multiple types with the same basename
		// in *different* internal packages, sigh.
	}

	{
		_, _, output, registrations := rewriteTrim(`
func F() {
	type foo_bar struct {
		foo int
		bar float32
	}
}

type foo_baz struct { foo int; baz float32 }

`)
		// t.Logf("registrations:\n%s", registrations)
		assert.NotContains(t, registrations, `foo_bar`)
		assert.Contains(t, output, `type GRLx_foo_baz struct { GRLx_foo int GRLx_baz float32 }`)
		assert.Contains(t, registrations, `"GRLx_foo_baz": reflect.ValueOf((*GRLx_foo_baz)(nil))`)
	}

	// If you declare "type foo time.Time", make sure we don't generate
	// accessors for foo.wall and so on.
	{
		_, _, output, registrations := rewriteTrim(`import "time"; type myTime time.Time`)
		// t.Logf("registrations:\n%s", registrations)
		assert.NotContains(t, registrations, "func (r *myTime)")
		assert.Contains(t, output, "type GRLx_myTime time.Time")
	}

	{
		r, _, output, registrations := rewriteTrim(`
type S struct {
	key int
}

var m = map[int]int{}

func f(s S) int {
	return m[s.key]
}

var ch = make(chan int)

func f2() {
	ch <- 5
}

func f3() {
	for k, v := range m {
		_, _ = k, v
	}
}

var v int

func f4() int {
	v++
	return v
}

func f5(i int) S {
	return S{
		key: i,
	}
}

`)
		_, _ = output, registrations
		// t.Logf("registrations:\n%s", registrations)
		// t.Logf("output:\n%s", output)

		newR := NewRewriter()
		newR.Config = r.Config
		newR.Load("../fake")

		err := newR.Rewrite(ModeRewrite, true)
		require.NoError(t, err)
		err = newR.Rewrite(ModeReload, false)
		require.NoError(t, err)

		// t.Logf("reloaded output:\n%s", formatNode(t, newR.Pkgs[0].Fset, newR.Pkgs[0].Syntax[0]))

		funcEquals(newR, "GRLfvar_f", "func(s S) int { return GRLx_m[s.GRLx_key] }")
		funcEquals(newR, "GRLfvar_f2", "func() { GRLx_ch <- 5 }")
		funcEquals(newR, "GRLfvar_f3", "func() { for k, v := range GRLx_m { _, _ = k, v } }")
		funcEquals(newR, "GRLfvar_f4", "func() int { GRLx_v++ return GRLx_v }")
		funcEquals(newR, "GRLfvar_f5", "func(i int) S { return S{ GRLx_key: i, } }")

		// t.Logf("newR.NewFunc: %#v", newR.NewFunc)
	}

	// Make sure we don't rewrite init() functions.
	{
		_, _, output, _ := rewriteTrim(`func init() { panic("") }`)
		// t.Logf("registrations:\n%s", registrations)
		assert.NotContains(t, output, "func GRLx_init")
		assert.Contains(t, output, "func init()")
	}

	{
		_, _, output, registrations := rewriteTrim(`const i = 0`)
		// t.Logf("registrations:\n%s", registrations)
		assert.Contains(t, output, "const GRLx_i = 0")
		assert.Contains(t, registrations,
			`"GRLx_i": reflect.ValueOf(constant.MakeFromLiteral("0", token.INT, 0)),`)
	}

	{
		_, _, output, _ := rewriteTrim(`func f[T any](x T) T { return x }`)
		// t.Logf("registrations:\n%s", registrations)
		assert.Contains(t, output, "func f[T any](x T) T { return x }")
	}

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

var (
	// Parse this and print out its AST to figure out what to generate for the
	// rewritten functions.
	targetFile = mustFormat(`
package target

import "sync"
import "context"

func target_func(arg ...int) int {
	return arg[0]
}

func target_func2(arg ...int) int {
	return GRLfvar_target_func2(arg...)
}

var GRLfvar_target_func2 func(arg ...int) int

func init() {
	GRLfvar_target_func2 = func(arg ...int) int {
		return arg[0]
	}
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

type GRLt_t11 = t11

type t1 struct {
	f1 int
}

var v3, V4 int

var v5 sync.Mutex

var v7 = new(float32)

func F() { 
	v3 = V4
	V4 = v3
	V4 = v3 + 5

	var v_t1 t1

	v_t1.f1 = 5
	V4 = v_t1.f1

	fmt.Printf("%v, %p, %v, %p", v3, &v3, V4, &V4)

	v5.Lock()

	*v7 += 0.1

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

func example() {
	unexported_var = 1 // test set
	if unexported_var == 0 { // test get
	}


	var v T10
	dummy := v.unexported_var

	v.unexported_var = 1

	if v.unexported_var == 0 {}
}
`)
)
