/*
Package reloader is designed to be embedded into a hot reloadable executable.

This package watches a set of source code files and attempts to hot-reload
changes to those files.
*/
package reloader

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/huckridgesw/got-reload/pkg/gotreload"
	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

const PackageListEnv = "GOT_RELOAD_PKGS"

var (
	// WatchedPkgs is the list of packages being watched for live reloading.
	// It is populated at init time from the process environment.
	WatchedPkgs = watchPackages()
	// PkgsToDirs and DirsToPkgs provide convenient lookups between local
	// disk directories and go package names.
	PkgsToDirs, DirsToPkgs = watchDirs()

	r gotreload.Rewriter
)

func watchPackages() []string {
	list := os.Getenv(PackageListEnv)
	return strings.Split(list, ",")
}

func watchDirs() (pkgToDir map[string]string, dirToPkg map[string]string) {
	pkgToDir, dirToPkg = make(map[string]string), make(map[string]string)
	cmd := exec.CommandContext(context.TODO(), "go", "list", "-f", "{{.ImportPath}} {{.Dir}}", "./...")
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

func StartWatching(list string) <-chan string {
	err := r.Load(list)
	if err != nil {
		log.Fatalf("Error parsing packages: %v", err)
	}
	err = r.Rewrite(true)
	if err != nil {
		log.Fatalf("Error rewriting packages: %s: %v", list, err)
	}

	log.Printf("WatchedPkgs: %v, PkgsToDirs: %v, DirsToPkgs: %v", WatchedPkgs, PkgsToDirs, DirsToPkgs)
	out := make(chan string)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	for dir := range DirsToPkgs {
		err := watcher.Add(dir)
		if err != nil {
			return nil
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
				}
				rMux.Unlock()
			}
		}
	}()
	return out
}

func Start() {
	log.Printf("Reloader Starting: 11:00")
	list := os.Getenv(PackageListEnv)
	changesCh := StartWatching(list)
	const dur = 2 * time.Second
	go func() {
		var timer *time.Timer
		mux := sync.Mutex{}
		changes := map[string]bool{}

		for change := range changesCh {
			log.Printf("Changed: %s", change)
			mux.Lock()
			changes[change] = true
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(dur, func() {
				mux.Lock()
				timer = nil

				for updated := range changes {
					log.Printf("Reparsing due to %s", updated)
					newR := &gotreload.Rewriter{}
					err := newR.Load("file=" + updated)
					if err != nil {
						log.Fatalf("Error parsing package containing file %s: %v", change, err)
					}
					err = newR.Rewrite(true)
					if err != nil {
						log.Fatalf("Error rewriting package for %s: %v", change, err)
					}

					newPkg := newR.Pkgs[0]
					for _, fileNode := range newPkg.Syntax {
						name := newPkg.Fset.Position(fileNode.Pos()).Filename
						if name == updated {
							continue
						}
						log.Printf("Ignoring %s since it's in the same package", name)
						delete(changes, name)
					}

					rMux.Lock()

					log.Printf("Looking for pkg %s", newPkg.PkgPath)
					for pkgPath, pkgSetters := range newR.NewFunc {
						if pkgPath != newPkg.PkgPath {
							log.Printf("Skipping %s", pkgPath)
							continue
						}

						log.Printf("Looking for setters in %s", newPkg.PkgPath)
						for setter := range pkgSetters {
							log.Printf("Looking at setter & func %s", setter)

							origDef, err := r.FuncDef(newPkg.PkgPath, setter)
							if err != nil {
								log.Printf("Error getting function definition of %s:%s: %v",
									newPkg.PkgPath, setter, err)
								continue
							}
							newDef, err := newR.FuncDef(newPkg.PkgPath, setter)
							if err != nil {
								log.Printf("Error getting function definition of %s:%s: %v",
									newPkg.PkgPath, setter, err)
								continue
							}
							if origDef == newDef {
								log.Printf("Skip %s", setter)
							} else {
								log.Printf("Call %s: %s", setter, newDef)

								i := interp.New(interp.Options{})
								i.Use(stdlib.Symbols)
								// i.Use(interp.Symbols)
								// yaegi.Use(giopkgs.Symbols)
								i.Use(gotreload.RegisteredSymbols)
								imports := []string{}
								for importPath := range gotreload.RegisteredSymbols {
									imports = append(imports, importPath)
								}
								func() {
									eFunc := fmt.Sprintf(`
package main
import (
	%q
)
func main() {
	%s.%s(%s)
}
`, newPkg.PkgPath, newPkg.Name, setter, newDef)

									log.Printf("Eval: %s", eFunc)
									_, err := i.Eval(eFunc)
									if err != nil {
										log.Printf("Eval error: %v", err)
										return
									}
									// YY_invalidate(hub, "yaegi")
									log.Println("yaegi successful")
								}()
							}
						}
					}

					for i, pkg := range r.Pkgs {
						if pkg.PkgPath == newPkg.PkgPath {
							log.Printf("Replacing %s in r", newPkg.PkgPath)
							r.Pkgs[i] = newPkg
							break
						}
					}
					r.NewFunc = newR.NewFunc

					rMux.Unlock()

					delete(changes, updated)
				}

				mux.Unlock()
			})
			mux.Unlock()
		}
	}()
}
