// Package pragma contains dummy functions used as pragmas in code reloaded by
// got-reload.
//
// Add `pragma.Foo()` to reloaded code to trigger the given pragma's behavior.
//
// These functions obviously don't actually do anything. The got-reload reloader
// checks for their textual inclusion in the reloaded source code to trigger
// their behavior.
//
// Example:
//
// func foo() {
//   pragma.PrintMe()
//
//   // other code
// }

package pragma

// PrintMe causes the filtered function to be printed out. This is handy for
// debugging and demos.
func PrintMe() {}

// ForceReload causes the filtered function to always be reloaded, even if not
// changed. This is handy when testing got-reload itself.
func ForceReload() {}

// NoCatchPanic causes got-reload to not catch panics in the Yaegi interp.Eval
// call. This is handy to get a stack trace that shows exactly where the
// interpreter is panicking (if it panics).
func NoCatchPanic() {}

// SkipMe tells got-reload to ignore the function containing this pragma.
func SkipMe() {}
