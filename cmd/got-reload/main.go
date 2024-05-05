package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
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
	var packages string
	var verbose, keep bool
	set := flag.NewFlagSet(selfName, flag.ExitOnError)
	set.StringVar(&packages, "pkgs", "", "The comma-delimited list of packages to enable for hot reload")
	set.StringVar(&packages, "p", "", "Short form of \"-pkgs\"")
	set.BoolVar(&verbose, "v", false, "Pass -v to \"go run\" command") // currently ignored
	set.BoolVar(&keep, "keep", false, "Keep all the filtered code")    // currently ignored
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

	// workDir, err := os.MkdirTemp("", "gotreload-*")
	// if err != nil {
	// 	log.Fatalf("Unable to create work directory: %v", err)
	// }

	workDir := "/tmp/got-reload"

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

	// rewriting can change the set of directly-imported symbols within the
	// packages, so we need to update go.mod so that things still compile.
	if err := runWithIOIn(workDir, "go", "get", "./..."); err != nil {
		log.Fatalf("Failed running go get ./...: %v", err)
	}
	// if err := runWithIOIn(workDir, "go", "mod", "tidy"); err != nil {
	// 	log.Fatalf("Failed running go mod tidy: %v", err)
	// }
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

// Not currently used
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

func writeTempFile(source []byte, targetFileName string) (string, error) {
	outputFile, err := os.CreateTemp("", "gotreloadable-*-"+filepath.Base(targetFileName))
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

	err = os.WriteFile(outputFileName, source, 0600)
	if err != nil {
		return "", err
	}
	return outputFileName, nil
}

// Write the grl_dependencies file for each pkg.
//
// Not used.
func addDependencies(packageList []string) ([]string, error) {
	r := gotreload.NewRewriter()
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
		err := os.WriteFile(fullpath,
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
	watchedPackages := map[string]bool{}
	for _, pkg := range packageList {
		watchedPackages[pkg] = true
	}

	log.Printf("Parsing package %v", packageList)
	r := gotreload.NewRewriter()
	err = r.Load(packageList...)
	if err != nil {
		log.Fatalf("%v", err)
	}
	err = r.Rewrite(gotreload.ModeRewrite)
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
		// syntax tree generated by GenContent (called indirectly via r.Rewrite,
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
			b := &bytes.Buffer{}
			err = format.Node(b, pkg.Fset, file)
			if err != nil {
				log.Fatalf("Error formatting filtered version of %s: %v", outputFileName, err)
			}
			outputFilePath := filepath.Join(newDir, filepath.Base(outputFileName))
			err = os.WriteFile(outputFilePath, b.Bytes(), 0644)
			if err != nil {
				log.Fatalf("Error writing filtered version of %s to %s: %v", outputFileName, outputFilePath, err)
			}
			log.Printf("Wrote %s", outputFilePath)
		}

		outputFilePath := filepath.Join(newDir, "grl_register.go")
		err := os.WriteFile(outputFilePath, r.Info[pkg].Registrations, 0644)
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
		// Skip packages we're watching, since we generate grl_register for
		// them.
		if watchedPackages[pkg.PkgPath] {
			continue
		}

		registrationSource, err := extract.GenContent(
			pkg0.Name, pkg.PkgPath, pkg.Types,
			nil,
			nil, nil, nil,
			extract.NewImportTracker("", ""))
		if err != nil {
			log.Fatalf("Failed generating symbol registration for %q: %v", pkg.PkgPath, err)
		}
		fname := filepath.Join(path, "grl_"+strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(pkg.PkgPath)+".go")
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
