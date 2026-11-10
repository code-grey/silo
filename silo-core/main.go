package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Println("Usage: silo <command> [arguments...]")
	fmt.Println("\nCommands:")
	fmt.Println("  run <cmd> [args...]   Run a command inside a new isolated container")
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "run":
		if len(os.Args) < 3 {
			usage()
		}
		run()
	case "child":
		child()
	default:
		usage()
	}
}
