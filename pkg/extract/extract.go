/*
This file is adapted from https://raw.githubusercontent.com/traefik/yaegi/master/extract/extract.go,
and is therefore under the terms of the Apache 2.0 License as is the rest of the yaegi project.
You can find their license terms here:
https://github.com/traefik/yaegi/blob/master/LICENSE
*/

/*
Package extract generates wrappers of package exported symbols.
*/
package extract

import (
	"bytes"
	"fmt"
	"go/constant"
	"go/types"
	"log"
	"math/big"
	"os"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/template"

	"github.com/got-reload/got-reload/pkg/util"
	goimports "golang.org/x/tools/imports"
)

const model = `
package {{.Dest}}

import (
{{- range $key, $value := .Imports }}
	{{$value}} "{{$key}}"
{{- end}}
	"reflect"
	"github.com/got-reload/got-reload/pkg/reloader"
	_ "github.com/got-reload/got-reload/pkg/reloader/start"
)

var _ = reloader.Add()

func init() {
	reloader.RegisterAll(map[string]map[string]reflect.Value{
    	"{{.ImportPath}}": {
		{{- if .Val}}
		// function, constant and variable definitions
		{{range $key, $value := .Val}}
			{{- if $value.Addr -}}
				"{{$key}}": reflect.ValueOf(&{{$value.Name}}).Elem(),
			{{else -}}
				"{{$key}}": reflect.ValueOf({{$value.Name}}),
			{{end -}}
		{{end}}
		{{- end -}}

		{{- if .Typ}}
		// type definitions
		{{range $key, $value := .Typ -}}
			"{{$key}}": reflect.ValueOf((*{{$value}})(nil)),
		{{end}}
		{{- end}}

		{{if .NeedsPublicType -}}
		// type aliases to export unexported or internal types
		{{range $unexportedName, $exportedName := .NeedsPublicType -}}
		"{{$exportedName}}": reflect.ValueOf((*{{$unexportedName}})(nil)),
		{{end}}
		{{- end}}

		{{- if .Wrap}}
		// interface wrapper definitions
		{{range $key, $value := .Wrap -}}
			"_{{$key}}": reflect.ValueOf((*{{$value.Name}})(nil)),
		{{end}}
		{{- end -}}
	},
	})
}
{{- if .Wrap }}
{{range $key, $value := .Wrap -}}
	// {{$value.Name}} is an interface wrapper for {{$key}} type
	type {{$value.Name}} struct {
		IValue interface{}
		{{range $m := $value.Method -}}
		W{{$m.Name}} func{{$m.Param}} {{$m.Result}}
		{{end}}
	}
	{{range $m := $value.Method -}}
		func (W {{$value.Name}}) {{$m.Name}}{{$m.Param}} {{$m.Result}} {
			{{- if and (eq $m.Name "String") (eq $m.Result "string") }}
			if W.WString == nil {
				return ""
			}
			{{end -}}
			{{$m.Ret}} W.W{{$m.Name}}{{$m.Arg}}
		}
	{{end}}
{{end}}
{{end}}

{{- if .NeedsPublicType }}
// Type aliases
{{range $unexportedName, $exportedName := .NeedsPublicType -}}
type {{$exportedName}} = {{$unexportedName}}
{{end}}
{{end}}
`

// May want this back
// {{range $name, $wrapper := .NeedsPublicFuncWrapper -}}
// var {{$wrapper}} = {{$name}}
// {{end}}

// Val stores the value name and addressable status of symbols.
type Val struct {
	Name string // "package.name"
	Addr bool   // true if symbol is a Var
}

// Method stores information for generating interface wrapper method.
type Method struct {
	Name, Param, Result, Arg, Ret string
}

// Wrap stores information for generating interface wrapper.
type Wrap struct {
	Name   string
	Method []Method
}

type FieldAccessor struct {
	*types.Var
	RType     string // receiver name
	AddrName  string // name of get-its-address func
	FieldType string // name of type of field
}

