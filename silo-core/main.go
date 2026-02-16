package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func main() {
	switch os.Args[1] {
	case "run":
		run()
	case "child":
		child()
	default:
		panic("help")
	}
}

func run() {
	fmt.Printf("Running %v \n", os.Args[2:])
	cmd := exec.Command("/proc/self/exe", append([]string{"child"}, os.Args[2:]...)...)

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUTS | syscall.CLONE_NEWPID | syscall.CLONE_NEWNS,
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Printf("Error running the run command - %s\n", err)
		os.Exit(1)
	}
}

func child() {
	fmt.Printf("Running %v as PID %d\n", os.Args[2:], os.Getegid())

	syscall.Sethostname([]byte("silo-container"))

	if err := syscall.Chroot("/tmp/silo-container/rootfs"); err != nil {
		panic(fmt.Sprintf("Chroot failed: %v (Did you create /tmp/silo-container/rootfs ?)", err))
	}

	if err := syscall.Chdir("/"); err != nil {
		panic(err)
	}

	syscall.Mount("proc", "proc", "proc", 0, "")

	cmd := exec.Command(os.Args[2], os.Args[3:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		fmt.Printf("Error running child command - %s\n", err)
		os.Exit(1)
	}

	syscall.Unmount("proc", 0)
}
