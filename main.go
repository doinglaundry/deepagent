package main

import (
	"log"

	"eino-cli/cmd"
)

func main() {
	err := cmd.Run()
	if err != nil {
		log.Fatal(err)
	}
}
