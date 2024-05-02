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
)

var RegisteredSymbols = interp.Exports{}

// Register records the mappings of exported symbol names to their values
// within the compiled executable.
func Register(pkgName, ident string, val reflect.Value) {
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
	// log.Printf("RegisterAll called on %d packages", len(symbols))
	for pkg, pkgSyms := range symbols {
		// log.Printf("RegisterAll: %s", pkg)
		for pkgSym, value := range pkgSyms {
			Register(pkg, pkgSym, value)
		}
	}
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

func StartWatching(list []string) (<-chan string, *gotreload.Rewriter) {
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
	out := make(chan string)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil
	}
	for dir := range DirsToPkgs {
		log.Printf("Watching %s", dir)
		err := watcher.Add(dir)
		if err != nil {
			return nil, nil
		}
	}
	go func() {
		defer close(out)
		for event := range watcher.Events {
			if event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Write) > 0 {
				abs, _ := filepath.Abs(event.Name)
				rMux.Lock()
				if _, _, err := r.LookupFile(abs); err == nil {
					out <- abs
				} else {
					// log.Printf("An unknown file changed: %s", abs)
				}
				rMux.Unlock()
			}
		}
	}()
	return out, r
}

func Start() {
	log.Println("Starting reloader")
	changesCh, r := StartWatching(WatchedPkgs)
	const dur = 100 * time.Millisecond
	go func() {
		// time.Sleep(5 * time.Second) // give the other init() functions time to register all the symbols
		// log.Printf("Registered symbols: %v", RegisteredSymbols)

		var timer *time.Timer
		mux := sync.Mutex{}
		changes := map[string]bool{}

		for change := range changesCh {
			log.Printf("Changed: %s", change)
			mux.Lock()
			changes[change] = true

			// The AfterFunc is sort of a poor-man's "debounce" function. We
			// accumulate changed files into "changes" and call the "afterfunc"
			// after "dur" time has passed with no new changes.
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(dur, func() {
				mux.Lock()
				timer = nil

				for updated := range changes {
					log.Printf("Reparsing due to %s", updated)
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
					pkgSetters := newR.NewFunc[newR.Pkgs[0].PkgPath]

					log.Printf("Looking for setters in %s", pkgPath)
					for setter := range pkgSetters {
						log.Printf("Looking at setter & func %s", setter)

						// Get a string version of the old function definition
						origDef, err := r.FuncDef(pkgPath, setter)
						if err != nil {
							log.Printf("Error getting function definition of %s:%s: %v",
								pkgPath, setter, err)
							continue
						}
						// Get a string version of the new function definition
						newDef, err := newR.FuncDef(pkgPath, setter)
						if err != nil {
							log.Printf("Error getting function definition of %s:%s: %v",
								pkgPath, setter, err)
							continue
						}

						if origDef == newDef {
							// If they're the same, don't do anything
							// log.Printf("Skip %s", setter)
							continue
						}
						// log.Printf("Call %s: %s", setter, newDef)

						// FIXME: use the same interpreter the whole time
						i := interp.New(interp.Options{
							GoPath: os.Getenv("GOPATH"),
						})
						err = i.Use(stdlib.Symbols)
						if err != nil {
							log.Printf("Error Using stdlib.Symbols")
							break
						}

						err = i.Use(unsafe.Symbols)
						if err != nil {
							log.Printf("Error Using unsafe.Symbols")
							break
						}

						err = i.Use(unrestricted.Symbols)
						if err != nil {
							log.Printf("Error Using unrestricted.Symbols")
							break
						}
						// i.Use(interp.Symbols)
						// log.Printf("Registered symbols: %v", RegisteredSymbols)
						err = i.Use(RegisteredSymbols)
						if err != nil {
							log.Printf("Error Using RegisteredSymbols")
							break
						}

						i.ImportUsed()

						// This really should be just a raw assignment (not a
						// function call), but stock Yaegi currently doesn't
						// support that. I've got a bug report in flight.
						eFunc := fmt.Sprintf(`%s.%s(%s)`, newPkg.Name, setter, newDef)

						i.Eval(fmt.Sprintf(`
package main
import . %q
func main() {
	%s
}`, newR.Pkgs[0].PkgPath, eFunc))

						// log.Printf("Eval: %s", eFunc)
						_, err = i.Eval(eFunc)
						if err == nil {
							log.Printf("Ran %s", setter)
						} else {
							log.Printf("Eval: %s", eFunc)
							log.Printf("Eval error: %v", err)
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

				mux.Unlock()
			})
			mux.Unlock()
		}
	}()
}
