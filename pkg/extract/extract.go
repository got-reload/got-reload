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
	"math/big"
	"os"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

const model = `

package registration

import (
{{- range $key, $value := .Imports }}
	{{- if $value}}
	"{{$key}}"
	{{- end}}
{{- end}}
	"reflect"
	"github.com/huckridgesw/got-reload/pkg/gotreload"
)

func init() {
	gotreload.RegisterAll(map[string]reflect.Value{
		{{- if .Val}}
		// function, constant and variable definitions
		{{range $key, $value := .Val -}}
			{{- if $value.Addr -}}
				"{{$key}}": reflect.ValueOf(&{{$value.Name}}).Elem(),
			{{else -}}
				"{{$key}}": reflect.ValueOf({{$value.Name}}),
			{{end -}}
		{{end}}

		{{- end}}
		{{- if .Typ}}
		// type definitions
		{{range $key, $value := .Typ -}}
			"{{$key}}": reflect.ValueOf((*{{$value}})(nil)),
		{{end}}

		{{- end}}
		{{- if .Wrap}}
		// interface wrapper definitions
		{{range $key, $value := .Wrap -}}
			"_{{$key}}": reflect.ValueOf((*{{$value.Name}})(nil)),
		{{end}}
		{{- end}}
	})
}
{{range $key, $value := .Wrap -}}
	// {{$value.Name}} is an interface wrapper for {{$key}} type
	type {{$value.Name}} struct {
		{{range $m := $value.Method -}}
		W{{$m.Name}} func{{$m.Param}} {{$m.Result}}
		{{end}}
	}
	{{range $m := $value.Method -}}
		func (W {{$value.Name}}) {{$m.Name}}{{$m.Param}} {{$m.Result}} { {{$m.Ret}} W.W{{$m.Name}}{{$m.Arg}} }
	{{end}}
{{end}}
`

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

func GenContent(importPath string, p *types.Package) ([]byte, error) {
	prefix := "_" + importPath + "_"
	prefix = strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(prefix)

	typ := map[string]string{}
	val := map[string]Val{}
	wrap := map[string]Wrap{}
	imports := map[string]bool{}
	sc := p.Scope()

	for _, pkg := range p.Imports() {
		imports[pkg.Path()] = false
	}
	qualify := func(pkg *types.Package) string {
		if pkg.Path() != importPath {
			imports[pkg.Path()] = true
		}
		return pkg.Name()
	}

	for _, name := range sc.Names() {
		o := sc.Lookup(name)
		if !o.Exported() {
			continue
		}

		pname := path.Base(importPath) + "." + name
		if rname := path.Base(importPath) + name; restricted[rname] {
			// Restricted symbol, locally provided by stdlib wrapper.
			pname = rname
		}

		switch o := o.(type) {
		case *types.Const:
			if b, ok := o.Type().(*types.Basic); ok && (b.Info()&types.IsUntyped) != 0 {
				// Convert untyped constant to right type to avoid overflow.
				val[name] = Val{fixConst(pname, o.Val(), imports), false}
			} else {
				val[name] = Val{pname, false}
			}
		case *types.Func:
			val[name] = Val{pname, false}
		case *types.Var:
			val[name] = Val{pname, true}
		case *types.TypeName:
			typ[name] = pname
			if t, ok := o.Type().Underlying().(*types.Interface); ok {
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
						params[j] = args[j] + " " + types.TypeString(v.Type(), qualify)
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
			}
		}
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

	b := new(bytes.Buffer)
	data := map[string]interface{}{
		"Imports":   imports,
		"PkgName":   importPath,
		"Val":       val,
		"Typ":       typ,
		"Wrap":      wrap,
		"BuildTags": buildTags,
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
