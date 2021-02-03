# The goal

Be able to hot-reload code at function/method-level granularity in a running
process.

# The roadmap in broad strokes

## Use the `-toolexec` flag of `go build` to install a source filter into the compilation process.  

From `go help build`:

	-toolexec 'cmd args'
		a program to use to invoke toolchain programs like vet and asm.
		For example, instead of running asm, the go command will run
		'cmd args /path/to/asm <arguments for asm>'.

The filter will transparently change functions from this

```go
func Foo(... args ...) (...return values...) {
  // body
}
```

into this

```go
func Foo(... args ...) (...return values...) {
  return YY_Foo(...args...)
}

var YY_Foo = func(...args...) (...return values...) {
   // body
}

func YY_SetFoo(f func(...Foo's signature)...) {
  YY_Foo = f
}
```

and similarly for methods.

## A thread will watch for changes in all filtered files

When a filtered source file changes, it will be read, parsed, and changed
functions will be installed with new versions of themselves via the generated
YY_Set* functions, via [Yaegi](https://github.com/traefik/yaegi), a Go
interpreter.

# Limitations

I'm not positive, but I'm pretty sure it's not gonna be possible to change the
signature of a function, change a type, declare a new type or variable, and
probably several other things.

I've performed the above function replacement process by hand in a real Go
program, so I know that at least that much _is_ possible.

It's not everything, but it's not nothing, either.

# Who came up with this hair-brained idea?

- Larry Clapp [@theclapp](https://github.com/theclapp) 
- Chris Waldon [@whereswaldon](https://github.com/whereswaldon)

Given that Yaegi's been out for a while, and Go's parsing tools have been out
since the beginning (well, a lot of them, anyway), we both wonder why nobody
has done this yet, to be honest.

# Can I use it now?

No.  We've been working on this for, like, three days, as of this writing.
We're still working on the basic translation bit.

Watch/Star/Sponsor, as the spirit moves you.

# Do you have a demo?

See this video that Chris did for something similar:

https://user-images.githubusercontent.com/2324697/106301108-4e2fce80-6225-11eb-8038-1d726b3eb269.mp4
