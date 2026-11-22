package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// OverlayFS holds the directory topology for a single ephemeral container instance
type OverlayFS struct {
	InstanceDir string
	LowerDir    string
	UpperDir    string
	WorkDir     string
	MergedDir   string
}

// PrepareOverlay creates the ephemeral upper, work, and merged directories on the host
func PrepareOverlay(baseRootfs string) (*OverlayFS, error) {
	// Unique instance ID prevents collisions between parallel containers
	instanceID := fmt.Sprintf("inst-%d", time.Now().UnixNano()%100000000)
	instanceDir := filepath.Join("/tmp/silo/instances", instanceID)

	upperDir := filepath.Join(instanceDir, "upper")
	workDir := filepath.Join(instanceDir, "work")
	mergedDir := filepath.Join(instanceDir, "merged")

	// Ensure all required directories exist
	for _, dir := range []string{upperDir, workDir, mergedDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			_ = os.RemoveAll(instanceDir)
			return nil, fmt.Errorf("failed to create overlay directory %s: %w", dir, err)
		}
	}

	return &OverlayFS{
		InstanceDir: instanceDir,
		LowerDir:    baseRootfs,
		UpperDir:    upperDir,
		WorkDir:     workDir,
		MergedDir:   mergedDir,
	}, nil
}

// InjectPayload copies a host file into the container's ephemeral upperdir/
// making it executable inside the container while respecting UsrMerge symlinks (e.g. bin -> usr/bin).
func (o *OverlayFS) InjectPayload(hostPath string) (string, error) {
	info, err := os.Stat(hostPath)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path '%s' is a directory, not a file", hostPath)
	}

	// Respect UsrMerge: If lowerdir/bin is a symlink (e.g. bin -> usr/bin in Debian/Ubuntu),
	// inject into upperdir/usr/bin to avoid shadowing the lowerdir symlink with a directory.
	binDirName := "bin"
	if linkTarget, err := os.Readlink(filepath.Join(o.LowerDir, "bin")); err == nil {
		binDirName = filepath.Clean(strings.TrimPrefix(linkTarget, "/"))
	}

	targetDir := filepath.Join(o.UpperDir, binDirName)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create upper/%s: %w", binDirName, err)
	}

	baseName := filepath.Base(hostPath)
	targetFile := filepath.Join(targetDir, baseName)

	src, err := os.Open(hostPath)
	if err != nil {
		return "", fmt.Errorf("failed to open host payload: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(targetFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("failed to write payload to upperdir: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("failed to copy payload into upperdir: %w", err)
	}

	return filepath.Join("/"+binDirName, baseName), nil
}

// Mount performs the union mount inside the container's private mount namespace
func (o *OverlayFS) Mount() error {
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", o.LowerDir, o.UpperDir, o.WorkDir)
	if err := syscall.Mount("overlay", o.MergedDir, "overlay", 0, opts); err != nil {
		return fmt.Errorf("failed to mount overlayfs at %s: %w", o.MergedDir, err)
	}
	return nil
}

// Cleanup permanently removes the ephemeral upper and work directories from disk.
// It enforces strict mount-boundary safety: unmounting mergedDir with MNT_DETACH
// and only performing non-recursive removal on mergedDir to guarantee it NEVER deletes into host volumes.
func (o *OverlayFS) Cleanup() error {
	// Step 1: Explicitly unmount mergedDir to sever any lingering kernel mount references
	_ = syscall.Unmount(o.MergedDir, syscall.MNT_DETACH)

	// Step 2: Safely remove the ephemeral upper and work scratch spaces
	_ = os.RemoveAll(o.UpperDir)
	_ = os.RemoveAll(o.WorkDir)

	// Step 3: Remove mergedDir non-recursively. If a mount is still attached,
	// os.Remove will safely fail with EBUSY instead of traversing and deleting host files.
	_ = os.Remove(o.MergedDir)

	// Step 4: Remove the instance root directory if empty
	return os.Remove(o.InstanceDir)
}
