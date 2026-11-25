package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const Version = "v0.4.1"

func usage(exitCode int) {
	fmt.Printf("Silo Container Runtime %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	fmt.Println("A lightweight, zero-config, transparent container engine for Linux.")
	fmt.Println()
	fmt.Println("Usage: silo <command> [flags] [arguments...]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run [flags] <payload> [args...]   Run a command, binary, or script inside an isolated container")
	fmt.Println("  images                            List all locally cached and synthesized container images")
	fmt.Println("  purge [flags]                     Reclaim disk space by cleaning cached images and stale layers")
	fmt.Println("  wrench [check|fix] [-y]           Diagnose and repair rootless kernel/AppArmor/cgroup prerequisites")
	fmt.Println("  version                           Print version and runtime architecture information")
	fmt.Println("  help                              Show this help message")
	fmt.Println()
	fmt.Println("Flags for 'run':")
	fmt.Println("  -v, --volume [host:target[:ro]]   Bind mount host directory (bare -v auto-binds pwd to /workspace)")
	fmt.Println("  -w, --workdir <path>              Set initial working directory inside container")
	fmt.Println("  --image <name>                    Explicitly specify base image (e.g. alpine, debian, python, node)")
	fmt.Println()
	fmt.Println("Flags for 'purge':")
	fmt.Println("  --all                             Purge images, instances, and crash dumps (default)")
	fmt.Println("  --images                          Purge only cached container images")
	fmt.Println("  --instances                       Purge only abandoned ephemeral instance layers")
	fmt.Println("  --crashes                         Purge only crash post-mortem reports")
	os.Exit(exitCode)
}

func printVersion() {
	fmt.Printf("Silo Container Runtime %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
	fmt.Println("Storage Engine: Ephemeral OverlayFS (kernel)")
	fmt.Println("Isolation: Namespaces (PID, Mount, UTS, User/Rootless) + Cgroups v2")
}

// ensureUserScope ensures rootless containers run within the user's delegated systemd slice,
// allowing full Cgroups v2 control without requiring root or sudo.
func ensureUserScope() {
	if os.Geteuid() == 0 || os.Getenv("SILO_IN_USER_SCOPE") != "" {
		return
	}

	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return
	}
	cgroup := strings.TrimSpace(string(data))

	// If running inside a terminal session scope (session-*.scope), migrate into user@<uid>.service
	if strings.Contains(cgroup, "session-") && strings.Contains(cgroup, ".scope") {
		if _, err := exec.LookPath("systemd-run"); err == nil {
			os.Setenv("SILO_IN_USER_SCOPE", "1")
			cmd := exec.Command("systemd-run", append([]string{"--user", "--scope", "--quiet", "--", os.Args[0]}, os.Args[1:]...)...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					os.Exit(exitErr.ExitCode())
				}
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		usage(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "run":
		if len(args) == 0 {
			usage(1)
		}
		ensureUserScope()
		cfg, err := parseRunConfig(args)
		if err != nil {
			fmt.Printf("Silo Error: %v\n", err)
			os.Exit(1)
		}
		os.Exit(run(cfg))

	case "images", "image", "list":
		listImages()

	case "purge", "prune", "clean":
		flags := PurgeFlags{All: true}
		if len(args) > 0 {
			flags.All = false
			for _, arg := range args {
				switch arg {
				case "--all":
					flags.All = true
				case "--images":
					flags.Images = true
				case "--instances":
					flags.Instances = true
				case "--crashes":
					flags.Crashes = true
				case "-f", "--force":
					flags.Force = true
				default:
					fmt.Printf("Unknown purge flag: %s\n", arg)
					os.Exit(1)
				}
			}
			if !flags.Images && !flags.Instances && !flags.Crashes {
				flags.All = true
			}
		}
		os.Exit(purge(flags))

	case "wrench":
		os.Exit(runWrench(args))

	case "__probe-userns":
		os.Exit(0)

	case "__synth":
		if len(args) != 2 {
			os.Exit(1)
		}
		os.Exit(runSynthChild(args[0], args[1]))

	case "version", "-v", "--version":
		printVersion()

	case "help", "-h", "--help":
		usage(0)

	case "child":
		child()

	default:
		// If user typed: `silo ./app.py` or `silo /bin/sh` without `run`, allow running directly
		if strings.HasPrefix(cmd, "-") || strings.Contains(cmd, "/") || strings.Contains(cmd, ".") {
			cfg, err := parseRunConfig(os.Args[1:])
			if err == nil {
				os.Exit(run(cfg))
			}
		}
		usage(1)
	}
}
