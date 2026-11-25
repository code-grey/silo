package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ImageSpec defines the metadata and provisioner for a container image
type ImageSpec struct {
	Name        string
	Description string
	URLTemplate string // For standalone rootfs tarballs (like Alpine or Ubuntu/Debian base)
	Version     string
	ApkPackage  string // For language runtimes synthesized on top of Alpine
}

// ImageCatalog defines the curated list of supported official runtime environments
var ImageCatalog = map[string]ImageSpec{
	"alpine": {
		Name:        "alpine",
		Description: "Ultra-compact Alpine Linux minirootfs (musl libc, ~3.3MB)",
		URLTemplate: "https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/%s/alpine-minirootfs-3.19.0-%s.tar.gz",
		Version:     "3.19.0",
	},
	"debian": {
		Name:        "debian",
		Description: "Official minimal glibc Linux rootfs (Ubuntu/Debian base ~28MB)",
		URLTemplate: "http://cdimage.ubuntu.com/ubuntu-base/releases/22.04/release/ubuntu-base-22.04.4-base-%s.tar.gz",
		Version:     "22.04",
	},
	"ubuntu": {
		Name:        "ubuntu",
		Description: "Official Ubuntu 22.04 LTS minimal rootfs (glibc)",
		URLTemplate: "http://cdimage.ubuntu.com/ubuntu-base/releases/22.04/release/ubuntu-base-22.04.4-base-%s.tar.gz",
		Version:     "22.04",
	},
	"python": {
		Name:        "python",
		Description: "Alpine Linux with Python 3 runtime pre-installed",
		ApkPackage:  "python3",
	},
	"node": {
		Name:        "node",
		Description: "Alpine Linux with Node.js runtime pre-installed",
		ApkPackage:  "nodejs",
	},
	"ruby": {
		Name:        "ruby",
		Description: "Alpine Linux with Ruby runtime pre-installed",
		ApkPackage:  "ruby",
	},
	"perl": {
		Name:        "perl",
		Description: "Alpine Linux with Perl runtime pre-installed",
		ApkPackage:  "perl",
	},
	"php": {
		Name:        "php",
		Description: "Alpine Linux with PHP runtime pre-installed",
		ApkPackage:  "php82",
	},
	"java": {
		Name:        "java",
		Description: "Alpine Linux with OpenJDK 17 runtime pre-installed",
		ApkPackage:  "openjdk17-jre-headless",
	},
}

// GetImagesDir resolves the persistent image cache directory on the host.
// It prioritizes the invoking non-root user's home (via SUDO_USER) to ensure rootless compatibility.
func GetImagesDir() string {
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil && u.HomeDir != "" {
			return filepath.Join(u.HomeDir, ".silo", "images")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".silo", "images")
	}
	return "/tmp/silo/images"
}

// isRootfsValid checks if the given directory contains a runnable shell binary
func isRootfsValid(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "bin", "sh")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "bash")); err == nil {
		return true
	}
	return false
}

