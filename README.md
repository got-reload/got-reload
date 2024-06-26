# got reload?

Function-level stateful hot reloading for Go!

# Status

Beta. Might work in your project, might not. The Yaegi bugs listed below
definitely make things awkward. Doesn't work with generic functions/methods. Not
recommended for production use.

All that said, it's pretty neat when it does work, especially on GUI code where
you can see the results immediately.

# How it works

## Rewrite each function/method

Got-reload copies everything in your project tree (the current directory when
you run got-reload) to a temporary directory (something under `$TMPDIR`, or
specified as an argument to `got-reload run`), and along the way alters each
function and method body in your code so that it indirects via a variable. This
allows it to redefine the implementation of functions/methods at runtime, by
assigning a new closure to that variable, via
[Yaegi](https://github.com/traefik/yaegi), a Go interpreter.

The filter will transparently change functions and variables from this

```go
var (
	f = new(float64)
)

func F1() int {
	*f += 0.1
	fmt.Printf("f: %0.3f, sin %0.3f\n", *f, example2.Sin(*f))
	return 1
}
```

into this

```go
var (
	GRLx_f = new(float64)
)

func F1() int { return GRLfvar_F1() }

var GRLfvar_F1 func() int

func init() {
	GRLfvar_F1 = func() int {
		*GRLx_f += 0.1
		fmt.Printf("f: %0.3f, sin %0.3f\n", *GRLx_f, example2.Sin(*GRLx_f))
		return 1
	}
}
```

and similarly for methods.

## Export everything that wasn't already

As seen in the example code above, got-reload exports all unexported
package-level identifiers (variables, types, field names of package-level
structs) by adding "GRLx_" to the front of the identifier names. (This is
because all interpreted Yaegi code runs from package `main`, and so can only
access exported identifiers).

(As mentioned above, none of this is done in-place, it's all performed on a
temporary copy of the packages being filtered. No original source code is
changed.)

## Watch your source for changes at runtime

When a source file in a watched package changes, it is reread, and changed
functions will be installed with new versions of themselves via the generated
`GRLfvar_*` variables, via Yaegi.

That replacement looks something like this:

```go
GRLfvar_F1 = func() int {
	*GRLx_f += 0.2 // note change here
	fmt.Printf("f: %0.3f, sin %0.3f\n", *GRLx_f, example2.Sin(*GRLx_f))
	return 1
}
```

# Can I see the rewritten code?

Yes, with some limitations. For the initial filtered code, check the first line
of output from `got-reload run` to get the temporary directory used:

```console
main.go:172: copying [...]/github.com/got-reload/demo to /var/folders/9d/vt3kqx293xx8w3tn8m1jy_wc0000gn/T/gotreload-3376983803
```

Check the path mentioned after "to". (It's a subdir of `$TMPDIR`.)

You can also give `got-reload run` a directory via the `-d` argument. (If you
use this more than once, it's up to you to clean up the directory between runs.)
I frequently run like this:

```sh
rm -rf /tmp/got-reload/* /tmp/got-reload/.* && 
got-reload run -d /tmp/got-reload -v -p <paths> <package-path>
```

Note that the given directory is written once, used as a target for `go run` to
run the filtered code, and not updated thereafter. In other words, if you change
your source and save, the filtered code is not updated on-disk.

There are also "pragmas" you can add to a function to alter the behavior of
got-reload, including printing filtered code; see below.

# Altering the behavior of got-reload at run-time via pragmas

You can add calls to functions in
[pkg/pragma](https://github.com/got-reload/got-reload/tree/main/pkg/pragma) to
your code to alter the behavior of got-reload. These functions don't actually do
anything, they're just stubs; got-reload checks for the existence of the
function-call in your source code via a simple call to `strings.Contains` and
alters its behavior accordingly.

As of this writing, there are four pragmas: PrintMe, SkipMe, ForceReload, and
NoCatchPanic.

The PrintMe pragma will make got-reload print the filtered function whenever it
changes. Check the godoc in the `pragma` package for the other pragmas.

# Do you have a demo?

Yes.

The first is strictly terminal-based, the second is a small Gio-UI GUI program.

## Terminal-based demo

See the demo repo readme for code samples (input, filtered output, replacement
code, logs from a sample run): https://github.com/got-reload/demo.

- Install got-reload:

```sh
go install github.com/got-reload/got-reload/cmd/got-reload@latest
```

- Clone `github.com/got-reload/demo` locally (update the path in the first line
  as appropriate for your environment)

```sh
cd $GOPATH/src/github.com # or some appropriate path
mkdir got-reload && cd got-reload
git clone git@github.com:got-reload/demo.git
cd demo
```

- Do the following within that repo's root directory:

```sh
# assumes you're still in "demo" from above
got-reload run \
    -p github.com/got-reload/demo/example,github.com/got-reload/demo/example2 \
    github.com/got-reload/demo
```

You should see messages similar to this:

```
main.go:172: copying [...]/github.com/got-reload/demo to /var/folders/9d/vt3kqx293xx8w3tn8m1jy_wc0000gn/T/gotreload-3376983803
main.go:302: Parsing package [github.com/got-reload/demo/example github.com/got-reload/demo/example2]
main.go:194: GOT_RELOAD_PKGS=github.com/got-reload/demo/example,github.com/got-reload/demo/example2
main.go:194: GOT_RELOAD_START_RELOADER=1
main.go:194: GOT_RELOAD_SOURCE_DIR=[...]/github.com/got-reload/demo
[...]
GRL: 16:19:27.424980 reloader.go:114: Running go list from [...]/github.com/got-reload/demo
GRL: 16:19:27.479108 reloader.go:196: Starting reloader
GRL: 16:19:27.920789 reloader.go:154: WatchedPkgs: [github.com/got-reload/demo/example github.com/got-reload/demo/example2], PkgsToDirs: map[github.com/got-reload/demo/example:[...]/github.com/got-reload/demo/example github.com/got-reload/demo/example2:[...]/github.com/got-reload/demo/example2], DirsToPkgs: map[[...]/github.com/got-reload/demo/example:github.com/got-reload/demo/example [...]/github.com/got-reload/demo/example2:github.com/got-reload/demo/example2]
GRL: 16:19:27.920847 reloader.go:161: Watching [...]/github.com/got-reload/demo/example
GRL: 16:19:27.921066 reloader.go:161: Watching [...]/github.com/got-reload/demo/example2
Press enter to call example.F1 and example2.F2 repeatedly
Enter s to stop
example2.Sin: f: 0.100
f: 0.100, sin 0.100
example.F1: 1
GRL: 16:19:28.921532 reloader.go:215: Reloader waiting for all RegisterAll calls to finish
GRL: 16:19:28.921569 reloader.go:220: Reloader continuing
```

If you don't see the bits about `Running go list` and `Starting reloader` and so
on then something's gone wrong. To be super candid: That's happened to me and
I'm not sure why. And then I abort, run the command again, and then it works. I
dunno. Sorry for the confusion here.

Anyway, press enter a few times to see the method get invoked and to watch the
package-level variable get incremented.

In a different window, return to the cloned demo repo and edit one of the
function definitions in `demo/example` or `demo/example2`. For starters, just
make it return a different constant. And then save the file.

You should see the running program discover the changes and reload the
definition of the function. Press enter a few more times to watch the return
value change.

Note how the package-level variable's state was not reset by the reload.

Enter "s" to stop.

## GUI-based demo

Clone github.com/git-reload/giodemo and run `got-reload run` (this assumes
you've installed got-reload already, as outlined in the above steps):

```sh
cd $GOPATH/src/github.com/got-reload # or some appropriate path
git clone git@github.com:got-reload/giodemo.git
cd giodemo
got-reload run -p github.com/got-reload/giodemo/reloadable github.com/got-reload/giodemo
```

Try altering the layout function defined in `giodemo/reloadable/reloadable.go`.
See the comments in the file for ideas. Some familiarity with Gio is useful
here.

# Inspiration

See this video that Chris did for something similar:

https://user-images.githubusercontent.com/2324697/106301108-4e2fce80-6225-11eb-8038-1d726b3eb269.mp4

# Limitations

## Fundamental limitations
 
- Does not support reloading packages that directly reference CGO symbols. You
  can still depend on packages that use CGO, just don't use any `C.foo` symbols
  in your reloadable code.
- Cannot redefine functions that never return. If your program has an event loop
  that runs forever, the new definition of that event loop function will never
  be invoked because the old one never returns.
- Cannot redefine `main` or `init` functions (even if you could, it would have
  no effect. Your program has already started, so these functions have already
  executed.)

## Current practical limitations (things we might to be able to eventually work around)

- You cannot change function signatures.
- You cannot redefine types (add/remove/change fields) or add new types.
- You cannot add new package-scope variables or constants during a reload.
- You cannot gain new module dependencies during a reload.

  That said, you *can* import any package that your module *already* imports
  transitively. So if X imports Y and you only directly import X, then you can
  later directly import Y without issue. You can also import any package in the
  standard library, which is already built-in to Yaegi.
- You cannot reload any symbols in the `main` package. You can work around this
  by just copying your current `main` code to a different package, e.g.
  `grl_main`, exporting `main` as `Main`, and rewriting your real `main` to just
  call `grl_main.Main()`. Eventually we may teach the filter how to do this for
  you. ([Issue 5](https://github.com/got-reload/got-reload/issues/5))

## Known bugs

See Yaegi bugs:

- https://github.com/traefik/yaegi/issues/1632
  - Updating variables in reloaded code has some problems. You can't always just
    use `foo`, sometimes you have use `*&foo`.
- https://github.com/traefik/yaegi/issues/1634
  - Interpreted closures mixed with "binary" functions don't capture closed-over
    values correctly
- https://github.com/traefik/yaegi/issues/1635.
  - Channel send on "binary" channel panics or errors (at compile-time)
- https://github.com/traefik/yaegi/issues/1637
  - The collision resolution mechanism in ImportUsed is insufficient when
    importing > 2 packages with the same base name
  - Can result in "package foo \<path\> has no symbol Bar" errors, when foo.Bar most
    definitely exists.

And of course any other Yaegi bugs; those are just those that I've filed
recently.

Note that not all of these bugs cause the interpreter to throw errors or panic,
sometimes it just behaves incorrectly. See especially the first two, 1632 and
1634. These are what make got-reload "not recommended for production use".

# Who came up with this harebrained idea?

- Larry Clapp [@theclapp](https://github.com/theclapp) 
- Chris Waldon [@whereswaldon](https://github.com/whereswaldon)

Given that Yaegi's been out for a while, and Go's parsing tools have been out
since the beginning (well, a lot of them, anyway), we both wonder why nobody has
done this yet, to be honest.

# Can I use it now?

You can absolutely give it a shot. It works in the demos, and on some functions
in a larger app, but fails in some cases (either the interpreter throws an error
or panics outright). See above under "known bugs". It's possible for it to work
on some functions but not others in the same file. If it works, it's great. :)

# Should I use it for production code?

Probably not. got-reload is currently intended only as an aid during
development.

# Can I support the development of this tool?

Yes! We appreciate stars, watchers, feedback, and, of course, pull requests! A
PR need not necessarily be code, of course; it could be documentation, or
something else. Whatever itch you care to scratch.

You can also sponsor the developers:

- Larry Clapp [@theclapp](https://github.com/sponsors/theclapp) 
- Chris Waldon [@whereswaldon](https://github.com/sponsors/whereswaldon)

