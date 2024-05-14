// Package something_elsesomething_else  has a different name than its import path implies.

//go:build !yaegi_test

// The above tag is set in pkg/gotreload/gotreload_test.go to try to make sure
// that Yaegi uses the compiled version of this package and doesn't just
// interpret/compile it itself.

package something_else

type T struct {
	F int
}