// EnsureImage checks the local cache for the requested image, automatically pulling or synthesizing it if missing.
func EnsureImage(imageName string) (string, error) {
	imagesBase := GetImagesDir()
	targetRootfs := filepath.Join(imagesBase, imageName, "rootfs")

	// Tier 1: Check Local Disk Cache (Sub-millisecond fast path)
	if isRootfsValid(targetRootfs) {
		Thought("Image", "Found cached image '%s' at %s", imageName, targetRootfs)
		return targetRootfs, nil
	}

	Thought("Image", "Image '%s' not found locally in %s. Resolving...", imageName, imagesBase)

	spec, exists := ImageCatalog[imageName]
	if !exists {
		// Unknown image name: fallback to alpine
		Thought("Image", "⚠️ Unknown image '%s'. Defaulting to Alpine base.", imageName)
		imageName = "alpine"
		spec = ImageCatalog["alpine"]
		targetRootfs = filepath.Join(imagesBase, "alpine", "rootfs")
		if isRootfsValid(targetRootfs) {
			return targetRootfs, nil
		}
	}

	// Standalone rootfs download (Alpine or Debian/Ubuntu glibc)
	if spec.URLTemplate != "" {
		Thought("Image", "Downloading %s from official mirror...", spec.Description)
		if err := downloadAndExtractRootfs(spec, targetRootfs); err != nil {
			Thought("Image", "⚠️ Download failed: %v", err)
			return fallbackToLegacyRootfs(fmt.Errorf("failed to provision image '%s': %w", imageName, err))
		}
		Thought("Image", "Cached image '%s' successfully at %s", imageName, targetRootfs)
		return targetRootfs, nil
	}

	// Tier 2: Base Alpine Image Download (Prerequisite for dynamic synthesis)
	alpineRootfs := filepath.Join(imagesBase, "alpine", "rootfs")
	if !isRootfsValid(alpineRootfs) {
		Thought("Image", "Downloading Alpine v3.19.0 minirootfs from official mirror...")
		if err := downloadAndExtractRootfs(ImageCatalog["alpine"], alpineRootfs); err != nil {
			Thought("Image", "⚠️ Base Alpine download failed: %v", err)
			return fallbackToLegacyRootfs(fmt.Errorf("failed to download base Alpine image: %w", err))
		}
		Thought("Image", "Cached base Alpine image successfully at %s", alpineRootfs)
	}

	// Tier 3: Dynamic Language Image Synthesis (apk snapshot)
	if spec.ApkPackage != "" {
		Thought("Image", "Synthesizing '%s' image on-demand by installing '%s'...", imageName, spec.ApkPackage)
		if err := synthesizeLanguageImage(alpineRootfs, targetRootfs, spec.ApkPackage); err != nil {
			Thought("Image", "⚠️ Dynamic synthesis failed (%v). Falling back to base Alpine.", err)
			return alpineRootfs, nil
		}
		Thought("Image", "Successfully synthesized and cached '%s' at %s", imageName, targetRootfs)
		return targetRootfs, nil
	}

	return alpineRootfs, nil
}

// fallbackToLegacyRootfs checks for a previously prepared rootfs if network or download fails
func fallbackToLegacyRootfs(cause error) (string, error) {
	legacy := "/tmp/silo-container/rootfs"
	if isRootfsValid(legacy) {
		Thought("Image", "⚠️ Operating offline: using existing local rootfs at %s", legacy)
		return legacy, nil
	}
	if cause != nil {
		return "", fmt.Errorf("no cached image and %w", cause)
	}
	return "", fmt.Errorf("no cached image and network unreachable")
}

// downloadAndExtractRootfs streams the tarball directly to disk with zero temp files and retries
func downloadAndExtractRootfs(spec ImageSpec, targetDir string) error {
	arch := runtime.GOARCH
	var url string

	if spec.Name == "alpine" {
		alpineArch := arch
		switch arch {
		case "amd64":
			alpineArch = "x86_64"
		case "arm64":
			alpineArch = "aarch64"
		}
		url = fmt.Sprintf(spec.URLTemplate, alpineArch, alpineArch)
	} else {
		// Ubuntu/Debian base naming convention
		url = fmt.Sprintf(spec.URLTemplate, arch)
	}

	// Optimized, non-blocking HTTP client with connection pooling and generous handshake timeouts
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			DisableKeepAlives:     false,
			MaxIdleConns:          10,
		},
		Timeout: 90 * time.Second,
	}

	var resp *http.Response
	var err error

	// Retry loop for transient handshake/TLS hiccups
	for attempt := 1; attempt <= 2; attempt++ {
		resp, err = client.Get(url)
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		if attempt < 2 {
			time.Sleep(1 * time.Second)
		}
	}

	if err != nil {
		return fmt.Errorf("HTTP request failed after retries: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("mirror returned HTTP %d for %s", resp.StatusCode, url)
	}

	// Decompress gzip on the fly without intermediate disk writing
	gzReader, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip decompression failed: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return err
	}

	// Reusable 64KB buffer for high-throughput disk writes
	copyBuf := make([]byte, 64*1024)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}

		targetPath := filepath.Join(targetDir, header.Name)

		// Prevent Zip-Slip path traversal vulnerabilities
		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(targetDir)) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(targetPath, 0755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(targetPath), 0755)
			outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, header.FileInfo().Mode())
			if err != nil {
				continue
			}
			_, _ = io.CopyBuffer(outFile, tarReader, copyBuf)
			outFile.Close()
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(targetPath), 0755)
			_ = os.Remove(targetPath)
			_ = os.Symlink(header.Linkname, targetPath)
		case tar.TypeLink:
			_ = os.MkdirAll(filepath.Dir(targetPath), 0755)
			_ = os.Remove(targetPath)
			oldPath := filepath.Join(targetDir, header.Linkname)
			_ = os.Link(oldPath, targetPath)
		}
	}

	// If executed under sudo, restore ownership of the downloaded image directory to SUDO_USER
	if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && os.Geteuid() == 0 {
		if u, err := user.Lookup(sudoUser); err == nil {
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			_ = filepath.Walk(filepath.Dir(targetDir), func(p string, info os.FileInfo, err error) error {
				if err == nil {
					_ = os.Chown(p, uid, gid)
				}
				return nil
			})
		}
	}

	return nil
}

