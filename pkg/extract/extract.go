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
	"go/format"
	"go/types"
	"log"
	"math/big"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

const model = `

package {{.Dest}}

import (
{{- range $key, $value := .Imports }}
	{{- if $value}}
	"{{$key}}"
	{{- end}}
{{- end}}
	"reflect"
	"github.com/got-reload/got-reload/pkg/reloader"
	_ "github.com/got-reload/got-reload/pkg/reloader/start"
)

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
		{{- if .NeedsAccessor}}
		// accessor functions for unexported variables
		{{range $var, $ignored := .NeedsAccessor -}}
			"GRLuaddr_{{$var}}": reflect.ValueOf(GRLuaddr_{{$var}}),
		{{end}}
		{{- end}}
		{{- if .NeedsFieldAccessor}}
		// accessor methods for unexported struct fields
		{{range $name, $s := .NeedsFieldAccessor -}}
		{{range $type, $thing := $s -}}
			"{{$thing.AddrName}}": reflect.ValueOf({{$thing.AddrName}}),
		{{end}}
		{{- end}}
		{{- end}}

		{{- if .Typ}}
		// type definitions
		{{range $key, $value := .Typ -}}
			"{{$key}}": reflect.ValueOf((*{{$value}})(nil)),
		{{end}}
		{{- end}}

		{{if .NeedsPublicType -}}
		// type aliases to export unexported types
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
			{{- if eq $m.Name "String" }}
			if W.WString == nil {
				return ""
			}
			{{end -}}
			{{$m.Ret}} W.W{{$m.Name}}{{$m.Arg}}
		}
	{{end}}
{{end}}
{{end}}

{{- if .NeedsAccessor }}
// Accessor functions for unexported variables
{{range $var, $type := .NeedsAccessor -}}
func GRLuaddr_{{$var}}() *{{$type}} { return &{{$var}} }
{{end}}
{{end}}

{{- if .NeedsPublicType }}
// Type aliases
{{range $unexportedName, $exportedName := .NeedsPublicType -}}
type {{$exportedName}} = {{$unexportedName}}
{{end}}
{{end}}

{{- if .NeedsFieldAccessor }}
// Field accessors
{{range $name, $s := .NeedsFieldAccessor -}}
{{range $type, $thing := $s -}}
func (r *{{$thing.RType}}) {{$thing.AddrName}}() *{{$thing.FieldType}} { return &r.{{$name}} }
{{end}}
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
	destPkg, basePkgName, importPath string,
	p *types.Package,
	setFuncs map[string]bool,
	needsAccessor map[string]string,
	needsPublicType map[string]string,
	// needsPublicFuncWrapper map[string]string,
	needsFieldAccessor map[string]map[*types.Struct]FieldAccessor, // field name -> rtype name -> stuff
	imports map[string]bool,
) ([]byte, error) {
	prefix := "_" + importPath + "_"
	prefix = strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(prefix)

	typ := map[string]string{}
	val := map[string]Val{}
	wrap := map[string]Wrap{}
	sc := p.Scope()
	if imports == nil {
		imports = map[string]bool{}
	}

	// pkgSeen := map[string]string{}
	// pkgSeen[basePkgName] = importPath
	// pkgDup := map[string]bool{}
	importPathName := filepath.Base(importPath)
	for _, pkg := range p.Imports() {
		if pkg.Name() == importPathName {
			log.Printf("Duplicate pkg names: %s, %s: %s, %s", destPkg, importPath, pkg.Name(), pkg.Path())
			return nil, nil
		}

		// if prev, ok := pkgSeen[pkg.Name()]; ok {
		// 	log.Printf("Duplicate pkg names: %s, %s: %s, %s; prev: %s", destPkg, importPath, pkg.Name(), pkg.Path(), prev)
		// 	pkgDup[destPkg] = true
		// 	// return nil, nil
		// }
		// pkgSeen[pkg.Name()] = pkg.Path()
		if !imports[pkg.Path()] {
			// log.Printf("Setting pkg %s to not imported (1)", pkg.Path())
			imports[pkg.Path()] = false
		}
	}
	qualify := func(pkg *types.Package) string {
		if pkg.Path() != importPath {
			// log.Printf("Setting pkg %s to imported (2)", pkg.Path())
			imports[pkg.Path()] = true
		}
		return pkg.Name()
	}

	for _, name := range sc.Names() {
		o := sc.Lookup(name)
		if !o.Exported() {
			// log.Printf("%s is not imported, skipping it", name)
			continue
		}

		pkgPrefix := ""
		if pkgName := o.Pkg().Name(); destPkg != pkgName {
			// log.Printf("Setting pkg %s to imported (3)", importPath)
			imports[importPath] = true
			pkgPrefix = pkgName + "."
		}

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
				for i := 0; i < t.NumMethods(); i++ {
					f := t.Method(i)
					if !f.Exported() {
						continue
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
								// log.Printf("method arg %d type %T: %s, %s", j, v.Type(), types.TypeString(v.Type(), qualify),
								// 	n.Obj().Pkg().Path())

								// If a method type is "internal", skip the method,
								// and don't import the type's package.
								if strings.Contains(n.Obj().Pkg().Path(), "/internal/") {
									log.Printf("Internal path: %s", n.Obj().Pkg().Path())
									imports[n.Obj().Pkg().Path()] = false
									return nil, nil
									// continue NAME
								}

							} else {
								// log.Printf("method arg %d type %T: %s", j, v.Type(), types.TypeString(v.Type(), qualify))
							}
							params[j] = args[j] + " " + types.TypeString(v.Type(), qualify)
						}
					}
					arg := "(" + strings.Join(args, ", ") + ")"
					param := "(" + strings.Join(params, ", ") + ")"

					results := make([]string, sign.Results().Len())
					for j := range results {
						v := sign.Results().At(j)
						results[j] = v.Name() + " " + types.TypeString(v.Type(), qualify)
					}
					result := "(" + strings.Join(results, ", ") + ")"

					ret := ""
					if sign.Results().Len() > 0 {
						ret = "return"
					}

					methods = append(methods, Method{f.Name(), param, result, arg, ret})
				}
				wrap[name] = Wrap{prefix + name, methods}
			} else {
				// log.Printf("type %s: %s: Underlying: %T", name, typ[name], o.Type().Underlying())
			}
		}
	}

	// Create a val slot for all the generated stubVar functions (GRLfvar_XXX),
	// just like *types.Func above.
	for name := range setFuncs {
		val[name] = Val{name, true}
	}

	if len(val) == 0 && len(typ) == 0 &&
		len(needsAccessor) == 0 && len(needsPublicType) == 0 && len(needsFieldAccessor) == 0 {

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
	// log.Printf("GenContent: ImportPath: %s", importPath)
	data := map[string]interface{}{
		"Dest":            destPkg,
		"Imports":         imports,
		"ImportPath":      importPath + "/" + pkgName,
		"Val":             val,
		"Typ":             typ,
		"Wrap":            wrap,
		"BuildTags":       buildTags,
		"NeedsAccessor":   needsAccessor,
		"NeedsPublicType": needsPublicType,
		// "NeedsPublicFuncWrapper": needsPublicFuncWrapper,
		"NeedsFieldAccessor": needsFieldAccessor,
	}
	err = parse.Execute(b, data)
	if err != nil {
		return nil, fmt.Errorf("template error: %w", err)
	}

	// gofmt
	source, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to format source: %w: %s", err, b.Bytes())
	}
	return source, nil
}

// fixConst checks untyped constant value, converting it if necessary to avoid overflow.
func fixConst(name string, val constant.Value, imports map[string]bool) string {
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

	imports["go/constant"] = true
	imports["go/token"] = true

	return fmt.Sprintf("constant.MakeFromLiteral(%q, token.%s, 0)", str, tok)
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
