package reloadable

import (
	"reflect"

	"github.com/got-reload/got-reload/pkg/gotreload"
	_ "github.com/got-reload/got-reload/pkg/reloader/start"
)

var (
	_ = reflect.ValueOf
	_ = gotreload.RegisterAll
)