// synthesizeLanguageImage clones base Alpine into an atomic staging directory and installs apkPkg
// inside an unprivileged user/mount namespace so language synthesis works 100% rootless.
func synthesizeLanguageImage(baseRootfs, targetRootfs, apkPkg string) error {
	stagingRootfs := targetRootfs + ".tmp"
	_ = os.RemoveAll(stagingRootfs)
	_ = os.RemoveAll(targetRootfs)

	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(stagingRootfs)
		}
	}()

	if err := os.MkdirAll(stagingRootfs, 0755); err != nil {
		return err
	}

	// 1. Copy base rootfs (fast file copy)
	copyCmd := exec.Command("cp", "-a", baseRootfs+"/.", stagingRootfs)
	if err := copyCmd.Run(); err != nil {
		return fmt.Errorf("failed to copy base rootfs: %w", err)
	}

	// 2. Inject host DNS configuration so apk can resolve package repositories
	resolvDst := filepath.Join(stagingRootfs, "etc", "resolv.conf")
	resolvSrc, err := os.ReadFile("/run/systemd/resolve/resolv.conf")
	if err != nil {
		resolvSrc, err = os.ReadFile("/etc/resolv.conf")
	}
	if err == nil {
		_ = os.WriteFile(resolvDst, resolvSrc, 0644)
	}

	// 3. Run apk add inside our own rootless CLONE_NEWUSER | CLONE_NEWNS child (__synth)
	apkCmd := exec.Command("/proc/self/exe", "__synth", stagingRootfs, apkPkg)
	apkCmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWNS,
	}
	if os.Geteuid() != 0 {
		apkCmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWUSER
		apkCmd.SysProcAttr.UidMappings = []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		}
		apkCmd.SysProcAttr.GidMappings = []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		}
		apkCmd.SysProcAttr.GidMappingsEnableSetgroups = false
	}

	if out, err := apkCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apk add %s failed: %v (%s)", apkPkg, err, string(out))
	}

	if err := os.Rename(stagingRootfs, targetRootfs); err != nil {
		return fmt.Errorf("failed to finalize synthesized rootfs: %w", err)
	}
	success = true
	return nil
}

// runSynthChild executes inside CLONE_NEWUSER | CLONE_NEWNS to chroot into stagingRootfs and run apk add.
func runSynthChild(targetRootfs, apkPkg string) int {
	_ = syscall.Mount("", "/", "", syscall.MS_PRIVATE|syscall.MS_REC, "")
	if err := setupDev(targetRootfs); err != nil {
		fmt.Fprintf(os.Stderr, "synth setup /dev failed: %v\n", err)
		return 1
	}
	if err := syscall.Chroot(targetRootfs); err != nil {
		fmt.Fprintf(os.Stderr, "synth chroot failed: %v\n", err)
		return 1
	}
	if err := syscall.Chdir("/"); err != nil {
		fmt.Fprintf(os.Stderr, "synth chdir failed: %v\n", err)
		return 1
	}

	apkArgs := []string{"/sbin/apk", "add", "--no-cache", "--no-chown", apkPkg}
	if err := syscall.Exec("/sbin/apk", apkArgs, []string{"PATH=/bin:/sbin:/usr/bin:/usr/sbin"}); err != nil {
		fmt.Fprintf(os.Stderr, "synth exec apk failed: %v\n", err)
		return 1
	}
	return 0
}
