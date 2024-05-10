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
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/got-reload/got-reload/pkg/gotreload"
	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
	"github.com/traefik/yaegi/stdlib/unrestricted"
	"github.com/traefik/yaegi/stdlib/unsafe"
)

const (
	PackageListEnv   = "GOT_RELOAD_PKGS"
	StartReloaderEnv = "GOT_RELOAD_START_RELOADER"
	SourceDirEnv     = "GOT_RELOAD_SOURCE_DIR"
)

var (
	// Use our own logger.
	log = lpkg.New(os.Stderr, "", lpkg.Lshortfile|lpkg.Lmicroseconds)

	// WatchedPkgs is the list of packages being watched for live reloading.
	// It is populated at init time from the process environment.
	WatchedPkgs = watchPackages()
	// PkgsToDirs and DirsToPkgs provide convenient lookups between local
	// disk directories and go package names.
	PkgsToDirs, DirsToPkgs = watchDirs()

	RegisteredSymbols = interp.Exports{}
	mux               sync.Mutex
	registerRead      bool

	registerAllWG sync.WaitGroup
)

// This returns a value so that it can be used in a blank var expression, e.g.
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

var rMux sync.Mutex

// StartWatching returns a channel and a new gotreload.Rewriter. The channel
// emits a series of filenames (absolute paths) that've changed.
func StartWatching(list []string) (<-chan string, chan struct{}, *gotreload.Rewriter, error) {
	r := gotreload.NewRewriter()
	r.Config.Dir = os.Getenv(SourceDirEnv)
	err := r.Load(list...)
	if err != nil {
		log.Fatalf("Error parsing packages: %v", err)
	}
	err = r.Rewrite(gotreload.ModeRewrite)
	if err != nil {
		log.Fatalf("Error rewriting packages: %s: %v", list, err)
	}
	err = r.Rewrite(gotreload.ModeReload)
	if err != nil {
		log.Fatalf("Error reloading packages: %s: %v", list, err)
	}

	log.Printf("WatchedPkgs: %v, PkgsToDirs: %v, DirsToPkgs: %v", WatchedPkgs, PkgsToDirs, DirsToPkgs)
	changesCh := make(chan string, 1)
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
		defer close(changesCh)
		defer watcher.Close()
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Write) > 0 {
					abs, _ := filepath.Abs(event.Name)
					rMux.Lock()
					if _, _, err := r.LookupFile(abs); err == nil {
						changesCh <- abs
					} else {
						// log.Printf("An unknown file changed: %s", abs)
					}
					rMux.Unlock()
				} else {
					// log.Printf("Unknown event: %v", event)
				}
			case <-exitCh:
				return
			}
		}
	}()
	return changesCh, exitCh, r, nil
}

