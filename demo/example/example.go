package example

import "fmt"

var (
	I int
)

func F1() int {
	I++
	fmt.Printf("I: %d\n", I)
	return 1
}
