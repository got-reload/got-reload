/*
Package dup provides tools for recursively copying files.
*/
package dup

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Copy copies the entire filesystem described by src to the local filesystem
// at the path rooted at the string dest. Dest does not need to exist in
// advance. File permissions are not preserved by the copy.
//
// Skips .git and .*.sw? (Vim swap files).
func Copy(dest string, src fs.FS) error {
	walkFunc := func(path string, d fs.DirEntry, _ error) (retErr error) {
		if shouldSkip(path) {
			return nil
		}

		destPath := filepath.Join(dest, path)
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("failed looking up file info for %s: %w", path, err)
			}
			if err := os.MkdirAll(destPath, info.Mode()|0700); err != nil {
				return fmt.Errorf("failed creating directory %s: %w", destPath, err)
			}
			return nil
		}
		newFile, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("failed copying to %s: %w", destPath, err)
		}
		defer func() {
			if e := newFile.Close(); e != nil {
				if retErr == nil {
					retErr = fmt.Errorf("failed finalizing %s: %w", destPath, e)
				}
			}
		}()
		srcFile, err := src.Open(path)
		if err != nil {
			return fmt.Errorf("failed opening %s: %w", path, err)
		}
		defer srcFile.Close()

		if _, err := io.Copy(newFile, srcFile); err != nil {
			return fmt.Errorf("failed copying file %s: %w", path, err)
		}

		return err
	}
	if err := fs.WalkDir(src, ".", walkFunc); err != nil {
		return fmt.Errorf("failed walking source: %w", err)
	}
	return nil
}

// shouldSkip returns true for any path containing .git, or any path ending in
// .sw?.
func shouldSkip(f string) bool {
	if len(f) > 4 && f[len(f)-4:len(f)-1] == ".sw" {
		return true
	}
	var file string
	for {
		f, file = filepath.Split(f)
		if f == "" {
			break
		}
		if file == ".git" || f == ".git/" {
			return true
		}
		if len(f) > 1 {
			f = f[:len(f)-1]
		}
	}
	return false
}
