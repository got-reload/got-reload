package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/huckridgesw/hot-reload/pkg/hotreload"
)

type ExitCode int

const (
	Success ExitCode = iota
	FailedOpenSrc
	FailedOpenDest
	FailedCloseDest
	FailedRead
	FailedWrite
	FailedParse
	FailedTruncate
)

// TODO: only do something if we're being invoked as part of the act
// of compiling source code. Otherwise, just exit success.
// TODO: find the actual file name in the compiler command, using the
// first arg like this is easy to test manually, but objectively wrong
// for interoperating with the compiler flags.
func main() {
	outPath := os.Args[1]
	fset := token.NewFileSet()
	files := map[string]ast.Node{}
	for _, filename := range os.Args[2:] {
		// fmt.Printf("Parsing %s\n", filename)
		node, err := parser.ParseFile(fset, filename, nil, 0)
		if err != nil {
			fmt.Printf("Parsing error in %s: %v\n", filename, err)
			continue
		}
		files[filename] = node
	}

	// fmt.Printf("Starting rewrite\n")
	for _, filename := range os.Args[2:] {
		files[filename] = hotreload.Rewrite(files[filename])
	}
	// fmt.Printf("Finished rewrite\n")

	// pkg, err := ast.NewPackage(fset, files, nil, nil)
	// if err != nil {
	// 	fmt.Printf("NewPackage error: %v", err)
	// 	return
	// }
	// bigFile := ast.MergePackageFiles(pkg, ast.FilterFuncDuplicates|ast.FilterUnassociatedComments|ast.FilterImportDuplicates)
	// nodes := hotreload.Rewrite(bigFile)

	// outputFile, err := ioutil.TempFile("", "hotreloadable-*-"+filename)
	// if err != nil {
	// 	log.Printf("Failed opening dest file: %v", err)
	// 	os.Exit(int(FailedOpenDest))
	// }
	// outFileName := outputFile.Name()
	// defer func() {
	// 	if err := outputFile.Close(); err != nil {
	// 		log.Printf("Failed closing file: %v", err)
	// 		os.Exit(int(FailedCloseDest))
	// 	}
	// 	log.Printf("Written to %s", outFileName)
	// }()
	// if err := format.Node(outputFile, token.NewFileSet(), nodes); err != nil {
	// 	log.Printf("Failed formatting results: %v", err)
	// 	os.Exit(int(FailedWrite))
	// }

	for _, filename := range os.Args[2:] {
		// fmt.Printf("Printing %s\n", filename)
		buf := bytes.Buffer{}
		if err := format.Node(&buf, fset, files[filename]); err != nil {
			fmt.Printf("Error formatting %s: %v\n", filename, err)
			continue
		}
		ioutil.WriteFile(filepath.Join(outPath, filename), buf.Bytes(), 0600)
	}
}
