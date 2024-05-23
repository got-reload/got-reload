module github.com/got-reload/got-reload

go 1.22.1

// replace github.com/traefik/yaegi => /Users/lmc/goget/src/github.com/traefik/yaegi
// replace github.com/traefik/yaegi => ../../traefik/yaegi

require (
	github.com/fsnotify/fsnotify v1.7.0
	github.com/stretchr/testify v1.9.0
	github.com/traefik/yaegi v0.16.2-0.20240430170404-381e045966b0
	golang.org/x/mod v0.17.0
	golang.org/x/tools v0.21.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.20.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
