package main

import (
	"bytes"
	"context"
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

	"github.com/huckridgesw/got-reload/pkg/gotreload"
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

func runAsSubprocess(version string, command []string) error {
	cmd, args := command[0], command[1:]
	subprocess := exec.Command(cmd, args...)
	versionCheck := contains("-V=full", args)
	// must hook up I/O streams so that the stdout of the compiler
	// can return its tool id as per this:
	// https://github.com/golang/go/blob/953d1feca9b21af075ad5fc8a3dad096d3ccc3a0/src/cmd/go/internal/work/buildid.go#L119
	subprocess.Stderr = os.Stderr
	subprocess.Stdout = os.Stdout
	subprocess.Stdin = os.Stdin
	var b bytes.Buffer
	if versionCheck {
		subprocess.Stdout = &b
	}
	log.Printf("Running command: %v", command)
	err := subprocess.Run()
	if versionCheck {
		parts := strings.Fields(b.String())
		parts[len(parts)-1] = parts[len(parts)-1] + version
		result := strings.Join(parts, " ")
		fmt.Fprintln(os.Stdout, result)
		log.Printf("Emitting version %s", result)
	}
	return err
}

const (
	subcommandToolexec = "toolexec"
	subcommandRun      = "run"
)

var subcommands = map[string]func(selfName string, args []string){
	subcommandToolexec: toolexec,
	subcommandRun:      run,
}

func toolexec(selfName string, args []string) {
	var packages string
	set := flag.NewFlagSet(selfName, flag.ExitOnError)
	set.StringVar(&packages, "pkgs", "", "The comma-delimited list of packages to enable for hot reload")
	set.StringVar(&packages, "p", "", "Short form of \"-pkgs\"")
	set.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), Usage, selfName, argListDelimiter)
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		set.Usage()
		os.Exit(1)
	}
	if len(packages) < 1 {
		log.Fatal("No packages specified")
	}

	boundary := indexOf(argListDelimiter, os.Args)
	if boundary < 0 {
		log.Fatalf("Must provide %s in args", argListDelimiter)
	}

	var intendedCommand []string
	args, intendedCommand = splitAt(argListDelimiter, args)

	finishAsNormal := func() {
		// use the list of hot packages as a version to ensure that we recompile packages each
		// time we change the list of hot-reloadable packages.
		if err := runAsSubprocess(packages, intendedCommand); err != nil {
			exitError := new(exec.ExitError)
			if errors.As(err, &exitError) {
				os.Exit(exitError.ExitCode())
			}
		}
		os.Exit(int(Success))
	}

	if !strings.HasSuffix(intendedCommand[0], "compile") {
		//log.Println("Not compiling")
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
		log.Printf("Not target package (package=%s, targets=%v), compiling normally", packageName, packages)
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

func run(selfName string, args []string) {
	var packages string
	set := flag.NewFlagSet(selfName, flag.ExitOnError)
	set.StringVar(&packages, "pkgs", "", "The comma-delimited list of packages to enable for hot reload")
	set.StringVar(&packages, "p", "", "Short form of \"-pkgs\"")
	set.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `%[1]s

%[1]s [flags] <package> [<package> ...]

Flags:

`, selfName)
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		set.Usage()
		os.Exit(1)
	}
	runPackage := set.Arg(0)

	packageList := strings.Split(packages, ",")
	if len(packageList) < 1 {
		log.Fatal("No hot-reload packages specified")
	}

	absExecutable, err := filepath.Abs(os.Args[0])
	if err != nil {
		log.Fatalf("Unable to find gotreload helper program: %v", err)
	}

	toolexecValue := absExecutable + " " + subcommandToolexec + " -p " + packages + " " + argListDelimiter
	cmd := exec.CommandContext(context.TODO(), "go", "run", "-toolexec", toolexecValue, runPackage)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

func main() {
	log.SetFlags(log.Lshortfile)
	executable := filepath.Base(os.Args[0])
	flag.Usage = func() {
		var subcommandList []string
		for cmd := range subcommands {
			subcommandList = append(subcommandList, cmd)
		}
		fmt.Fprintf(flag.CommandLine.Output(), `%[1]s:

%[1]s <subcommand> [flags]

Where subcommand is one of: %v

`, executable, subcommandList)
		flag.PrintDefaults()
	}
	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(1)
	}
	subcommand := os.Args[1]
	os.Args = append([]string{executable}, os.Args[2:]...)
	if cmd, ok := subcommands[subcommand]; !ok {
		flag.Usage()
		log.Fatalf("Unrecognized subcommand: %s", subcommand)
	} else {
		cmd(executable+" "+subcommand, os.Args[1:])
	}
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
	nodes, err := gotreload.Parse(targetFileName, string(source))
	if err != nil {
		return "", fmt.Errorf("failed parsing %s: %w", targetFileName, err)
	}
	nodes = gotreload.Rewrite(nodes)

	outputFile, err := ioutil.TempFile("", "gotreloadable-*-"+filepath.Base(targetFileName))
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