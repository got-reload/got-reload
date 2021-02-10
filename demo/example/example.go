package example

import (
	"reflect"

	"github.com/huckridgesw/got-reload/pkg/gotreload"
)

var (
	_ = reflect.ValueOf
	_ = gotreload.Register
)

func F1() int {
	return 1
}