// restricted map defines symbols for which a special implementation is provided.
var restricted = map[string]bool{
	"osExit":        true,
	"osFindProcess": true,
	"logFatal":      true,
	"logFatalf":     true,
	"logFatalln":    true,
	"logLogger":     true,
	"logNew":        true,
}

func matchList(name string, list []string) (match bool, err error) {
	for _, re := range list {
		match, err = regexp.MatchString(re, name)
		if err != nil || match {
			return
		}
	}
	return
}

func GenContent(
	destPath, // for goimports call
	destPkg, importPath string,
	p *types.Package,
	setFuncs map[string]bool,
	needsPublicType map[string]string,
	imports *ImportTracker,
) ([]byte, error) {
	prefix := "_" + importPath + "_"
	prefix = strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(prefix)

	typ := map[string]string{}
	val := map[string]Val{}
	wrap := map[string]Wrap{}
	sc := p.Scope()

	qualify := func(pkg *types.Package) string {
		return imports.GetAlias(pkg.Name(), pkg.Path())
	}

	var skippedMethods []string

NAME:
	for _, name := range sc.Names() {
		o := sc.Lookup(name)

		pkgPrefix := imports.GetAlias(o.Pkg().Name(), o.Pkg().Path())
		if pkgPrefix != "" {
			pkgPrefix += "."
		}

		if !o.Exported() {
			if pkgPrefix == "" {
				// It's in this package: export it
				name = "GRLx_" + name
			} else {
				// It's not in this package: skip it
				continue
			}
		}
		// log.Printf("gencontent: %s, %#v", name, o)

		pname := name
		// LMC: Not sure what this is all about.  We don't import the package
		// that provides the custom implementation.
		// if rname := path.Base(importPath) + name; restricted[rname] {
		// 	// Restricted symbol, locally provided by stdlib wrapper.
		// 	pname = rname
		// }

		switch o := o.(type) {
		case *types.Const:
			if b, ok := o.Type().(*types.Basic); ok && (b.Info()&types.IsUntyped) != 0 {
				// Convert untyped constant to right type to avoid overflow.
				val[name] = Val{fixConst(pkgPrefix+pname, o.Val(), imports), false}
			} else {
				val[name] = Val{pkgPrefix + pname, false}
			}
		case *types.Func:
			// Skip generic functions and methods.
			if s := o.Type().(*types.Signature); s.TypeParams().Len() > 0 || s.RecvTypeParams().Len() > 0 {
				continue
			}
			val[name] = Val{pkgPrefix + pname, false}
		case *types.Var:
			val[name] = Val{pkgPrefix + pname, true}
		case *types.TypeName:
			// Skip type if it is generic.
			if t, ok := o.Type().(*types.Named); ok && t.TypeParams().Len() > 0 {
				continue
			}
			typ[name] = pkgPrefix + pname
			if t, ok := o.Type().Underlying().(*types.Interface); ok {
				if t.NumMethods() == 0 && t.NumEmbeddeds() != 0 {
					// Skip interfaces used to implement constraints for generics.
					delete(typ, name)
					continue
				}
				// log.Printf("type %s: %s: Underlying: %T, t.Underlying: %T",
				// 	name, typ[name], o.Type().Underlying(), t.Underlying())
				var methods []Method
			METHOD:
				for i := 0; i < t.NumMethods(); i++ {
					f := t.Method(i)
					fName := f.Name()
					if !f.Exported() {
						if pkgPrefix == "" {
							// If it's in this package, export it.
							fName = "GRLx_" + fName
						} else {
							continue
						}
					}

					sign := f.Type().(*types.Signature)
					args := make([]string, sign.Params().Len())
					params := make([]string, len(args))
					for j := range args {
						v := sign.Params().At(j)
						if args[j] = v.Name(); args[j] == "" {
							args[j] = fmt.Sprintf("a%d", j)
						}
						// process interface method variadic parameter
						if sign.Variadic() && j == len(args)-1 { // check is last arg
							// only replace the first "[]" to "..."
							at := types.TypeString(v.Type(), qualify)[2:]
							params[j] = args[j] + " ..." + at
							args[j] += "..."
						} else {
							if n, ok := v.Type().(*types.Named); ok {
								// If a method type is "internal", skip the method,
								// and don't import the type's package.
								if pkg := n.Obj().Pkg(); pkg != nil && util.InternalPkg(pkg.Path()) {
									// TODO: Not sure this error prints the actual
									// thing it's upset about.
									//
									// Also not sure if we shouldn't just skip the
									// whole interface, if we can't wrap every method
									// in it.
									// log.Printf("WARNING: %s: Skipping method %s.%s; this may impact what interfaces this type implements",
									// 	n.Obj().Name(), typ[name], fName)
									skippedMethods = append(skippedMethods, typ[name])

									// Not sure I need this, given that I essentially
									// run "goimports" later.
									imports.NoImport(pkg.Name(), pkg.Path())
									continue METHOD
								}
							}
							params[j] = args[j] + " " + types.TypeString(v.Type(), qualify)
						}
					}
					arg := "(" + strings.Join(args, ", ") + ")"
					param := "(" + strings.Join(params, ", ") + ")"

					hasInternal := false
					qualify2 := func(pkg *types.Package) string {
						hasInternal = hasInternal || util.InternalPkg(pkg.Path())
						return qualify(pkg)
					}

					results := make([]string, sign.Results().Len())
					for j := range results {
						v := sign.Results().At(j)
						results[j] = v.Name() + " " + types.TypeString(v.Type(), qualify2)
					}
					result := "(" + strings.Join(results, ", ") + ")"

					if hasInternal {
						continue NAME
					}

					ret := ""
					if sign.Results().Len() > 0 {
						ret = "return"
					}

					methods = append(methods, Method{fName, param, result, arg, ret})
				}
				wrap[name] = Wrap{prefix + name, methods}
			}
		}
	}
	if len(skippedMethods) > 0 {
		log.Printf("WARNING: Skipped methods on these types: %v", skippedMethods)
	}

	// Create a val slot for all the generated stubVar functions (GRLfvar_XXX),
	// just like *types.Func above.
	for name := range setFuncs {
		val[name] = Val{name, true}
	}

	if len(val) == 0 && len(typ) == 0 && len(needsPublicType) == 0 {
		log.Printf("No vals or types or public types, etc: %s, %s", destPkg, importPath)
		return nil, nil
	}

	// Generate buildTags with Go version only for stdlib packages.
	// Third party packages do not depend on Go compiler version by default.
	var buildTags string
	if isInStdlib(importPath) {
		var err error
		buildTags, err = genBuildTags()
		if err != nil {
			return nil, err
		}
	}

	base := template.New("extract")
	parse, err := base.Parse(model)
	if err != nil {
		return nil, fmt.Errorf("template parsing error: %w", err)
	}

	if importPath == "log/syslog" {
		buildTags += ",!windows,!nacl,!plan9"
	}

	if importPath == "syscall" {
		// As per https://golang.org/cmd/go/#hdr-Build_constraints,
		// using GOOS=android also matches tags and files for GOOS=linux,
		// so exclude it explicitly to avoid collisions (issue #843).
		// Also using GOOS=illumos matches tags and files for GOOS=solaris.
		switch os.Getenv("GOOS") {
		case "android":
			buildTags += ",!linux"
		case "illumos":
			buildTags += ",!solaris"
		}
	}

	_, pkgName := path.Split(importPath)
	b := new(bytes.Buffer)
	data := map[string]interface{}{
		"Dest":            destPkg,
		"Imports":         imports.alias,
		"ImportPath":      importPath + "/" + pkgName,
		"Val":             val,
		"Typ":             typ,
		"Wrap":            wrap,
		"BuildTags":       buildTags,
		"NeedsPublicType": needsPublicType,
	}
	err = parse.Execute(b, data)
	if err != nil {
		return nil, fmt.Errorf("template error: %w", err)
	}

	// goimports, natch
	source, err := goimports.Process(destPath, b.Bytes(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to 'goimports' source: %w: %s", err, b.Bytes())
	}

	return source, nil
}

type ImportTracker struct {
	seq                int
	baseName, basePath string
	usedName           map[string]bool   // key is package name or alias
	alias              map[string]string // import path => alias (can be "")
}

func NewImportTracker(name, path string) *ImportTracker {
	it := &ImportTracker{
		baseName: name,
		basePath: path,
		usedName: map[string]bool{},
		alias:    map[string]string{},
	}

	// pre-load with the base package
	_ = it.GetAlias(name, path)

	return it
}

func (it *ImportTracker) GetAlias(name, path string) (retS string) {
	// defer func() {
	// 	log.Printf("getAlias: %s, %s => %q", name, path, retS)
	// }()
	if path == it.basePath {
		return ""
	}
	if alias, ok := it.alias[path]; ok {
		if alias == "" {
			return name
		}
		return alias
	}

	// New package. Have we seen this pkg name already?
	if it.usedName[name] {
		alias := fmt.Sprintf("%s_%d", name, it.seq)
		it.seq++
		it.usedName[alias] = true
		it.alias[path] = alias
		return alias
	}
	it.usedName[name] = true
	it.alias[path] = ""
	return name
}

func (it *ImportTracker) NoImport(name, path string) {
	delete(it.alias, path)
	delete(it.usedName, name)
}

// fixConst checks untyped constant value, converting it if necessary to avoid overflow.
func fixConst(name string, val constant.Value, it *ImportTracker) string {
	var (
		tok string
		str string
	)
	switch val.Kind() {
	case constant.String:
		tok = "STRING"
		str = val.ExactString()
	case constant.Int:
		tok = "INT"
		str = val.ExactString()
	case constant.Float:
		v := constant.Val(val) // v is *big.Rat or *big.Float
		f, ok := v.(*big.Float)
		if !ok {
			f = new(big.Float).SetRat(v.(*big.Rat))
		}

		tok = "FLOAT"
		str = f.Text('g', int(f.Prec()))
	case constant.Complex:
		// TODO: not sure how to parse this case
		fallthrough
	default:
		return name
	}

	constantAlias := it.GetAlias("constant", "go/constant")
	tokenAlias := it.GetAlias("token", "go/token")

	return fmt.Sprintf("%s.MakeFromLiteral(%q, %s.%s, 0)",
		constantAlias, str, tokenAlias, tok)
}

// GetMinor returns the minor part of the version number.
func GetMinor(part string) string {
	minor := part
	index := strings.Index(minor, "beta")
	if index < 0 {
		index = strings.Index(minor, "rc")
	}
	if index > 0 {
		minor = minor[:index]
	}

	return minor
}

const defaultMinorVersion = 15

func genBuildTags() (string, error) {
	version := runtime.Version()
	if strings.HasPrefix(version, "devel") {
		return "", fmt.Errorf("extracting only supported with stable releases of Go, not %v", version)
	}
	parts := strings.Split(version, ".")

	minorRaw := GetMinor(parts[1])

	currentGoVersion := parts[0] + "." + minorRaw

	minor, err := strconv.Atoi(minorRaw)
	if err != nil {
		return "", fmt.Errorf("failed to parse version: %w", err)
	}

	// Only append an upper bound if we are not on the latest go
	if minor >= defaultMinorVersion {
		return currentGoVersion, nil
	}

	nextGoVersion := parts[0] + "." + strconv.Itoa(minor+1)

	return currentGoVersion + ",!" + nextGoVersion, nil
}

func isInStdlib(path string) bool { return !strings.Contains(path, ".") }
