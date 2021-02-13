package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/huckridgesw/got-reload/demo/example"
	"github.com/huckridgesw/got-reload/demo/example2"
)

func main() {
	fmt.Printf("Press enter to call example.F1 and example2.F2 repeatedly\n")
	fmt.Printf("Enter s to stop\n")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() == "s" {
			break
		}
		fmt.Printf("example.F1: %d\n", example.F1())
		fmt.Printf("example2.F2: %d\n", example2.F2())
		fmt.Printf("example.I: %d\n", example.I)
	}
}
