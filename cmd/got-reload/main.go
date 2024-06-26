package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/got-reload/got-reload/pkg/dup"
	"github.com/got-reload/got-reload/pkg/extract"
	"github.com/got-reload/got-reload/pkg/gotreload"
	"github.com/got-reload/got-reload/pkg/reloader"
	"github.com/got-reload/got-reload/pkg/util"
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
	subcommandRun    = "run"
	subcommandFilter = "filter"
)

var subcommands = map[string]func(selfName string, args []string){
	subcommandRun:    run,
	subcommandFilter: filter,
}

func run(selfName string, args []string) {
	var packagesCSV string
	var verbose bool
	var useDir string
	// var keep bool

	set := flag.NewFlagSet(selfName, flag.ExitOnError)
	// set.BoolVar(&keep, "keep", false, "Keep all the filtered code")    // currently ignored
	set.StringVar(&packagesCSV, "pkgs", "", "The comma-delimited list of packages to enable for hot reload")
	set.StringVar(&packagesCSV, "p", "", "Short form of \"-pkgs\"")
	set.BoolVar(&verbose, "v", false, "Pass -v to \"go run\" command")
	set.StringVar(&useDir, "dir", "", "The directory to use instead of $TMPDIR/gotreload-*")
	set.StringVar(&useDir, "d", "", "Short form of \"-dir\"")
	set.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, `%[1]s

%[1]s [flags] <package> <cmd-args>

Flags:

`, selfName)
		set.PrintDefaults()
		fmt.Fprintf(out, `
-dir/-d - If you specify this option, it's up to you to clean up the directory
			 before each use. We are wary of running the equivalent of "rm -rf
			 $dir/*" ourselves.
`)
	}
	if err := set.Parse(args); err != nil {
		set.Usage()
		os.Exit(1)
	}
	runPackage := set.Arg(0)

	packages := strings.Split(packagesCSV, ",")
	if len(packages) < 1 {
		log.Fatal("No got-reload packages specified")
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

	var workDir string
	if useDir == "" {
		workDir, err = os.MkdirTemp("", "got-reload-*")
		if err != nil {
			log.Fatalf("Unable to create work directory: %v", err)
		}
	} else {
		workDir = useDir
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
	cmdArgs := append([]string{"filter", "-dir", workDir}, packages...)
	if err := runWithIO(absExecutable, cmdArgs...); err != nil {
		log.Fatalf("Failed rewriting code: %v", err)
	}
	// - invoke go run on the filtered codebase
	os.Setenv(reloader.PackageListEnv, packagesCSV)
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

	// rewriting can change the set of directly-imported symbols within the
	// packages, so we need to update go.mod so that things still compile.
	if err := runWithIOIn(workDir, "go", "get", "./..."); err != nil {
		log.Fatalf("Failed running go get ./...: %v", err)
	}
	// The above "go get" seems to take care of this?
	// if err := runWithIOIn(workDir, "go", "mod", "tidy"); err != nil {
	// 	log.Fatalf("Failed running go mod tidy: %v", err)
	// }
	runArgs := []string{"run"}
	if verbose {
		runArgs = append(runArgs, "-v")
	}
	runArgs = append(runArgs, string(mainPath))
	runArgs = append(runArgs, set.Args()[1:]...)
	if err := runWithIOIn(workDir, "go", runArgs...); err != nil {
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
	watchedPackages := map[string]bool{}
	for _, pkg := range packageList {
		watchedPackages[pkg] = true
	}

	log.Printf("Parsing package %v", packageList)
	r := gotreload.NewRewriter()
	r.OutputDir = outputDir
	r.Pwd = pwd
	err = r.RewriteGoMod()
	if err != nil {
		log.Fatalf("%v", err)
	}

	err = r.Load(packageList...)
	if err != nil {
		log.Fatalf("%v", err)
	}
	err = r.Rewrite(gotreload.ModeRewrite, true)
	if err != nil {
		log.Fatalf("%v", err)
	}

	// Write new source files, and a registration file, for each package.
	allImports := map[*packages.Package]int{}
	for _, pkg := range r.Pkgs {
		// Is this even possible?
		if len(pkg.Syntax) == 0 {
			continue
		}

		var newDir string
		// Write new source files for this package, based on the rewritten
		// syntax tree generated by rewritePkg (called indirectly via r.Rewrite,
		// above).
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
			_, b, err := gotreload.FormatNode(pkg.Fset, file)
			if err != nil {
				log.Fatalf("Error formatting filtered version of %s: %v", outputFileName, err)
			}
			outputFilePath := filepath.Join(newDir, filepath.Base(outputFileName))
			err = os.WriteFile(outputFilePath, b, 0644)
			if err != nil {
				log.Fatalf("Error writing filtered version of %s to %s: %v", outputFileName, outputFilePath, err)
			}
			// log.Printf("Wrote %s", outputFilePath)
		}

		outputFilePath := filepath.Join(newDir, "grl_register.go")
		err := os.WriteFile(outputFilePath, r.Info[pkg].Registrations, 0644)
		if err != nil {
			log.Fatalf("Error writing %s: %v", outputFilePath, err)
		}
		// log.Printf("Wrote %s", outputFilePath)

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
		// Skip packages we're watching, since we generate grl_register for
		// them.
		if watchedPackages[pkg.PkgPath] {
			continue
		}

		fname := filepath.Join(path, "grl_"+strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(pkg.PkgPath)+".go")
		registrationSource, err := extract.GenContent(fname,
			pkg0.Name, pkg.PkgPath, pkg.Types,
			nil, nil, extract.NewImportTracker("", ""))
		if err != nil {
			log.Fatalf("Failed generating symbol registration for %q: %v", pkg.PkgPath, err)
		}
		if registrationSource == nil {
			log.Printf("SKIPPING Registrations for %s / %s -> %s", pkg.Name, pkg.PkgPath, fname)
			continue
		}
		// log.Printf("Registrations for %s / %s -> %s", pkg.Name, pkg.PkgPath, fname)
		// log.Println(string(registrationSource))
		os.WriteFile(fname, registrationSource, 0644)
	}
}

// Find all (direct and indirect) packages imported by pkg
func allImportedPackages(m map[*packages.Package]int, pkg *packages.Package) {
	if m[pkg] > 0 {
		return
	}
	// log.Printf("Getting imported packages for %s / %s", pkg.Name, pkg.PkgPath)
	for _, iPkg := range pkg.Imports {
		if util.InternalPkg(iPkg.PkgPath) || util.ProbablyStdLib(iPkg.PkgPath) {
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
