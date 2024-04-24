module github.com/got-reload/got-reload

go 1.22.1

// replace github.com/traefik/yaegi => /Users/lmc/goget/src/github.com/traefik/yaegi
replace github.com/traefik/yaegi => ../../traefik/yaegi

require (
	github.com/fsnotify/fsnotify v1.5.1
	github.com/smartystreets/goconvey v1.7.2
	github.com/traefik/yaegi v0.16.1
	golang.org/x/tools v0.20.0
)

require (
	github.com/gopherjs/gopherjs v0.0.0-20181017120253-0766667cb4d1 // indirect
	github.com/jtolds/gls v4.20.0+incompatible // indirect
	github.com/smartystreets/assertions v1.2.0 // indirect
	golang.org/x/mod v0.17.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.19.0 // indirect
)
