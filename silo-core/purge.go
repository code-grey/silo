package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// PurgeFlags configures which storage categories to clean
type PurgeFlags struct {
	All       bool
	Images    bool
	Instances bool
	Crashes   bool
	Force     bool
}

// removeAllSafe robustly deletes a directory tree, ensuring write permissions on read-only files/directories first.
func removeAllSafe(path string) error {
	_ = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			if info.IsDir() {
				_ = os.Chmod(p, 0700)
			} else {
				_ = os.Chmod(p, 0600)
			}
		}
		return nil
	})
	return os.RemoveAll(path)
}

// fixSudoOwnership ensures that if Silo was executed under sudo, any created path inside
// the user's home directory is owned by the real invoking user (SUDO_USER) rather than root.
func fixSudoOwnership(path string) {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && os.Geteuid() == 0 {
		if u, err := user.Lookup(sudoUser); err == nil {
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			_ = os.Chown(path, uid, gid)
			if parent := filepath.Dir(path); parent != "" && parent != "/" {
				_ = os.Chown(parent, uid, gid)
			}
		}
	}
}

// formatBytes returns human-readable formatted file size strings
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// getDirSize recursively calculates the total disk usage of a directory in bytes
func getDirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// listImages scans and displays all cached container rootfs environments
func listImages() {
	imagesDir := GetImagesDir()
	entries, err := os.ReadDir(imagesDir)
	if err != nil || len(entries) == 0 {
		fmt.Printf("No cached container images found in %s\n", imagesDir)
		return
	}

	fmt.Printf("%-12s %-12s %-14s %-10s %s\n", "IMAGE", "STATUS", "TYPE", "SIZE", "PATH")
	fmt.Println(strings.Repeat("-", 75))

	var totalSize int64
	count := 0

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		imgPath := filepath.Join(imagesDir, name)
		size, _ := getDirSize(imgPath)
		totalSize += size

		imgType := "Custom"
		if spec, ok := ImageCatalog[name]; ok {
			if spec.ApkPackage != "" {
				imgType = "Synthesized"
			} else {
				imgType = "Official"
			}
		}

		status := "Valid"
		if !isRootfsValid(filepath.Join(imgPath, "rootfs")) {
			status = "Incomplete"
		}

		fmt.Printf("%-12s %-12s %-14s %-10s %s\n", name, status, imgType, formatBytes(size), imgPath)
		count++
	}

	fmt.Println(strings.Repeat("-", 75))
	fmt.Printf("Total cached images: %d (%s)\n", count, formatBytes(totalSize))
}

// purge cleans cached images, ephemeral instance layers, and crash dumps
func purge(flags PurgeFlags) int {
	fmt.Println("[silo:purge] 🧹 Reclaiming storage and cleaning artifacts...")
	var totalReclaimed int64

	// 1. Purge Images if requested
	if flags.All || flags.Images {
		imagesDir := GetImagesDir()
		if size, err := getDirSize(imagesDir); err == nil && size > 0 {
			if err := removeAllSafe(imagesDir); err == nil {
				_ = os.MkdirAll(imagesDir, 0755)
				fixSudoOwnership(imagesDir)
				fmt.Printf("✓ Purged image cache (%s) [%s]\n", imagesDir, formatBytes(size))
				totalReclaimed += size
			} else {
				fmt.Printf("⚠️ Failed to purge image cache %s: %v\n", imagesDir, err)
			}
		} else {
			fixSudoOwnership(imagesDir)
			fmt.Println("• No image caches to purge.")
		}
	}

	// 2. Purge Abandoned Instances
	if flags.All || flags.Instances {
		instancesDir := filepath.Join(GetRuntimeDir(), "instances")
		if size, err := getDirSize(instancesDir); err == nil && size > 0 {
			if err := removeAllSafe(instancesDir); err == nil {
				_ = os.MkdirAll(instancesDir, 0755)
				fixSudoOwnership(instancesDir)
				fmt.Printf("✓ Purged ephemeral instance layers [%s]\n", formatBytes(size))
				totalReclaimed += size
			} else {
				fmt.Printf("⚠️ Failed to purge instances: %v\n", err)
			}
		} else {
			fmt.Println("• No abandoned instance layers found.")
		}
	}

	// 3. Purge Historical Crash Dumps
	if flags.All || flags.Crashes {
		crashesDir := filepath.Join(GetRuntimeDir(), "crashes")
		if size, err := getDirSize(crashesDir); err == nil && size > 0 {
			if err := removeAllSafe(crashesDir); err == nil {
				_ = os.MkdirAll(crashesDir, 0755)
				fixSudoOwnership(crashesDir)
				fmt.Printf("✓ Purged historical crash reports [%s]\n", formatBytes(size))
				totalReclaimed += size
			} else {
				fmt.Printf("⚠️ Failed to purge crashes: %v\n", err)
			}
		} else {
			fmt.Println("• No crash reports found.")
		}
	}

	// 4. Clean legacy test rootfs if present
	legacy := "/tmp/silo-container"
	if size, err := getDirSize(legacy); err == nil && size > 0 {
		_ = removeAllSafe(legacy)
		fmt.Printf("✓ Cleaned legacy test rootfs [%s]\n", formatBytes(size))
		totalReclaimed += size
	}

	fmt.Printf("\nTotal disk space reclaimed: %s\n", formatBytes(totalReclaimed))
	return 0
}
