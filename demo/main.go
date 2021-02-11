package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/huckridgesw/got-reload/demo/example"
	"github.com/huckridgesw/got-reload/pkg/reloader"
)

func main() {
	reloader.Start()

	fmt.Printf("Press enter to call example.F1 repeatedly\n")
	fmt.Printf("Enter s to stop\n")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() == "s" {
			break
		}
		fmt.Printf("example.F1: %d\n", example.F1())
		fmt.Printf("example.F2: %d\n", example.F2())
		fmt.Printf("example.I: %d\n", example.I)
	}
}
