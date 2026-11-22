package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func run(args []string) int {
	var explicitImage string
	var payloadArgs []string

	// Parse flags while allowing standard positional payload arguments
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--image" && i+1 < len(args) {
			explicitImage = args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--image=") {
			explicitImage = strings.TrimPrefix(arg, "--image=")
		} else {
			payloadArgs = args[i:]
			break
		}
	}

	if len(payloadArgs) == 0 {
		fmt.Println("Silo Error: No command or payload specified to run.")
		return 1
	}

	targetPayload := payloadArgs[0]

	// Phase 1: Pre-Flight Runtime Detection
	inspection := InspectPayload(targetPayload)
	var targetImage string
	if explicitImage != "" {
		targetImage = explicitImage
		Thought("PreFlight", "Explicit image specified by user: '%s' (payload: %s)", targetImage, inspection.Reason)
	} else {
		targetImage = inspection.RecommendedImage
		Thought("PreFlight", "Payload analysis: %s ➔ Selected image '%s'", inspection.Reason, targetImage)
	}

	// Phase 2: Ensure Image is cached or pulled/synthesized
	baseRootfs, err := EnsureImage(targetImage)
	if err != nil {
		fmt.Printf("Silo Error: Failed to resolve rootfs for '%s': %v\n", targetImage, err)
		return 1
	}

	// Phase 3: Setup Ephemeral OverlayFS Layer
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

	childArgs := append([]string{}, payloadArgs...)

	// Phase 4: Single-File Payload Injection & Execution Auto-Wrapping
	if _, err := os.Stat(targetPayload); err == nil {
		_, inRootfs := os.Stat(filepath.Join(baseRootfs, targetPayload))
		if inRootfs != nil || strings.HasPrefix(targetPayload, ".") || !filepath.IsAbs(targetPayload) {
			injectedPath, err := overlay.InjectPayload(targetPayload)
			if err != nil {
				fmt.Printf("Silo Error: Failed to inject host payload '%s': %v\n", targetPayload, err)
				return 1
			}
			Thought("Payload", "Ingested host file '%s' into ephemeral upperdir (%s) — host directory remains 100%% private", targetPayload, injectedPath)

			if len(inspection.Runner) > 0 {
				childArgs = append(append([]string{}, inspection.Runner...), injectedPath)
				if len(payloadArgs) > 1 {
					childArgs = append(childArgs, payloadArgs[1:]...)
				}
				Thought("Runner", "Auto-wrapping execution with interpreter: %s", strings.Join(childArgs, " "))
			} else {
				childArgs[0] = injectedPath
			}
		}
	}

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, childArgs...)...)

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
