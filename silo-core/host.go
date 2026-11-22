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

// RunConfig holds the validated configuration for container execution
type RunConfig struct {
	Image       string
	Volumes     []string
	Workdir     string
	PayloadArgs []string
}

// parseVolumeArg parses a volume string (e.g. ".", "./data:/data:ro", "/host:/container")
func parseVolumeArg(vol string) string {
	parts := strings.Split(vol, ":")
	if len(parts) == 0 || parts[0] == "" {
		return ""
	}

	hostPath := parts[0]
	if hostPath == "." {
		if cwd, err := os.Getwd(); err == nil {
			hostPath = cwd
		}
	} else if abs, err := filepath.Abs(hostPath); err == nil {
		hostPath = abs
	}

	containerPath := "/workspace"
	mode := "rw"
	if len(parts) >= 2 && parts[1] != "" {
		containerPath = parts[1]
		if !filepath.IsAbs(containerPath) {
			containerPath = "/" + containerPath
		}
	}
	if len(parts) >= 3 && parts[2] == "ro" {
		mode = "ro"
	}

	return fmt.Sprintf("%s:%s:%s", hostPath, containerPath, mode)
}

// parseRunConfig extracts and validates flags from the command line arguments
func parseRunConfig(args []string) (*RunConfig, error) {
	cfg := &RunConfig{}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--image" && i+1 < len(args) {
			cfg.Image = args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--image=") {
			cfg.Image = strings.TrimPrefix(arg, "--image=")
		} else if (arg == "-w" || arg == "--workdir") && i+1 < len(args) {
			cfg.Workdir = args[i+1]
			i++
		} else if strings.HasPrefix(arg, "-w=") || strings.HasPrefix(arg, "--workdir=") {
			cfg.Workdir = strings.TrimPrefix(strings.TrimPrefix(arg, "-w="), "--workdir=")
		} else if arg == "-v" || arg == "--volume" {
			if i+1 < len(args) && (strings.Contains(args[i+1], ":") || args[i+1] == ".") {
				volArg := args[i+1]
				i++
				if v := parseVolumeArg(volArg); v != "" {
					cfg.Volumes = append(cfg.Volumes, v)
				}
			} else {
				if cwd, err := os.Getwd(); err == nil {
					cfg.Volumes = append(cfg.Volumes, fmt.Sprintf("%s:/workspace:rw", cwd))
				}
			}
		} else if strings.HasPrefix(arg, "-v=") || strings.HasPrefix(arg, "--volume=") {
			volArg := strings.TrimPrefix(strings.TrimPrefix(arg, "-v="), "--volume=")
			if v := parseVolumeArg(volArg); v != "" {
				cfg.Volumes = append(cfg.Volumes, v)
			}
		} else {
			cfg.PayloadArgs = args[i:]
			break
		}
	}

	if len(cfg.PayloadArgs) == 0 {
		return nil, fmt.Errorf("no command or payload specified to run")
	}

	// Auto-default working directory to /workspace if a volume is mounted to /workspace
	if cfg.Workdir == "" && len(cfg.Volumes) > 0 {
		for _, v := range cfg.Volumes {
			if strings.Contains(v, ":/workspace:") {
				cfg.Workdir = "/workspace"
				break
			}
		}
	}

	return cfg, nil
}

func run(cfg *RunConfig) int {
	targetPayload := cfg.PayloadArgs[0]

	// Phase 1: Pre-Flight Runtime Detection
	inspection := InspectPayload(targetPayload)
	targetImage := inspection.RecommendedImage
	if cfg.Image != "" {
		targetImage = cfg.Image
		Thought("PreFlight", "Explicit image specified by user: '%s' (payload: %s)", targetImage, inspection.Reason)
	} else {
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

	childArgs := append([]string{}, cfg.PayloadArgs...)

	// Phase 4: Single-File Payload Injection (only if no workspace volume is mounted)
	if len(cfg.Volumes) == 0 {
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
					if len(cfg.PayloadArgs) > 1 {
						childArgs = append(childArgs, cfg.PayloadArgs[1:]...)
					}
					Thought("Runner", "Auto-wrapping execution with interpreter: %s", strings.Join(childArgs, " "))
				} else {
					childArgs[0] = injectedPath
				}
			}
		}
	} else {
		// Volume mounted: auto-wrap runner if executing a script directly in the workspace
		if len(inspection.Runner) > 0 && len(cfg.PayloadArgs) > 0 && !strings.Contains(cfg.PayloadArgs[0], inspection.Runner[0]) {
			childArgs = append(append([]string{}, inspection.Runner...), cfg.PayloadArgs...)
			Thought("Runner", "Auto-wrapping workspace execution with interpreter: %s", strings.Join(childArgs, " "))
		}
	}

	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, childArgs...)...)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
	}

	// Propagate overlay configuration and volume specifications into container environment
	cmd.Env = append(os.Environ(),
		"SILO_INSTANCE="+overlay.InstanceDir,
		"SILO_LOWER="+overlay.LowerDir,
		"SILO_UPPER="+overlay.UpperDir,
		"SILO_WORK="+overlay.WorkDir,
		"SILO_MERGED="+overlay.MergedDir,
	)

	if len(cfg.Volumes) > 0 {
		cmd.Env = append(cmd.Env, "SILO_VOLUMES="+strings.Join(cfg.Volumes, ";"))
		Thought("Storage", "Volume mount configured: %s", strings.Join(cfg.Volumes, ", "))
	}
	if cfg.Workdir != "" {
		cmd.Env = append(cmd.Env, "SILO_WORKDIR="+cfg.Workdir)
		Thought("Storage", "Target working directory: '%s'", cfg.Workdir)
	}

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