func Start() {
	log.Println("Starting reloader")
	changesCh, exitCh, r, err := StartWatching(WatchedPkgs)
	if err != nil {
		log.Printf("Error starting watcher; reloading will not work: %v", err)
		return
	}

	const dur = 100 * time.Millisecond
	go func() {
		// Give the rest of the initialization proceses a moment to roll along
		// and, one hopes, do so Adds on registerAllWG.
		time.Sleep(time.Second)

		// signal the watcher to shutdown if we exit
		defer close(exitCh)

		// Wait until all RegisterAlls have finished to continue
		registerAllWG.Wait()
		mux.Lock()
		registerRead = true
		mux.Unlock()
		log.Println("Reloader continuing")

		// log.Printf("Registered symbols: %v", RegisteredSymbols)

		var timer *time.Timer
		changes := map[string]bool{}

		i, err := getInterp()

		if err != nil {
			log.Printf("Error creating an interpreter, reloading will not work: %v", err)
			return
		}

		for change := range changesCh {
			mux.Lock()
			if !changes[change] {
				log.Printf("Changed: %s", change)
				changes[change] = true
			}

			// The AfterFunc is sort of a poor-man's "debounce" function. We
			// accumulate changed files into "changes" and call the "afterfunc"
			// after "dur" time has passed with no new changes.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(dur, func() {
				mux.Lock()
				timer = nil
				defer mux.Unlock()

				for updated := range changes {
					log.Printf("Reparsing package containing %s", updated)
					newR := gotreload.NewRewriter()
					newR.Config.Dir = os.Getenv(SourceDirEnv)

					// Load with file=<foo> loads the package that contains the
					// given file.
					err := newR.Load("file=" + updated)
					if err != nil {
						log.Fatalf("Error parsing package containing file %s: %v", change, err)
					}

					// Rewrite the package in "reload" mode
					err = newR.Rewrite(gotreload.ModeRewrite)
					if err != nil {
						log.Fatalf("Error rewriting package for %s: %v", change, err)
					}
					// FIXME: Have Rewrite w/ModeReload call ModeRewrite itself, or
					// merge them in some better way.
					err = newR.Rewrite(gotreload.ModeReload)
					if err != nil {
						log.Fatalf("Error reloading package for %s: %v", change, err)
					}

					// Ignore any other files from the same package that also
					// changed.
					newPkg := newR.Pkgs[0]
					for _, fileNode := range newPkg.Syntax {
						name := newPkg.Fset.Position(fileNode.Pos()).Filename
						if name == updated {
							continue
						}
						// log.Printf("Ignoring %s since it's in the same package", name)
						delete(changes, name)
					}

					rMux.Lock()

					// log.Printf("Looking for pkg %s", newPkg.PkgPath)
					pkgPath := newR.Pkgs[0].PkgPath
					stubVars := newR.NewFunc[newR.Pkgs[0].PkgPath]

					log.Printf("Looking for updated functions in %s", pkgPath)
					// TODO: This works by formatting every function in the
					// package. That's really slow. It'd be nice to at least only
					// look at the functions in the files that changed. It'd also
					// be nice to compare the data structures directly, instead of
					// formatting it and then comparing the strings.
					for stubVar := range stubVars {
						// log.Printf("Looking at %s: %s", pkgPath, stubVar)

						// Get a string version of the old function definition
						origDefStr, err := r.FuncDef(pkgPath, stubVar)
						if err != nil {
							log.Printf("Error getting function definition of %s:%s: %v",
								pkgPath, stubVar, err)
							continue
						}
						// Get a string version of the new function definition
						newDefStr, err := newR.FuncDef(pkgPath, stubVar)
						if err != nil {
							log.Printf("Error getting new function definition of %s:%s: %v",
								pkgPath, stubVar, err)
							continue
						}

						if origDefStr == newDefStr {
							continue
						}

						log.Printf("%s is new (%s)", stubVar, strings.Split(newDefStr, "\n")[0])
						// log.Printf("Call %s: %s", stubVar, newDef)

						// newDefStr, _ := newR.FuncDef(pkgPath, stubVar)
						setStub := fmt.Sprintf(`%s = %s`, stubVar, newDefStr)

						mainStub := fmt.Sprintf(`package main
import . %q
func main() {
	%s
}`, newR.Pkgs[0].PkgPath, setStub)

						panicked := false
						// Catch Yaegi panics
						func() {
							// defer func() {
							// 	if r := recover(); r != nil {
							// 		err = fmt.Errorf("Eval panicked: %v", r)
							// 		panicked = true
							// 	}
							// }()
							_, err = i.Eval(mainStub)
						}()

						if panicked {
							log.Printf("ERROR: Interpreter panicked: %v", err)
							i, err = getInterp()
							if err != nil {
								log.Printf("ERROR: Can't create a new interpreter, reloading will not work any more: %v", err)
								rMux.Unlock()
								return
							}
							continue
						}

						if err == nil {
							log.Printf("Ran %s", stubVar)
						} else {
							errStr := err.Error()
							log.Printf("Eval error: %s", errStr)
							var line int
							_, err := fmt.Sscanf(errStr, "%d:", &line)
							if err == nil && line-1 >= 0 {
								log.Printf("%d: %s", line, strings.Split(mainStub, "\n")[line-1])
							} else {
								// log.Printf("Cannot parse line number")
								for i, line := range strings.Split(mainStub, "\n") {
									// i+1 because any error (of course) takes into
									// account the "package" (etc) lines.
									log.Printf("%d: %s", i+1, line)
								}
								// log.Printf("Eval error (again): %v", err)
							}
						}
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

					rMux.Unlock()

					delete(changes, updated)
				}
			})
			mux.Unlock()
		}
	}()
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
