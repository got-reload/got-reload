package main

import (
	"go/format"
	"go/token"
	"io/ioutil"
	"log"
	"os"

	"github.com/huckridgesw/hot-reload/pkg/hotreload"
)

type ExitCode int

const (
	Success ExitCode = iota
	FailedOpenSrc
	FailedOpenDest
	FailedCloseDest
	FailedRead
	FailedWrite
	FailedParse
	FailedTruncate
)

func main() {
	// TODO: only do something if we're being invoked as part of the act
	// of compiling source code. Otherwise, just exit success.
	// TODO: find the actual file name in the compiler command, using the
	// first arg like this is easy to test manually, but objectively wrong
	// for interoperating with the compiler flags.
	targetFileName := os.Args[1]
	targetFile, err := os.Open(targetFileName)
	if err != nil {
		log.Printf("Failed opening source file: %v", err)
		os.Exit(int(FailedOpenSrc))
	}
	defer targetFile.Close()
	source, err := ioutil.ReadAll(targetFile)
	if err != nil {
		log.Printf("Failed reading source file: %v", err)
		os.Exit(int(FailedRead))
	}

	log.Printf("Args: %v", os.Args)
	nodes, err := hotreload.Parse("", string(source))
	if err != nil {
		log.Printf("Failed parsing: %v", err)
		os.Exit(int(FailedParse))
	}
	nodes = hotreload.Rewrite(nodes)

	outputFile, err := ioutil.TempFile("", "hotreloadable-*-"+targetFileName)
	if err != nil {
		log.Printf("Failed opening dest file: %v", err)
		os.Exit(int(FailedOpenDest))
	}
	outFileName := outputFile.Name()
	defer func() {
		if err := outputFile.Close(); err != nil {
			log.Printf("Failed closing file: %v", err)
			os.Exit(int(FailedCloseDest))
		}
		log.Printf("Written to %s", outFileName)
	}()
	if err := format.Node(outputFile, token.NewFileSet(), nodes); err != nil {
		log.Printf("Failed formatting results: %v", err)
		os.Exit(int(FailedWrite))
	}
}
