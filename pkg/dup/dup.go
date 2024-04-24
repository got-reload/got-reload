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
func Copy(dest string, src fs.FS) error {
	walkFunc := func(path string, d fs.DirEntry, inErr error) (retErr error) {
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
