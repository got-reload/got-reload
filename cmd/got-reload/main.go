package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/got-reload/got-reload/pkg/dup"
	"github.com/got-reload/got-reload/pkg/extract"
	"github.com/got-reload/got-reload/pkg/gotreload"
	"github.com/got-reload/got-reload/pkg/reloader"
	"golang.org/x/tools/go/packages"
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
	FailedCleanup
)

var ToolexecUsage string = `%[1]s:

%[1]s filter [flags]

%[1]s [flags] %[2]s <go compiler invocation>

This tool expects to be invoked by the go build toolchain. You can
insert it like so:

go build -toolexec '%[1]s %[2]s' .

You *must* provide the "%[2]s" to denote the boundary between flags to
%[1]s and the following go compiler invocation.

Flags:
`

var FilterUsage string = `%[1]s filter -out <dir> package [package ...]

Filter the given packages, and write them to directory tree specified by -out.
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

func runAsSubprocess(version string, command []string, logIt bool) error {
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
	if logIt {
		log.Printf("Running command: %v", command)
	}
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
	subcommandFilter   = "filter"
)

var subcommands = map[string]func(selfName string, args []string){
	// subcommandToolexec: toolexec,
	subcommandRun:    run,
	subcommandFilter: filter,
}

func toolexec(selfName string, args []string) {
	panic("toolexec not supported")
	var packages string
	var keep bool
	set := flag.NewFlagSet(selfName, flag.ExitOnError)
	set.StringVar(&packages, "pkgs", "", "The comma-delimited list of packages to enable for hot reload")
	set.StringVar(&packages, "p", "", "Short form of \"-pkgs\"")
	set.BoolVar(&keep, "keep", false, "Keep all the filtered code")
	set.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), ToolexecUsage, selfName, argListDelimiter)
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		set.Usage()
		os.Exit(1)
	}
	if len(packages) < 1 {
		log.Fatal("No packages specified")
	}

	// log.Printf("toolexec called with: %v", args)

	boundary := indexOf(argListDelimiter, os.Args)
	if boundary < 0 {
		log.Fatalf("Must provide %s in args", argListDelimiter)
	}

	var intendedCommand []string
	args, intendedCommand = splitAt(argListDelimiter, args)

	var toDelete []string
	finishAsNormal := func(logIt bool) {
		// Use the list of hot packages as a version to ensure that we recompile
		// packages each time we change the list of hot-reloadable packages.
		if err := runAsSubprocess(packages, intendedCommand, logIt); err != nil {
			exitError := new(exec.ExitError)
			if errors.As(err, &exitError) {
				os.Exit(exitError.ExitCode())
			}
		}

		// Clean up all our generated *.go files.
		exitCode := Success
		if !keep {
			for _, f := range toDelete {
				err := os.Remove(f)
				if err != nil {
					exitCode = FailedCleanup
					fmt.Fprintf(os.Stderr, "Error removing %s: %v", f, err)
				}
			}
		}

		os.Exit(int(exitCode))
	}

	if !strings.HasSuffix(intendedCommand[0], "compile") {
		//log.Println("Not compiling")
		// we are not compiling, no rewriting to do
		finishAsNormal(false)
	}

	packageNameIndex := indexOf("-p", intendedCommand) + 1
	if packageNameIndex < 0 {
		// no package name in arguments, do not rewrite
		log.Println("No package name found in compiler cmdline")
		finishAsNormal(false)
	}

	packageName := intendedCommand[packageNameIndex]
	if !contains(packageName, strings.Split(packages, ",")) {
		// we are not rewriting this package
		// log.Printf("Not target package (package=%s, targets=%v), compiling normally", packageName, packages)
		finishAsNormal(false)
	}

	gofiles := map[string]string{}
	for _, arg := range intendedCommand {
		if filepath.Ext(arg) == ".go" {
			gofiles[arg] = ""
		}
	}

	// log.Printf("Parsing package %s", packageName)
	r := &gotreload.Rewriter{}
	err := r.Load(packageName)
	if err != nil {
		log.Fatalf("%v", err)
	}
	err = r.Rewrite(false)
	if err != nil {
		log.Fatalf("%v", err)
	}

	for file := range gofiles {
		newName, err := rewrite(r, file)
		if err != nil {
			log.Fatalf("Error rewriting file %s: %v", file, err)
		}
		toDelete = append(toDelete, newName)
		gofiles[file] = newName
	}
	registerFName, err := writeRegister(r)
	if err != nil {
		log.Fatalf("Error writing register files: %v", err)
	}
	toDelete = append(toDelete, registerFName)

	// Substitute rewritten file names, and save the position of the last one.
	var last int
	for i, arg := range intendedCommand {
		if filepath.Ext(arg) == ".go" {
			intendedCommand[i] = gofiles[arg]
			last = i
		}
	}

	// Insert our "register" filename after the last go file.
	intendedCommand = append(intendedCommand, "")
	if last < len(intendedCommand)-1 {
		copy(intendedCommand[last+2:], intendedCommand[last+1:])
	}
	intendedCommand[last+1] = registerFName

	// log.Printf("Final cmdline: %v", intendedCommand)
	finishAsNormal(true)
}

func run(selfName string, args []string) {
	var packages string
	var verbose, keep bool
	set := flag.NewFlagSet(selfName, flag.ExitOnError)
	set.StringVar(&packages, "pkgs", "", "The comma-delimited list of packages to enable for hot reload")
	set.StringVar(&packages, "p", "", "Short form of \"-pkgs\"")
	set.BoolVar(&verbose, "v", false, "Pass -v to \"go run\" command")
	set.BoolVar(&keep, "keep", false, "Keep all the filtered code")
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

	// This is either "got-reload" or whatever executable "go run" builds.  (I
	// mention this in part because if you search the source for "got-reload" I
	// feel like you should find it, since we (might) run it here!)
	absExecutable, err := os.Executable()
	if err == nil {
		_, err := os.Stat(absExecutable)
		if err != nil {
			log.Printf("%s does not exist; searching $PATH for %s and hoping for the best", absExecutable, os.Args[0])
			absExecutable = os.Args[0]
		}
	} else {
		log.Printf("Unable to derive absolute path for %s, using relative path and hoping for the best: %v", os.Args[0], err)
		absExecutable = os.Args[0]
	}

	// TODO(whereswaldon):
	// Procedure
	// - create temporary directory
	workDir, err := ioutil.TempDir("", "gotreload-*")
	if err != nil {
		log.Fatalf("Unable to create work directory: %v", err)
	}
	// - copy entire local module into temporary directory using dup.Copy
	path, err := goListSingle("-m", "-f", "{{.Dir}}")
	if err != nil {
		log.Fatalf("Unable to find module root: %v", err)
	}
	log.Printf("copying %s to %s", path, workDir)
	if err := dup.Copy(workDir, os.DirFS(path)); err != nil {
		log.Fatalf("Failed copying files to working dir: %v", err)
	}
	// - invoke filter command on that copy
	cmdArgs := append([]string{"filter", "-dir", workDir}, packageList...)
	if err := runWithIO(absExecutable, cmdArgs...); err != nil {
		log.Fatalf("Failed rewriting code: %v", err)
	}
	// - invoke go run on the filtered codebase
	os.Setenv(reloader.PackageListEnv, packages)
	os.Setenv(reloader.StartReloaderEnv, "1")
	paths, err := goListSingle("-f", "{{.Dir}} {{.ImportPath}}", runPackage)
	if err != nil {
		log.Fatalf("Could not resolve main package %s: %v", runPackage, err)
	}
	fsMainPath := strings.Fields(paths)[0]
	mainPath := strings.Fields(paths)[1]
	os.Setenv(reloader.SourceDirEnv, fsMainPath)

	for _, v := range os.Environ() {
		if strings.Contains(v, "GOT") {
			log.Println(v)
		}
	}

	if err := runWithIOIn(workDir, "go", "get", "./..."); err != nil {
		log.Fatalf("Failed running go get ./...: %v", err)
	}
	if err := runWithIOIn(workDir, "go", "run", "-v", string(mainPath)); err != nil {
		log.Fatalf("Failed running go run: %v", err)
	}
}

func goListSingle(flags ...string) (string, error) {
	args := append([]string{"list"}, flags...)
	mainPath, err := exec.Command("go", args...).Output()
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(string(mainPath))
	return out, nil
}

func runWithIO(cmd string, args ...string) error {
	return runWithIOIn("", cmd, args...)
}

func runWithIOIn(dir, cmd string, args ...string) error {
	runCmd := exec.Command(cmd, args...)
	runCmd.Dir = dir
	runCmd.Stdin = os.Stdin
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr
	return runCmd.Run()
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

func rewrite(r *gotreload.Rewriter, targetFileName string) (outputFileName string, err error) {
	// log.Printf("rewrite %s", targetFileName)

	fileNode, fset, err := r.LookupFile(targetFileName)
	if err != nil {
		return "", err
	}

	// log.Printf("Writing filtered version of %s", targetFileName)
	b := bytes.Buffer{}
	err = format.Node(&b, fset, fileNode)
	if err != nil {
		return "", fmt.Errorf("Error writing filtered version of %s: %w", targetFileName, err)
	}
	return writeTempFile(b.Bytes(), targetFileName)
}

// Write the grl_register.go file.
func writeRegister(r *gotreload.Rewriter) (outputFileNames string, err error) {
	// Since we only run toolexec on a single package, there will only be a
	// single package to in r.Pkgs.
	pkg := r.Pkgs[0]
	log.Printf("register for %s: %s", pkg.Name, string(r.Info[pkg].Registrations))
	outputFileName, err := writeTempFile(r.Info[pkg].Registrations, "grl_register.go")
	if err != nil {
		return "", err
	}
	return outputFileName, nil
}

func writeTempFile(source []byte, targetFileName string) (string, error) {
	outputFile, err := ioutil.TempFile("", "gotreloadable-*-"+filepath.Base(targetFileName))
	if err != nil {
		return "", fmt.Errorf("Error opening dest file: %w", err)
	}
	outputFileName := outputFile.Name()
	defer func() {
		if closeerr := outputFile.Close(); closeerr != nil {
			if err == nil {
				// if we didn't fail for another reason, fail for this
				err = fmt.Errorf("Error closing file: %w", closeerr)
			}
		}
	}()

	err = ioutil.WriteFile(outputFileName, source, 0600)
	if err != nil {
		return "", err
	}
	return outputFileName, nil
}

// Write the grl_dependencies file for each pkg.
func addDependencies(packageList []string) ([]string, error) {
	r := gotreload.Rewriter{}
	err := r.Load(packageList...)
	if err != nil {
		return nil, err
	}

	var deps []string

	for _, pkg := range r.Pkgs {
		if len(pkg.Syntax) == 0 {
			continue
		}
		file := pkg.Syntax[0]
		pkgName := file.Name.Name
		fileName := pkg.Fset.Position(file.Pos()).Filename
		dir := filepath.Dir(fileName)
		fullpath := filepath.Join(dir, "grl_dependencies.go")
		err := ioutil.WriteFile(fullpath,
			[]byte(fmt.Sprintf(`package %s

