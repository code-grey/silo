package main

import (
	"fmt"
	"os"
)

var educationalTrace = true // Enabled by default to demystify container internals

// Thought emits an annotated educational trace showing the live kernel-level transitions
func Thought(subsystem string, format string, a ...interface{}) {
	if !educationalTrace {
		return
	}
	msg := fmt.Sprintf(format, a...)
	// Writes to stderr so process stdout streams cleanly without pollution
	fmt.Fprintf(os.Stderr, "\033[1;36m[silo:thought]\033[0m 🧠 \033[1;35m[%s]\033[0m %s\n", subsystem, msg)
}
