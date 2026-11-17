package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func run() int {
	baseRootfs := "/tmp/silo-container/rootfs"
	if _, err := os.Stat(baseRootfs); os.IsNotExist(err) {
		fmt.Printf("Silo Error: Base rootfs not found at %s. Please run scripts/setup_rootfs.sh first.\n", baseRootfs)
		return 1
	}

	Thought("Storage", "Preparing ephemeral OverlayFS workspace on host...")
	overlay, err := PrepareOverlay(baseRootfs)
	if err != nil {
		fmt.Printf("Silo Error: Failed to prepare overlayfs: %v\n", err)
		return 1
	}
	defer func() {
		Thought("Storage", "Purging ephemeral OverlayFS layer (%s)...", overlay.InstanceDir)
		_ = overlay.Cleanup()
	}()

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, os.Args[2:]...)...)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
	}

	// Propagate overlay configuration into the container process environment
	cmd.Env = append(os.Environ(),
		"SILO_INSTANCE="+overlay.InstanceDir,
		"SILO_LOWER="+overlay.LowerDir,
		"SILO_UPPER="+overlay.UpperDir,
		"SILO_WORK="+overlay.WorkDir,
		"SILO_MERGED="+overlay.MergedDir,
	)

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	Thought("Namespaces", "Spawning container supervisor with CLONE_NEWUTS | CLONE_NEWPID | CLONE_NEWNS...")
	if err := cmd.Start(); err != nil {
		fmt.Printf("Error running the run command - %s\n", err)
		return 1
	}
	fmt.Printf("Silo: Container booted successfully (Host PID: %d)\n", cmd.Process.Pid)

	Thought("Cgroups v2", "Allocating /sys/fs/cgroup/silo-%d and attaching host PID %d...", cmd.Process.Pid, cmd.Process.Pid)
	cleanupCgroup, err := setupCgroup(cmd.Process.Pid)
	if err != nil {
		fmt.Printf("Silo Error: Failed to setup cgroups: %v\n", err)
		_ = cmd.Process.Kill()
		return 1
	}
	defer func() {
		Thought("Cgroups v2", "Destroying cgroup /sys/fs/cgroup/silo-%d...", cmd.Process.Pid)
		cleanupCgroup()
	}()

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
		inspectCgroup(cmd.Process.Pid)
		if exitError, ok := cmdErr.(*exec.ExitError); ok {
			return exitError.ExitCode()
		}
		return 1
	}
	return 0
}
