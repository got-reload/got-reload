package example

import (
	"fmt"
	"reflect"

	"github.com/huckridgesw/got-reload/pkg/gotreload"
)

var (
	_ = reflect.ValueOf
	_ = gotreload.Register
)

var (
	I int
)

func F1() int {
	I++
	fmt.Printf("I: %d\n", I)
	return 2
}
