package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func pivotRoot(newroot string) (err error) {
	if err := syscall.Mount(newroot, newroot, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind mount failed: %v", err)
	}

	putold := filepath.Join(newroot, ".oldroot")
	if err := os.MkdirAll(putold, 0700); err != nil {
		return fmt.Errorf("mkdir .oldroot failed: %v", err)
	}

	defer func() {
		if err != nil {
			_ = os.Remove(putold)
		}
	}()

	if err = syscall.PivotRoot(newroot, putold); err != nil {
		return fmt.Errorf("pivot_root failed: %v", err)
	}

	if err = syscall.Chdir("/"); err != nil {
		return fmt.Errorf("chdir / failed: %v", err)
	}

	putoldInsideRoot := "/.oldroot"

	if err = syscall.Unmount(putoldInsideRoot, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount .oldroot failed: %v", err)
	}

	return os.Remove(putoldInsideRoot)
}

// setupDev configures a secure, isolated /dev filesystem in RAM (tmpfs)
// with essential device nodes (null, zero, full, random, urandom, tty), /dev/shm, and POSIX stream links.
func setupDev() error {
	Thought("DevFS", "Mounting isolated /dev tmpfs with safe device nodes and /dev/shm...")

	// 1. Mount isolated tmpfs on /dev with MS_NOSUID
	if err := syscall.Mount("tmpfs", "/dev", "tmpfs", syscall.MS_NOSUID|syscall.MS_STRICTATIME, "mode=755"); err != nil {
		return fmt.Errorf("mount /dev tmpfs failed: %w", err)
	}

	// 2. Bind-mount standard character device nodes from host
	safeDevices := []string{
		"/dev/null",
		"/dev/zero",
		"/dev/full",
		"/dev/random",
		"/dev/urandom",
		"/dev/tty",
	}
	for _, dev := range safeDevices {
		target := filepath.Join("/dev", filepath.Base(dev))
		if f, err := os.OpenFile(target, os.O_CREATE|os.O_RDONLY, 0666); err == nil {
			f.Close()
			_ = syscall.Mount(dev, target, "", syscall.MS_BIND|syscall.MS_NOSUID|syscall.MS_NODEV, "")
		}
	}

	// 3. Mount /dev/shm (POSIX shared memory)
	shmDir := "/dev/shm"
	_ = os.MkdirAll(shmDir, 0777)
	_ = syscall.Mount("tmpfs", shmDir, "tmpfs", syscall.MS_NOSUID|syscall.MS_NODEV, "mode=1777")

	// 4. Create standard POSIX file descriptor symlinks
	_ = os.Symlink("/proc/self/fd", "/dev/fd")
	_ = os.Symlink("/proc/self/fd/0", "/dev/stdin")
	_ = os.Symlink("/proc/self/fd/1", "/dev/stdout")
	_ = os.Symlink("/proc/self/fd/2", "/dev/stderr")

	return nil
}

// mountVolumes parses and bind-mounts host volumes into mergedDir prior to pivot_root
func mountVolumes(mergedDir, volumeSpec string) error {
	if volumeSpec == "" {
		return nil
	}

	vols := strings.Split(volumeSpec, ";")
	for _, vol := range vols {
		vol = strings.TrimSpace(vol)
		if vol == "" {
			continue
		}

		parts := strings.Split(vol, ":")
		if len(parts) < 2 {
			continue
		}

		hostPath := parts[0]
		containerPath := parts[1]
		mode := "rw"
		if len(parts) >= 3 && parts[2] == "ro" {
			mode = "ro"
		}

		targetPath := filepath.Join(mergedDir, containerPath)
		if err := os.MkdirAll(targetPath, 0755); err != nil {
			return fmt.Errorf("failed to create volume mount point %s: %w", targetPath, err)
		}

		// Step 1: Bind mount with MS_NOSUID and MS_NODEV
		if err := syscall.Mount(hostPath, targetPath, "", syscall.MS_BIND|syscall.MS_REC|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
			return fmt.Errorf("failed to bind mount %s to %s: %w", hostPath, targetPath, err)
		}

		// Step 2: If read-only, execute the Linux two-step remount dance
		if mode == "ro" {
			if err := syscall.Mount("", targetPath, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_REC|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
				return fmt.Errorf("failed to remount %s read-only: %w", targetPath, err)
			}
			Thought("Mount", "Bound host '%s' ➔ container '%s' [read-only]", hostPath, containerPath)
		} else {
			Thought("Mount", "Bound host '%s' ➔ container '%s' [read-write]", hostPath, containerPath)
		}
	}
	return nil
}

func child() {
	Thought("MountNamespace", "Isolated mount namespace active. Setting MS_PRIVATE propagation on host root.")
	if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
		panic(fmt.Sprintf("Failed to make root private: %v", err))
	}

	Thought("UTSNamespace", "Setting isolated container hostname to 'silo-container'.")
	if err := syscall.Sethostname([]byte("silo-container")); err != nil {
		fmt.Printf("Setting hostname failed: %v\n", err)
	}

	mergedDir := os.Getenv("SILO_MERGED")
	if mergedDir != "" {
		overlay := &OverlayFS{
			InstanceDir: os.Getenv("SILO_INSTANCE"),
			LowerDir:    os.Getenv("SILO_LOWER"),
			UpperDir:    os.Getenv("SILO_UPPER"),
			WorkDir:     os.Getenv("SILO_WORK"),
			MergedDir:   mergedDir,
		}
		Thought("OverlayFS", "Stacking lowerdir (read-only) + upperdir (ephemeral) onto %s", overlay.MergedDir)
		if err := overlay.Mount(); err != nil {
			panic(fmt.Sprintf("OverlayFS mount failed: %v", err))
		}
	} else {
		mergedDir = "/tmp/silo-container/rootfs"
	}

	// Mount any requested host volumes into mergedDir before pivot_root
	if volSpec := os.Getenv("SILO_VOLUMES"); volSpec != "" {
		if err := mountVolumes(mergedDir, volSpec); err != nil {
			panic(fmt.Sprintf("Volume mount failed: %v", err))
		}
	}

	Thought("pivot_root", "Swapping container root to %s and detaching old root.", mergedDir)
	if err := pivotRoot(mergedDir); err != nil {
		panic(fmt.Sprintf("PivotRoot failed: %v (Ensure target rootfs exists at %s)", err, mergedDir))
	}

	Thought("ProcFS", "Mounting fresh /proc virtual filesystem inside isolated PID namespace.")
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		panic(fmt.Sprintf("mount proc failed: %v", err))
	}

	// Setup /dev virtual filesystem with safe device nodes & /dev/shm
	if err := setupDev(); err != nil {
		panic(fmt.Sprintf("setup /dev failed: %v", err))
	}

	// Switch to requested working directory if provided
	if workdir := os.Getenv("SILO_WORKDIR"); workdir != "" {
		Thought("Workdir", "Switching working directory to '%s'", workdir)
		if err := syscall.Chdir(workdir); err != nil {
			fmt.Printf("Warning: failed to chdir to %s: %v\n", workdir, err)
		}
	}

	path, err := exec.LookPath(os.Args[2])
	if err != nil {
		fmt.Printf("Error finding binary: %v\n", err)
		os.Exit(1)
	}

	Thought("Exec", "Invoking syscall.Exec(%s) — Go runtime replaced, payload is PID 1!", path)
	if err := syscall.Exec(path, os.Args[2:], os.Environ()); err != nil {
		fmt.Printf("Error running syscall.Exec - %s\n", err)
		os.Exit(1)
	}
}
