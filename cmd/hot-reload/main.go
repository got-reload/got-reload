package main

import (
	"flag"
	"fmt"
	"go/format"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

var Usage string = `%[1]s:

%[1]s [flags] -- <go compiler invocation>

This tool expects to be invoked by the go build toolchain. You can
insert it like so:

go build -toolexec '%[1]s --' .

You *must* provide the "--" to denote the boundary between flags to
%[1]s and the following go compiler invocation.

`

func main() {
	log.Println("initial args:", os.Args)
	boundary := -1
	for i := range os.Args {
		if os.Args[i] == "--" {
			boundary = i
			break
		}
	}
	if boundary < 0 {
		log.Fatal("Must provide -- in args")
	}
	intendedCommand := os.Args[boundary+1:]
	os.Args = os.Args[:boundary]
	var packages string
	flag.StringVar(&packages, "pkgs", "", "The comma-delimited list of packages to enable for hot reload")
	flag.StringVar(&packages, "p", "", "The comma-delimited list of packages to enable for hot reload")
	flag.Parse()
	if len(packages) < 1 {
		log.Fatal("No packages specified")
	}

	finishAsNormal := func() {
		cmd, args := intendedCommand[0], intendedCommand[1:]
		log.Printf("running cmd: %v %v", cmd, args)
		if err := exec.Command(cmd, args...).Run(); err != nil {
			log.Fatal("Subcommand failed:", err)
		}
		os.Exit(int(Success))
	}

	if !strings.HasSuffix(intendedCommand[0], "compile") {
		// we are not compiling, no rewriting to do
		finishAsNormal()
	}

	packageNameIndex := -1
	for i := range intendedCommand {
		if intendedCommand[i] == "-p" {
			packageNameIndex = i + 1
			break
		}
	}
	if packageNameIndex < 0 {
		// no package name in arguments, do not rewrite
		finishAsNormal()
	}
	packageName := intendedCommand[packageNameIndex]
	found := false
	for _, pkg := range strings.Split(packages, ",") {
		if pkg == packageName {
			found = true
			break
		}
	}
	if !found {
		// we are not rewriting this package
		finishAsNormal()
	}

	gofiles := map[string]string{}
	for _, arg := range intendedCommand {
		if filepath.Ext(arg) == ".go" {
			gofiles[arg] = ""
		}
	}

	for file := range gofiles {
		newName, err := rewrite(file)
		if err != nil {
			log.Fatalf("Failed rewriting file %s: %v", file, err)
		}
		gofiles[file] = newName
	}

	// substitute rewritten file names
	for i, arg := range intendedCommand {
		if filepath.Ext(arg) == ".go" {
			intendedCommand[i] = gofiles[arg]
		}
	}

	log.Printf("Final cmdline: %v", intendedCommand)
	finishAsNormal()
}

func rewrite(targetFileName string) (outputFileName string, err error) {
	targetFile, err := os.Open(targetFileName)
	if err != nil {
		return "", fmt.Errorf("failed opening source file: %w", err)
	}
	defer targetFile.Close()
	source, err := ioutil.ReadAll(targetFile)
	if err != nil {
		return "", fmt.Errorf("failed reading source file: %w", err)
	}
	nodes, err := hotreload.Parse(targetFileName, string(source))
	if err != nil {
		return "", fmt.Errorf("failed parsing %s: %w", targetFileName, err)
	}
	nodes = hotreload.Rewrite(nodes)

	outputFile, err := ioutil.TempFile("", "hotreloadable-*-"+targetFileName)
	if err != nil {
		return "", fmt.Errorf("failed opening dest file: %w", err)
	}
	outputFileName = outputFile.Name()
	defer func() {
		if closeerr := outputFile.Close(); closeerr != nil {
			if err == nil {
				// if we didn't fail for another reason, fail for this
				err = fmt.Errorf("failed closing file: %w", closeerr)
			}
		}
	}()
	if err := format.Node(outputFile, token.NewFileSet(), nodes); err != nil {
		return "", fmt.Errorf("failed formatting results: %w", err)
	}
	return
}
