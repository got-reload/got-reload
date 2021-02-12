package example

import (
	"fmt"
	"reflect"

	"github.com/huckridgesw/got-reload/pkg/gotreload"
)

// Force import of reflect and gotreload for the moment.
var (
	_ = reflect.ValueOf
	_ = gotreload.RegisterAll
)

var (
	I int
)

func F1() int {
	I++
	fmt.Printf("I: %d\n", I)
	return 1
}
