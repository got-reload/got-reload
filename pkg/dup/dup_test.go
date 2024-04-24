package dup_test

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"testing"

	"github.com/got-reload/got-reload/pkg/dup"
)

//go:embed testdata
var testdata embed.FS

func TestCopy(t *testing.T) {
	dir, err := os.MkdirTemp("", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll(dir)
	})
	dest := os.DirFS(dir)

	// err = dup.Copy(dir, dup.Target{FS: testdata, Root: "."})
	err = dup.Copy(dir, testdata)
	if err != nil {
		t.Fatalf("Did not expect error copying: %v", err)
	}
	paths := make(map[string][]byte)
	err = fs.WalkDir(testdata, ".", func(path string, d fs.DirEntry, _ error) error {
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(testdata, path)
		if err != nil {
			return err
		}
		paths[path] = b
		return nil
	})
	if err != nil {
		t.Fatalf("Failed traversing test data: %v", err)
	}
	err = fs.WalkDir(dest, ".", func(path string, d fs.DirEntry, _ error) error {
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(dest, path)
		if err != nil {
			return err
		}
		if bytes.Compare(paths[path], b) != 0 {
			return fmt.Errorf("Copied contents differ for file %v", path)
		}
		delete(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("Failed traversing copied data: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("Copied data is missing some files/content: %v", paths)
	}
}
