# got reload?

Function/method-level stateful hot reloading for Go!

## Status

Very much work in progress. The usage of this tool changes pretty much daily
as we iterate on it. That being said, it is kind-of usable for some definition
of "usable."

## Do you have a demo?

Yes!

The first is strictly terminal-based, the second is a small Giu UI GUI
program.

### Terminal-based demo

First: Install got-reload:

    # terminal-based demo
    go install github.com/got-reload/got-reload@latest

Then: Clone github.com/got-reload/demo locally and do the following within
that repo's root directory:

```sh
cd /path/to/demo
got-reload run \
    -p github.com/got-reload/demo/example,github.com/got-reload/demo/example2 \
    github.com/got-reload/demo
```

Press enter a few times to see the method get invoked and to watch the
package-level variable get incremented.

In a different window, return to the *original* cloned repo and edit one of
the function definitions in `demo/example` or `demo/example2`. For starters,
just make it return a different constant. (And then save the file.)

You should see the running program discover the changes and reload the definition
of the function. Press enter a few more times to watch the return value change.

Note how the package-level variable's state was not reset by the reload.

### GUI-based demo

Clone github.com/git-reload/giodemo

```sh
cd /path/to/giodemo
got-reload run -p github.com/got-reload/giodemo/reloadable github.com/got-reload/giodemo
```

Try altering the layout function defined in `giodemo/reloadable/reloadable.go`.
See the comments in the file for ideas. Some familiarity with Gio is useful
here.

## Inspiration

See this video that Chris did for something similar:

https://user-images.githubusercontent.com/2324697/106301108-4e2fce80-6225-11eb-8038-1d726b3eb269.mp4

## How it works

### Rewrite each function/method

We alter each function and method declaration in your code so that it invokes
a package-level function variable. This allows us to redefine the implementation
of your functions/methods at runtime.

The filter will transparently change functions from this

```go
func Foo(... args ...) (...return values...) {
  // body
}
```

into this

```go
func Foo(... args ...) (...return values...) {
  return GRLfvar_Foo(...args...)
}

var GRLfvar_Foo func(...args...) (...return values...)

func init() {
  GRLfvar_Foo func(...args...) (...return values...) {
    // body
  }
}
```

and similarly for methods.

We also export all private package-level variables, types, interfaces, and
struct field names, by adding "GRLx_" to the front.

(None of this is done in-place, it's all performed on a temporary copy of the
packages being filtered. No original source code is changed.)

### We watch your source for changes at runtime

When a filtered source file changes, it will be read, parsed, and changed
functions will be installed with new versions of themselves via the generated
`GRLfvar_*` variables, via [Yaegi](https://github.com/traefik/yaegi), a Go
interpreter.

## Limitations

- Fundamental limitations
 
    - Does not support reloading packages that directly reference CGO symbols. You can still depend on packages that use CGO, just don't use any `C.foo` symbols in your reloadable code.
    - Cannot redefine functions that never return. If your whole program runs an event loop that iterates indefinitely over some channels, the new definition of that event loop function will never be invoked because the old one never returned.
    - Cannot redefine `main` or `init` functions (even if you could, it would have no effect. Your program has already started, so these functions have already executed.)

- Current practical limitations (things we be able to eventually work around)

    - You cannot change function signatures.
    - You cannot redefine types (add/remove/change fields) or add new types.
    - You cannot add new package-scope variables or constants during a reload (this should be easy to fix, just haven't gotten to it).
    - You cannot gain new module dependencies during a reload. That said, you _can_ import any package that your module _already_ imports transitively. So if X imports Y and you only import X, then you can later import Y without issue. You can also import any package in the standard library, which is already built-in to Yaegi.
    - You cannot reload any symbols in the `main` package. You can work around this by just copying your current `main` code to (for example) grl_main, exporting `main` as `Main`, and rewriting your real `main` to just call `grl_main.Main()`. Eventually we'll teach the filter how to do this for you. ([Issue 5](https://github.com/got-reload/got-reload/issues/5))

- Known bugs

    - Updating variables in reloaded code directly has some bugs. You can't always just use `foo`, sometimes you have use `*&foo`.

      got-reload could rewrite every mention of the former into the latter, but I'd rather just define the problem well enough to submit an issue to Yaegi and get them to fix it. So far that has been kind of elusive.
    - got-reload/Yaegi have problems with packages referred to by aliases (`import foo "github.com/path/to/bar"`).
    - Sufficiently large Gio programs have problems with some packages, like gioui.org/widget/material. Not sure what's going on there yet; it works fine in the giodemo repository.

## Who came up with this harebrained idea?

- Larry Clapp [@theclapp](https://github.com/theclapp) 
- Chris Waldon [@whereswaldon](https://github.com/whereswaldon)

Given that Yaegi's been out for a while, and Go's parsing tools have been out
since the beginning (well, a lot of them, anyway), we both wonder why nobody
has done this yet, to be honest.

## Can I use it now?

Maybe. It works in the demos, and on some functions in a larger app, but fails
in many cases. (See above under "known bugs".)

## Can I support the development of this tool?

Yes! We appreciate stars, watchers, feedback, and, of course, pull requests! A PR need not necessarily be code, of course; it could be documentation, or something else. Whatever itch you care to scratch.

You can also sponsor the developers:

- Larry Clapp [@theclapp](https://github.com/sponsors/theclapp) 
- Chris Waldon [@whereswaldon](https://github.com/sponsors/whereswaldon)
