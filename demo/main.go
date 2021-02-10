package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/huckridgesw/got-reload/demo/example"
	_ "github.com/huckridgesw/got-reload/pkg/reloader"
)

func main() {
	fmt.Printf("Press enter to call example.F1 repeatedly\n")
	fmt.Printf("Enter s to stop\n")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if scanner.Text() == "s" {
			break
		}
		fmt.Printf("Example.F1: %d\n", example.F1())
	}
}
