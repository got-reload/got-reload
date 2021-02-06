package main

import (
	"errors"
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

%[1]s [flags] %[2]s <go compiler invocation>

This tool expects to be invoked by the go build toolchain. You can
insert it like so:

go build -toolexec '%[1]s %[2]s' .

You *must* provide the "%[2]s" to denote the boundary between flags to
%[1]s and the following go compiler invocation.

Flags:
`

func indexOf(target string, input []string) int {
	boundary := -1
	for i, element := range input {
		if element == target {
			boundary = i
			break
		}
	}
	return boundary
}

func splitAt(target string, input []string) ([]string, []string) {
	boundary := indexOf(target, input)
	if boundary == -1 || boundary == len(input) {
		return input, nil
	}
	return input[:boundary], input[boundary+1:]
}

func contains(target string, input []string) bool {
	return indexOf(target, input) != -1
}

const argListDelimiter = "--"

func runAsSubprocess(command []string) error {
	cmd, args := command[0], command[1:]
	subprocess := exec.Command(cmd, args...)
	// must hook up I/O streams so that the stdout of the compiler
	// can return its tool id as per this:
	// https://github.com/golang/go/blob/953d1feca9b21af075ad5fc8a3dad096d3ccc3a0/src/cmd/go/internal/work/buildid.go#L119
	subprocess.Stderr = os.Stderr
	subprocess.Stdout = os.Stdout
	subprocess.Stdin = os.Stdin
	log.Printf("Running command: %v", command)
	return subprocess.Run()
}

func main() {
	log.SetFlags(log.Lshortfile)
	var packages string
	flag.StringVar(&packages, "pkgs", "", "The comma-delimited list of packages to enable for hot reload")
	flag.StringVar(&packages, "p", "", "Short form of \"-pkgs\"")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), Usage, os.Args[0], argListDelimiter)
		flag.PrintDefaults()
	}
	flag.Parse()
	if len(packages) < 1 {
		log.Fatal("No packages specified")
	}

	boundary := indexOf(argListDelimiter, os.Args)
	if boundary < 0 {
		log.Fatalf("Must provide %s in args", argListDelimiter)
	}

	var intendedCommand []string
	os.Args, intendedCommand = splitAt(argListDelimiter, os.Args)

	finishAsNormal := func() {
		if err := runAsSubprocess(intendedCommand); err != nil {
			exitError := new(exec.ExitError)
			if errors.As(err, &exitError) {
				os.Exit(exitError.ExitCode())
			}
		}
		os.Exit(int(Success))
	}

	if !strings.HasSuffix(intendedCommand[0], "compile") {
		log.Println("Not compiling")
		// we are not compiling, no rewriting to do
		finishAsNormal()
	}

	packageNameIndex := indexOf("-p", intendedCommand) + 1
	if packageNameIndex < 0 {
		// no package name in arguments, do not rewrite
		log.Println("No package name found in compiler cmdline")
		finishAsNormal()
	}

	packageName := intendedCommand[packageNameIndex]
	if !contains(packageName, strings.Split(packages, ",")) {
		// we are not rewriting this package
		log.Println("Not target package, compiling normally")
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

	outputFile, err := ioutil.TempFile("", "hotreloadable-*-"+filepath.Base(targetFileName))
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
