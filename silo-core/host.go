package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func run() {
	fmt.Printf("Running %v \n", os.Args[2:])
	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, os.Args[2:]...)...)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		fmt.Printf("Error running the run command - %s\n", err)
		os.Exit(1)
	}
	fmt.Printf("Silo: Container booted successfully (Host PID: %d)\n", cmd.Process.Pid)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	done := make(chan error, 1)

	go func() {
		done <- cmd.Wait()
	}()

	var cmdErr error

	select {
	case cmdErr = <-done:
		if cmdErr != nil {
			fmt.Printf("Silo: Container exited with error: %v\n", cmdErr)
		} else {
			fmt.Println("Silo: Container exited cleanly.")
		}

	case sig := <-sigChan:
		fmt.Printf("\nSilo: Intercepted %v. Proxying SIGTERM to container PID %d...\n", sig, cmd.Process.Pid)

		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			fmt.Printf("Silo Error: Failed to signal container: %v\n", err)
		}

		select {
		case cmdErr = <-done:
			fmt.Println("Silo: Container safely terminated.")
		case <-time.After(10 * time.Second):
			fmt.Println("Silo: Timeout reached (10s). Force killing container (SIGKILL)...")
			_ = cmd.Process.Kill()
			cmdErr = <-done
		case <-sigChan:
			fmt.Println("\nSilo: Second signal intercepted. Force killing container (SIGKILL)...")
			_ = cmd.Process.Kill()
			cmdErr = <-done
		}
	}

	if cmdErr != nil {
		if exitError, ok := cmdErr.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		os.Exit(1)
	}
}