import (
	"reflect"

	"github.com/got-reload/got-reload/pkg/reloader"
	_ "github.com/got-reload/got-reload/pkg/reloader/start"
)

var (
	_ = reflect.ValueOf
	_ = reloader.RegisterAll
)
`, pkgName)),
			0600)
		if err != nil {
			return deps, fmt.Errorf("Error writing %s/grl_dependencies.go: %w", pkgName, err)
		}
		deps = append(deps, fullpath)
		// log.Printf("Wrote %s", fullpath)
	}

	return deps, nil
}

func filter(selfName string, args []string) {
	var outputDir string
	set := flag.NewFlagSet(selfName, flag.ExitOnError)
	set.StringVar(&outputDir, "dir", "", "The output directory for all filtered code")
	set.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), FilterUsage, selfName)
		set.PrintDefaults()
	}
	if err := set.Parse(args); err != nil {
		set.Usage()
		os.Exit(1)
	}
	if len(args) < 1 {
		log.Fatal("No packages specified")
	}
	if outputDir == "" {
		log.Fatal("No output directory specified")
	}

	pwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Could not get current directory: %v", err)
	}

	packageList := set.Args()

	// log.Printf("Parsing package %s", packageName)
	r := &gotreload.Rewriter{}
	err = r.Load(packageList...)
	if err != nil {
		log.Fatalf("%v", err)
	}
	err = r.Rewrite(false)
	if err != nil {
		log.Fatalf("%v", err)
	}

	allImports := map[*packages.Package]int{}
	for _, pkg := range r.Pkgs {
		// Is this even possible?
		if len(pkg.Syntax) == 0 {
			continue
		}

		var newDir string
		for _, file := range pkg.Syntax {
			// log.Printf("Writing filtered version of %s", targetFileName)
			outputFileName := strings.TrimPrefix(pkg.Fset.Position(file.Pos()).Filename, pwd+"/")
			newDir = filepath.Join(outputDir, filepath.Dir(outputFileName))
			_, err := os.Stat(newDir)
			if err != nil {
				err := os.MkdirAll(newDir, 0755)
				if err != nil {
					log.Fatalf("Error creating %s: %v", newDir, err)
				}
			}
			b := &bytes.Buffer{}
			err = format.Node(b, pkg.Fset, file)
			if err != nil {
				log.Fatalf("Error formatting filtered version of %s: %v", outputFileName, err)
			}
			outputFilePath := filepath.Join(newDir, filepath.Base(outputFileName))
			err = ioutil.WriteFile(outputFilePath, b.Bytes(), 0644)
			if err != nil {
				log.Fatalf("Error writing filtered version of %s to %s: %v", outputFileName, outputFilePath, err)
			}
			log.Printf("Wrote %s", outputFilePath)
		}

		outputFilePath := filepath.Join(newDir, "grl_register.go")
		err := ioutil.WriteFile(outputFilePath, r.Info[pkg].Registrations, 0644)
		if err != nil {
			log.Fatalf("Error writing %s: %v", outputFilePath, err)
		}
		log.Printf("Wrote %s", outputFilePath)

		allImportedPackages(allImports, pkg)
	}

	pkg0 := r.Pkgs[0]
	path := filepath.Join(outputDir,
		filepath.Dir(
			strings.TrimPrefix(
				pkg0.Fset.Position(pkg0.Syntax[0].Pos()).Filename,
				pwd+"/")))
	for pkg, state := range allImports {
		if state != 2 {
			continue
		}

		registrationSource, err := extract.GenContent(pkg0.Name, pkg.PkgPath, true, pkg.Types, nil, nil)
		if err != nil {
			log.Fatalf("Failed generating symbol registration for %s: %v", pkg.PkgPath, err)
		}
		fname := filepath.Join(path, "grl_"+strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(pkg.PkgPath)+".go")
		// log.Printf("Registrations for %s / %s -> %s", pkg.Name, pkg.PkgPath, fname)
		// log.Println(string(registrationSource))
		ioutil.WriteFile(fname, registrationSource, 0644)
	}
}

func allImportedPackages(m map[*packages.Package]int, pkg *packages.Package) {
	if m[pkg] > 0 {
		return
	}
	// log.Printf("Getting imported packages for %s / %s", pkg.Name, pkg.PkgPath)
	for _, iPkg := range pkg.Imports {
		if internalPkg(iPkg.PkgPath) || probablyStdLib(iPkg.PkgPath) {
			if m[iPkg] == 0 {
				// log.Printf("Skipping %s", iPkg.PkgPath)
				m[iPkg] = 1
			}
		} else {
			allImportedPackages(m, iPkg)
			m[iPkg] = 2
		}
	}
}

func internalPkg(dir string) bool {
	for _, p := range strings.Split(dir, string(filepath.Separator)) {
		if p == "internal" {
			return true
		}
	}
	return false
}

func probablyStdLib(dir string) bool {
	baseDir := strings.Split(dir, string(filepath.Separator))[0]
	if strings.ContainsRune(baseDir, '.') {
		return false
	}
	return true
}
