package main

import (
	"eino-cli/host/cli"
	"fmt"
	"os"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
