/*
Package reloader is designed to be embedded into a hot reloadable executable.

This package watches a set of source code files and attempts to hot-reload
changes to those files.
*/
package reloader

import (
	"context"
	"fmt"
	lpkg "log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/got-reload/got-reload/pkg/gotreload"
	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
	"github.com/traefik/yaegi/stdlib/unrestricted"
	"github.com/traefik/yaegi/stdlib/unsafe"
	goimports "golang.org/x/tools/imports"
)

const (
	PackageListEnv   = "GOT_RELOAD_PKGS"
	StartReloaderEnv = "GOT_RELOAD_START_RELOADER"
	SourceDirEnv     = "GOT_RELOAD_SOURCE_DIR"
)

var (
	// Use our own logger.
	log = lpkg.New(os.Stderr, "GRL: ", lpkg.Lshortfile|lpkg.Lmicroseconds)

	// WatchedPkgs is the list of packages being watched for live reloading.
	// It is populated at init time from the process environment,
	// $GOT_RELOAD_PKGS.
	WatchedPkgs = watchPackages()
	// PkgsToDirs and DirsToPkgs provide convenient lookups between local
	// disk directories and go package names.
	PkgsToDirs, DirsToPkgs = watchDirs()

	RegisteredSymbols = interp.Exports{}
	mux               sync.Mutex
	registerRead      bool

	// A mux for accessing r, the gotreload.Rewriter
	rMux sync.Mutex

	registerAllWG sync.WaitGroup
)

// Add returns a value so that it can be used in a blank var expression, e.g.
//
//	var _ = Add()
//
// This is used to make sure all the RegisterAll functions have run.
func Add() int {
	// log.Printf("reloader.Add()")
	registerAllWG.Add(1)
	return 0
}

// register records the mappings of exported symbol names to their values
// within the compiled executable.
func register(pkgName, ident string, val reflect.Value) {
	// log.Printf("Register %s.%s", pkgName, ident)
	// baseName := path.Base(pkgName)
	// log.Printf("Register %s.%s as package %s", pkgName, ident, baseName)
	// pkgName = baseName
	if RegisteredSymbols[pkgName] == nil {
		RegisteredSymbols[pkgName] = map[string]reflect.Value{}
	}
	RegisteredSymbols[pkgName][ident] = val
}

// RegisterAll invokes Register once for each symbol provided in the symbols
// map.
func RegisterAll(symbols interp.Exports) {
	mux.Lock()
	defer mux.Unlock()

	// log.Printf("RegisterAll called on %d packages", len(symbols))
	for pkg, pkgSyms := range symbols {
		// log.Printf("RegisterAll: %s", pkg)
		for pkgSym, value := range pkgSyms {
			register(pkg, pkgSym, value)
		}
	}

	if registerRead {
		log.Printf("WARNING: RegisterAll called after Yaegi interpreter initialized")
	}

	registerAllWG.Done()
}

func watchPackages() []string {
	list := os.Getenv(PackageListEnv)
	return strings.Split(list, ",")
}

func watchDirs() (pkgToDir map[string]string, dirToPkg map[string]string) {
	pkgToDir, dirToPkg = make(map[string]string), make(map[string]string)
	cmd := exec.CommandContext(context.TODO(), "go", "list", "-f", "{{.ImportPath}} {{.Dir}}", "./...")
	cmd.Dir = os.Getenv(SourceDirEnv)
	if cmd.Dir != "" {
		log.Printf("Running go list from %s", cmd.Dir)
	}
	out, err := cmd.Output()
	if err != nil {
		return
	}
	lines := strings.Split(string(out), "\n")
	for _, pkg := range watchPackages() {
	innerLoop:
		for _, line := range lines {
			if !strings.HasPrefix(line, pkg) {
				continue innerLoop
			}
			dir := strings.Fields(line)[1]
			pkgToDir[pkg] = dir
			dirToPkg[dir] = pkg
			break innerLoop
		}
	}
	return
}

