module github.com/got-reload/got-reload

go 1.22.1

// replace github.com/traefik/yaegi => ../../traefik/yaegi

require (
	github.com/fsnotify/fsnotify v1.7.0
	github.com/stretchr/testify v1.9.0
	github.com/traefik/yaegi v0.16.2-0.20240730175404-e686f55767b9
	golang.org/x/mod v0.19.0
	golang.org/x/tools v0.23.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
