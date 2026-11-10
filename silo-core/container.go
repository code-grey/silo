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
	if err := syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, ""); err != nil {
		panic(fmt.Sprintf("Failed to make root private: %v", err))
	}

	fmt.Printf("Running %v as PID %d inside the container\n", os.Args[2:], os.Getpid())

	if err := syscall.Sethostname([]byte("silo-container")); err != nil {
		fmt.Printf("Setting hostname failed: %v\n", err)
	}

	rootfs := "/tmp/silo-container/rootfs"
	if err := pivotRoot(rootfs); err != nil {
		panic(fmt.Sprintf("PivotRoot failed: %v (Ensure target rootfs exists at %s)", err, rootfs))
	}

	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		panic(fmt.Sprintf("mount proc failed: %v", err))
	}

	path, err := exec.LookPath(os.Args[2])
	if err != nil {
		fmt.Printf("Error finding binary: %v\n", err)
		os.Exit(1)
	}

	if err := syscall.Exec(path, os.Args[2:], os.Environ()); err != nil {
		fmt.Printf("Error running syscall.Exec - %s\n", err)
		os.Exit(1)
	}
}
