package main

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/types"
	"log"
	"os"

	"golang.org/x/tools/go/packages"
)

func main() {
	config := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo,
		Logf: log.Printf,
		Overlay: map[string][]byte{
			"/Users/lmc/src/goget/src/github.com/huckridgesw/got-reload/pkg/gotreload/fake/t1.go": []byte(`
package main

func foo() int {
	return bar()
}

func bar() int {
	return 0
}

func main() {}
`)},
	}
	pkgs, err := packages.Load(config, os.Args[1])
	if err != nil {
		fmt.Printf("Error loading package %s", os.Args[1])
		return
	}

	fmt.Printf("%p\n", types.Universe)
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			fmt.Println("No TypesInfo")
		} else {
			translated := map[types.Object]bool{}
			for ident, obj := range pkg.TypesInfo.Defs {
				if ident.Name == "_" ||
					obj == nil ||
					ast.IsExported(ident.Name) {
					continue
				}
				if obj.Parent() == nil {
					fmt.Printf("No parent: %#v\n", obj)
					continue
				}
				if obj.Parent().Parent() != types.Universe {
					continue
				}
				ident.Name = "YY_" + ident.Name
				translated[obj] = true
				// fmt.Printf("Ident %v, scope: %p\n", ident, obj.Parent())
				// fmt.Printf("Pkg: %s, TL: %v, Ident: %v => Obj %v\n", pkg.Name, isTopLevel, ident, obj)
			}
			for ident, obj := range pkg.TypesInfo.Uses {
				if translated[obj] {
					ident.Name = "YY_" + ident.Name
				}
			}
			for _, file := range pkg.Syntax {
				format.Node(os.Stdout, pkg.Fset, file)
			}
		}
	}

}
