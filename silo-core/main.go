package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Println("Usage: silo <command> [flags] [arguments...]")
	fmt.Println("\nCommands:")
	fmt.Println("  run [--image <name>] <cmd/file> [args...]   Run a command or file inside an isolated container")
	fmt.Println("\nFlags for 'run':")
	fmt.Println("  --image <name>   Explicitly specify base image (e.g. alpine, debian, python, node)")
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
		os.Exit(run(os.Args[2:]))
	case "child":
		child()
	default:
		usage()
	}
}
