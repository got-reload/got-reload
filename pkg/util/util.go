package util

import (
	"path/filepath"
	"strings"
)

func InternalPkg(dir string) bool {
	for _, p := range strings.Split(dir, string(filepath.Separator)) {
		if p == "internal" {
			return true
		}
	}
	return false
}

func ProbablyStdLib(dir string) bool {
	baseDir := strings.Split(dir, string(filepath.Separator))[0]
	if strings.ContainsRune(baseDir, '.') {
		return false
	}
	return true
}
