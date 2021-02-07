/*
Package reloader is designed to be embedded into a hot reloadable executable.

This package watches a set of source code files and attempts to hot-reload
changes to those files.
*/
package reloader

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

const PackageListEnv = "GOT_RELOAD_PKGS"

var (
	// WatchedPkgs is the list of packages being watched for live reloading.
	// It is populated at init time from the process environment.
	WatchedPkgs = watchPackages()
	// PkgsToDirs and DirsToPkgs provide convenient lookups between local
	// disk directories and go package names.
	PkgsToDirs, DirsToPkgs = watchDirs()
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

func StartWatching() <-chan string {
	log.Println(WatchedPkgs, PkgsToDirs, DirsToPkgs)
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
				out <- abs
			}
		}
	}()
	return out
}

func init() {
	changes := StartWatching()
	go func() {
		for change := range changes {
			log.Println(change)
		}
	}()
}
