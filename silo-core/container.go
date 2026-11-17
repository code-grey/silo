package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	Thought("pivot_root", "Swapping container root to %s and detaching old root.", mergedDir)
	if err := pivotRoot(mergedDir); err != nil {
		panic(fmt.Sprintf("PivotRoot failed: %v (Ensure target rootfs exists at %s)", err, mergedDir))
	}

	Thought("ProcFS", "Mounting fresh /proc virtual filesystem inside isolated PID namespace.")
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		panic(fmt.Sprintf("mount proc failed: %v", err))
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
