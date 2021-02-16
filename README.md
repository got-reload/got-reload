# got reload?

Function/method-level stateful hot reloading for Go!

## Status

Very much work in progress. The usage of this tool changes pretty much daily
as we iterate on it. That being said, it is usually usable for some definition
of "usable."

## Do you have a demo?

> Note: We intend to simplify the usage greatly from this form; bear with us!

Clone this repo somewhere and do the following:

```sh
# define a directory for rewritten source code
export GOT_RELOAD_TREE=$(mktemp -d)

# define the packages we want to make reloadable
export GOT_RELOAD_PKGS=$(echo github.com/got-reload/got-reload/demo/{example,example2} | tr ' ' ,)

# define the location of our main package's source code
export GOT_RELOAD_SOURCE_DIR=$(cd demo && pwd)

# copy all of our files to our alternative tree to be rewritten
tar -cf - * | tar -xf - -C "$GOT_RELOAD_TREE"

# rewrite those files to be reloadable
go run ./cmd/got-reload/ filter -dir "$GOT_RELOAD_TREE" $(echo "$GOT_RELOAD_PKGS" | tr , ' ')

# signal the live reloader to activate when its init() function is called
export GOT_RELOAD_START_RELOADER=1

# go to our rewritten main package
cd "$GOT_RELOAD_TREE/demo"

# run our code
go run -v .

# press enter a few times to see the method get invoked and to watch the
# package-level variable get incremented
```

In a different terminal, return to the *original* cloned repo and edit one of
the function definitions in `demo/example` or `demo/example2`. For starters,
just make it return a different constant.

You should see the running program discover the changes and reload the definition
of the function. Press enter a few more times to watch the return value change.
Note how the package-level variable's state was not reset by the reload.

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
  return GRLf_Foo(...args...)
}

var GRLf_Foo = func(...args...) (...return values...) {
   // body
}

func GRLset_Foo(f func(...Foo's signature)...) {
  GRLf_Foo = f
}
```

and similarly for methods.

Export all named private package-level variables, types, interfaces, and
struct field names, by adding "GRL_" to the front.

(None of this is done in-place, it's all performed on a temporary copy of the
packages being filtered.  No original source code is changed.)

### We watch your source for changes at runtime

When a filtered source file changes, it will be read, parsed, and changed
functions will be installed with new versions of themselves via the generated
`GRLset_*` functions, via [Yaegi](https://github.com/traefik/yaegi), a Go
interpreter.

## Limitations

- Fundamental limitations
 
    - Does not support reloading packages that directly reference CGO symbols. You can still depend on packages that use CGO, just don't use any `C.foo` symbols in your reloadable code.
    - Cannot redefine functions that never return. If your whole program runs an event loop that iterates indefinitely over some channels, the new definition of that event loop function will never be invoked because the old one never returned.
    - Cannot redefine `main` or `init` functions (even if you could, it would have no effect. Your program has already started, so these functions have already executed.)

- Current practical limitations (things we hope to eventually work around)

    - You cannot change function signatures.
    - You cannot redefine types (add/remove/change fields).
    - You cannot add new package-scope variables or constants during a reload (this should be easy to fix, just haven't gotten to it).
    - You cannot gain new module dependencies during a reload.  That said, you _can_ import any package that your module _already_ imports transitively.  So if X imports Y and you only import X, then you can later import Y without issue.  You can also import any package in the standard library, which is already built-in to Yaegi.
    - You cannot reload any symbols in the `main` package.  You can work around this by just copying your current `main` code to (for example) grl_main, exporting `main` as `Main`, and rewriting your real `main` to just call `grl_main.Main()`.  Eventually we'll teach the filter how to do this for you.  ([Issue 5](https://github.com/got-reload/got-reload/issues/5))

## Who came up with this hair-brained idea?

- Larry Clapp [@theclapp](https://github.com/theclapp) 
- Chris Waldon [@whereswaldon](https://github.com/whereswaldon)

Given that Yaegi's been out for a while, and Go's parsing tools have been out
since the beginning (well, a lot of them, anyway), we both wonder why nobody
has done this yet, to be honest.

## Can I use it now?

Yes? Kinda depends on your tolerance for jank and breaking changes. If you can
survive the fact that the CLI may change on a daily basis, then sure!

## Can I support the development of this tool?

Yes! You can sponsor one of the two developers:

- Larry Clapp [@theclapp](https://github.com/sponsors/theclapp) 
- Chris Waldon [@whereswaldon](https://github.com/sponsors/whereswaldon)

We also appreciate stars, watchers, and feedback!