// StartWatching returns a channel and a new gotreload.Rewriter. The channel
// emits a series of filenames (absolute paths) that've changed.
func StartWatching(list []string) (<-chan string, chan struct{}, *gotreload.Rewriter, error) {
	r := gotreload.NewRewriter()
	r.Config.Dir = os.Getenv(SourceDirEnv)
	err := r.Load(list...)
	if err != nil {
		log.Fatalf("Error parsing packages: %v", err)
	}
	err = r.Rewrite(gotreload.ModeRewrite, false)
	if err != nil {
		log.Fatalf("Error rewriting packages: %s: %v", list, err)
	}
	err = r.Rewrite(gotreload.ModeReload, false)
	if err != nil {
		log.Fatalf("Error reloading packages: %s: %v", list, err)
	}

	log.Printf("WatchedPkgs: %v, PkgsToDirs: %v, DirsToPkgs: %v", WatchedPkgs, PkgsToDirs, DirsToPkgs)
	changedCh := make(chan string, 1)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, nil, err
	}
	for dir := range DirsToPkgs {
		log.Printf("Watching %s", dir)
		err := watcher.Add(dir)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	exitCh := make(chan struct{})
	go func() {
		defer close(changedCh)
		defer watcher.Close()
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Write) > 0 {
					abs, _ := filepath.Abs(event.Name)
					rMux.Lock()
					file := r.LookupFile(abs)
					rMux.Unlock()
					if file != nil {
						changedCh <- abs
					} else {
						// log.Printf("An unknown file changed: %s", abs)
					}
				} else {
					// log.Printf("Unknown event: %v", event)
				}
			case <-exitCh:
				return
			}
		}
	}()
	return changedCh, exitCh, r, nil
}

func Start() {
	log.Println("Starting reloader")
	changedCh, exitCh, r, err := StartWatching(WatchedPkgs)
	if err != nil {
		log.Printf("Error starting watcher; reloading will not work: %v", err)
		return
	}

	go rlLoop(r, changedCh, exitCh, 100*time.Millisecond)
}

func rlLoop(r *gotreload.Rewriter, changedCh <-chan string, exitCh chan struct{}, dur time.Duration) {
	// Give the rest of the initialization processes a moment to roll along and,
	// one hopes, do some Adds on registerAllWG.
	time.Sleep(time.Second)

	// signal the watcher to shutdown if we exit
	defer close(exitCh)

	// Wait until all RegisterAlls have finished to continue
	log.Println("Reloader waiting for all RegisterAll calls to finish")
	registerAllWG.Wait()
	mux.Lock()
	registerRead = true
	mux.Unlock()
	log.Println("Reloader continuing")

	// log.Printf("Registered symbols: %v", RegisteredSymbols)

	var timer *time.Timer
	changed := map[string]bool{}

	_, interpErr := getInterp()
	if interpErr != nil {
		log.Printf("Error creating an interpreter, reloading will not work: %v", interpErr)
		return
	}

	for change := range changedCh {
		mux.Lock()
		if interpErr != nil {
			mux.Unlock()
			return
		}

		if !changed[change] {
			log.Printf("Changed: %s", change)
			changed[change] = true
		}

		// The AfterFunc is sort of a poor-man's "debounce" function. We
		// accumulate changed files into "changed" and call the "afterfunc"
		// after "dur" time has passed with no new changes.
		if timer != nil {
			timer.Stop()
		}
		timer = time.AfterFunc(dur, func() {
			mux.Lock()
			defer mux.Unlock()

			timer = nil
			interpErr = processChanges(r, changed)
		})
		mux.Unlock()

	}
}

func processChanges(r *gotreload.Rewriter, changed map[string]bool) error {
	for updated := range changed {
		log.Printf("Reparsing package containing %s", updated)
		newR := gotreload.NewRewriter()
		newR.Config.Dir = os.Getenv(SourceDirEnv)

		// Load with file=<foo> loads the package that contains the
		// given file.
		err := newR.Load("file=" + updated)
		if err != nil {
			log.Fatalf("Error parsing package containing file %s: %v", updated, err)
		}

		log.Printf("Refiltering package containing %s", updated)
		// Rewrite the package in "reload" mode
		err = newR.Rewrite(gotreload.ModeRewrite, false)
		if err != nil {
			log.Fatalf("Error rewriting package for %s: %v", updated, err)
		}
		// FIXME: Have Rewrite w/ModeReload call ModeRewrite itself, or
		// merge them in some better way.
		err = newR.Rewrite(gotreload.ModeReload, false)
		if err != nil {
			log.Fatalf("Error reloading package for %s: %v", updated, err)
		}

		err = processSingleChange(r, newR, changed)
		if err != nil {
			return err
		}
	}
	return nil
}

func processSingleChange(r, newR *gotreload.Rewriter, changed map[string]bool) error {
	rMux.Lock()
	defer rMux.Unlock()

	newPkg := newR.Pkgs[0]
	// log.Printf("Looking for pkg %s", newPkg.PkgPath)
	pkgPath := newR.Pkgs[0].PkgPath

	log.Printf("Looking for updated functions in %s", pkgPath)

	// Look at all stubbed functions, and build a map of only those in files that
	// changed.
	allStubVars := newR.NewFunc[newR.Pkgs[0].PkgPath]
	var possiblyChangedStubVars []string
	seen := map[string]bool{}
	for stubVar, funcLit := range allStubVars {
		name := newPkg.Fset.Position(funcLit.Pos()).Filename
		if !changed[name] {
			continue
		}
		seen[name] = true
		possiblyChangedStubVars = append(possiblyChangedStubVars, stubVar)
		// log.Printf("Might've changed: %s", stubVar)
	}
	// Delete all seen files from the map of changed files.
	for name := range seen {
		delete(changed, name)
	}
	sort.Strings(possiblyChangedStubVars)

	updatedFound := false
	// Look at all the functions that might've changed, because of being in one
	// of the files that changed.
	for _, stubVar := range possiblyChangedStubVars {
		funcLit := allStubVars[stubVar]

		// log.Printf("Looking at %s: %s", pkgPath, stubVar)

		// Get a string version of the new function definition
		newDefStr, _, err := gotreload.FormatNode(newPkg.Fset, funcLit)
		if err != nil {
			log.Printf("Error getting new function definition of %s:%s: %v", pkgPath, stubVar, err)
			continue
		}

		status := "is new"
		if hasPragma(newDefStr, "ForceReload") {
			status = "forced reload"
		} else {
			// Get a string version of the old function definition
			origDefStr, err := r.FuncDef(pkgPath, stubVar)
			if err != nil {
				log.Printf("Error getting function definition of %s:%s: %v", pkgPath, stubVar, err)
				continue
			}

			if origDefStr == newDefStr {
				continue
			}
		}

		updatedFound = true

		log.Printf("%s %s", stubVar, status)

		// Get the named imports (if any) from the changed file
		changedFile := gotreload.FileFromPos(newPkg, funcLit)
		importsList := []string{
			fmt.Sprintf(". %q", newR.Pkgs[0].PkgPath),
		}
		for _, imp := range changedFile.Imports {
			impName := newPkg.TypesInfo.PkgNameOf(imp).Name()

			// Note that imp.Path.Value includes the surrounding
			// double-quotes of the import.
			importsList = append(importsList,
				fmt.Sprintf("%s %s", impName, imp.Path.Value))
		}
		imports := strings.Join(importsList, "\n")
		// log.Printf("Imports:\n%s", imports)

		mainFunc := fmt.Sprintf(`package main
import (
	%s
)
func main() {
	%s = %s
}`, imports, stubVar, newDefStr)

		// Run "goimports" on the generated main() function.
		//
		// TODO: Could probably adapt astutil.UsesImport for this.
		updatedFilename := newPkg.Fset.Position(changedFile.Pos()).Filename
		mfBytes, err := goimports.Process(updatedFilename, []byte(mainFunc), nil)
		if err != nil {
			log.Printf("failed to 'goimports' source for %s: %v", stubVar, err)
			log.Printf("Main func: %s", mainFunc)
			continue
		}
		mainFunc = string(mfBytes)

		// log.Printf("Main:\n%s", mainFunc)

		// Getting a new interp for every function is overkill, but it's the only
		// way I can see, so far, to not get duplicate import errors if you eval
		// the same function twice, or change two functions in the same file at
		// the same time.
		i, err := getInterp()
		if err != nil {
			log.Printf("Error getting a new interp: %v", err)
			return err
		}

		var r any
		panicked := false
		// Catch Yaegi panics, maybe
		func() {
			if !hasPragma(newDefStr, "NoCatchPanic") {
				defer func() {
					if r = recover(); r != nil {
						err = fmt.Errorf("Eval panicked: %v", r)
						panicked = true
					}
				}()
			}

			_, err = i.Eval(mainFunc)
		}()

		if panicked {
			log.Printf("ERROR: Interpreter panicked: %v", err)
			errStr := fmt.Sprintf("%v", r)
			var line int
			_, err := fmt.Sscanf(errStr, "%d:", &line)
			if err == nil && line-1 >= 0 {
				log.Printf("%d: %s", line, strings.Split(mainFunc, "\n")[line-1])
			}
			continue
		}

		if err == nil {
			log.Printf("Ran %s", stubVar)

			if hasPragma(newDefStr, "PrintMe") {
				for i, line := range strings.Split(mainFunc, "\n") {
					log.Printf("%d: %s", i+1, line)
				}
			}
		} else {
			errStr := err.Error()
			log.Printf("Eval error: %s", errStr)
			var line int
			_, err := fmt.Sscanf(errStr, "%d:", &line)
			if err == nil && line-1 >= 0 {
				log.Printf("%d: %s", line, strings.Split(mainFunc, "\n")[line-1])
			} else {
				// log.Printf("Cannot parse line number")
				for i, line := range strings.Split(mainFunc, "\n") {
					// i+1 because any error (of course) takes into
					// account the "package" (etc) lines.
					log.Printf("%d: %s", i+1, line)
				}
				// log.Printf("Eval error (again): %v", err)
			}
			// log.Printf("Main func: %s", mainFunc)
		}
		// log.Printf("Main func: %s", mainFunc)
	}
	if !updatedFound {
		log.Printf("(Didn't find any.)")
	}

	// Update r with data from newR
	for i, pkg := range r.Pkgs {
		if pkg.PkgPath == pkgPath {
			// log.Printf("Replacing %s in r", pkgPath)
			r.Pkgs[i] = newPkg
			break
		}
	}
	r.NewFunc[pkgPath] = newR.NewFunc[pkgPath]

	return nil
}

func hasPragma(s, pragma string) bool {
	return strings.Contains(s, fmt.Sprintf("pragma.%s()\n", pragma))
}

func getInterp() (*interp.Interpreter, error) {
	i := interp.New(interp.Options{
		GoPath: os.Getenv("GOPATH"),
	})
	err := i.Use(stdlib.Symbols)
	if err != nil {
		return nil, fmt.Errorf("Error Using stdlib.Symbols")
	}

	err = i.Use(unsafe.Symbols)
	if err != nil {
		return nil, fmt.Errorf("Error Using unsafe.Symbols")
	}

	err = i.Use(unrestricted.Symbols)
	if err != nil {
		return nil, fmt.Errorf("Error Using unrestricted.Symbols")
	}

	// i.Use(interp.Symbols)
	// log.Printf("Registered symbols: %v", RegisteredSymbols)
	err = i.Use(RegisteredSymbols)
	if err != nil {
		return nil, fmt.Errorf("Error Using RegisteredSymbols")
	}

	i.ImportUsed()

	return i, nil
}
