package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// WrenchStatus represents the outcome of a single host-readiness diagnostic check.
type WrenchStatus string

const (
	StatusPass WrenchStatus = "PASS"
	StatusWarn WrenchStatus = "WARN"
	StatusFail WrenchStatus = "FAIL"
)

// WrenchCheck holds the result and remediation advice for one kernel or host check.
type WrenchCheck struct {
	Name   string
	Status WrenchStatus
	Detail string
	Hint   string
}

// WrenchReport aggregates all host readiness findings and required remediation flags.
type WrenchReport struct {
	ExePath           string
	UID               int
	GID               int
	AppArmorLabel     string
	Checks            []WrenchCheck
	NeedsSysctlUserns bool
	NeedsMaxUserns    bool
	NeedsAppArmor     bool
	NeedsDirOwnership []string
}

// HasFailures reports whether any critical check failed.
func (r *WrenchReport) HasFailures() bool {
	for _, c := range r.Checks {
		if c.Status == StatusFail {
			return true
		}
	}
	return false
}

// resolveSiloExecutable returns the canonical filesystem path of the running silo binary.
func resolveSiloExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// readTrimmedFile reads a small procfs/sysfs file and returns its trimmed string content.
func readTrimmedFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// probeUserNamespace attempts a real 1ms CLONE_NEWUSER + UID/GID mapping spawn on /proc/self/exe.
// This verifies with the live kernel whether sysctl and AppArmor allow rootless execution for this exact binary.
func probeUserNamespace(exePath string) error {
	cmd := exec.Command(exePath, "__probe-userns")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
	}
	return cmd.Run()
}

// parseKernelVersion extracts major and minor version numbers from /proc/sys/kernel/osrelease.
func parseKernelVersion() (int, int, string) {
	rel, err := readTrimmedFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return 0, 0, "unknown"
	}
	core := strings.SplitN(rel, "-", 2)[0]
	parts := strings.Split(core, ".")
	if len(parts) < 2 {
		return 0, 0, rel
	}
	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	return major, minor, rel
}

// detectAppArmorABI returns the highest available AppArmor ABI directive on the host.
func detectAppArmorABI() string {
	if _, err := os.Stat("/etc/apparmor.d/abi/4.0"); err == nil {
		return "abi <abi/4.0>,"
	}
	if _, err := os.Stat("/etc/apparmor.d/abi/3.0"); err == nil {
		return "abi <abi/3.0>,"
	}
	return ""
}

// buildAppArmorProfile renders a least-privilege AppArmor profile scoped to exePath.
func buildAppArmorProfile(exePath string) string {
	var lines []string
	lines = append(lines, "# Silo rootless container runtime userns profile")
	if abi := detectAppArmorABI(); abi != "" {
		lines = append(lines, abi)
	}
	lines = append(lines,
		"include <tunables/global>",
		"",
		fmt.Sprintf("profile silo %q flags=(unconfined) {", exePath),
		"  userns,",
		"}",
		"",
	)
	return strings.Join(lines, "\n")
}

// runWrenchDiagnose inspects kernel sysctls, AppArmor, user namespaces, Cgroups v2, and directory ownership.
func runWrenchDiagnose() *WrenchReport {
	exePath := resolveSiloExecutable()
	aaLabel, err := readTrimmedFile("/proc/self/attr/current")
	if err != nil {
		aaLabel = "unavailable"
	}

	report := &WrenchReport{
		ExePath:       exePath,
		UID:           os.Getuid(),
		GID:           os.Getgid(),
		AppArmorLabel: aaLabel,
	}

	// 1. Kernel & Unprivileged OverlayFS readiness (>= 5.11)
	major, minor, rel := parseKernelVersion()
	if major > 5 || (major == 5 && minor >= 11) {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Kernel / OverlayFS",
			Status: StatusPass,
			Detail: fmt.Sprintf("Linux %s (>= 5.11 supports rootless OverlayFS)", rel),
		})
	} else {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Kernel / OverlayFS",
			Status: StatusWarn,
			Detail: fmt.Sprintf("Linux %s (< 5.11 may block unprivileged overlay mounts)", rel),
			Hint:   "Upgrade to Linux >= 5.11 or run with sudo on legacy kernels.",
		})
	}

	// 2. Sysctl: kernel.unprivileged_userns_clone (Debian/Ubuntu/hardened kernels)
	sysctlBlocked := false
	if val, err := readTrimmedFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if val == "0" {
			sysctlBlocked = true
			report.NeedsSysctlUserns = true
			report.Checks = append(report.Checks, WrenchCheck{
				Name:   "Sysctl userns_clone",
				Status: StatusFail,
				Detail: "kernel.unprivileged_userns_clone = 0 (unprivileged CLONE_NEWUSER disabled)",
				Hint:   "Enable via /etc/sysctl.d/99-silo-userns.conf: kernel.unprivileged_userns_clone = 1",
			})
		} else {
			report.Checks = append(report.Checks, WrenchCheck{
				Name:   "Sysctl userns_clone",
				Status: StatusPass,
				Detail: fmt.Sprintf("kernel.unprivileged_userns_clone = %s", val),
			})
		}
	} else {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Sysctl userns_clone",
			Status: StatusPass,
			Detail: "not restricted by kernel.unprivileged_userns_clone",
		})
	}

	// 3. Sysctl: user.max_user_namespaces
	if val, err := readTrimmedFile("/proc/sys/user/max_user_namespaces"); err == nil {
		if n, convErr := strconv.Atoi(val); convErr == nil && n <= 0 {
			sysctlBlocked = true
			report.NeedsMaxUserns = true
			report.Checks = append(report.Checks, WrenchCheck{
				Name:   "Sysctl max_userns",
				Status: StatusFail,
				Detail: fmt.Sprintf("user.max_user_namespaces = %s (exhausted/disabled)", val),
				Hint:   "Increase via sysctl: user.max_user_namespaces = 15000",
			})
		} else {
			report.Checks = append(report.Checks, WrenchCheck{
				Name:   "Sysctl max_userns",
				Status: StatusPass,
				Detail: fmt.Sprintf("user.max_user_namespaces = %s", val),
			})
		}
	}

	// 4. AppArmor userns restriction + Live Kernel Probe
	aaRestrict, aaErr := readTrimmedFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
	aaActive := aaErr == nil && aaRestrict == "1"

	if os.Geteuid() == 0 {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "UserNS Probe",
			Status: StatusPass,
			Detail: "running as root (euid=0); user namespaces bypassed",
		})
	} else {
		probeErr := probeUserNamespace(exePath)
		if probeErr == nil {
			report.Checks = append(report.Checks, WrenchCheck{
				Name:   "UserNS Probe",
				Status: StatusPass,
				Detail: fmt.Sprintf("CLONE_NEWUSER succeeded for %s (attr: %s)", exePath, aaLabel),
			})
			if aaActive {
				report.Checks = append(report.Checks, WrenchCheck{
					Name:   "AppArmor Policy",
					Status: StatusPass,
					Detail: "apparmor_restrict_unprivileged_userns = 1, profile allows silo",
				})
			}
		} else {
			report.Checks = append(report.Checks, WrenchCheck{
				Name:   "UserNS Probe",
				Status: StatusFail,
				Detail: fmt.Sprintf("CLONE_NEWUSER blocked for %s: %v", exePath, probeErr),
				Hint:   "Run 'silo wrench fix' to configure rootless permissions automatically.",
			})

			if aaActive && !sysctlBlocked {
				report.NeedsAppArmor = true
				report.Checks = append(report.Checks, WrenchCheck{
					Name:   "AppArmor Policy",
					Status: StatusFail,
					Detail: fmt.Sprintf("apparmor_restrict_unprivileged_userns = 1 and %s is not permitted (attr: %s)", exePath, aaLabel),
					Hint:   fmt.Sprintf("Install AppArmor profile at /etc/apparmor.d/silo for %q", exePath),
				})
			} else if aaActive {
				// Sysctl blocked first; check if /etc/apparmor.d/silo already covers this binary path.
				profileBytes, readErr := os.ReadFile("/etc/apparmor.d/silo")
				if readErr != nil || !strings.Contains(string(profileBytes), exePath) {
					report.NeedsAppArmor = true
					report.Checks = append(report.Checks, WrenchCheck{
						Name:   "AppArmor Policy",
						Status: StatusFail,
						Detail: fmt.Sprintf("apparmor_restrict_unprivileged_userns = 1 and /etc/apparmor.d/silo does not match %s", exePath),
						Hint:   fmt.Sprintf("Install AppArmor profile at /etc/apparmor.d/silo for %q", exePath),
					})
				} else {
					report.Checks = append(report.Checks, WrenchCheck{
						Name:   "AppArmor Policy",
						Status: StatusPass,
						Detail: "/etc/apparmor.d/silo covers binary path (pending sysctl fix)",
					})
				}
			}
		}
	}

	// 5. Cgroups v2 & Rootless Delegation
	checkCgroupReadiness(report)

	// 6. Storage & Runtime Directory Ownership
	checkStorageOwnership(report)

	return report
}

// checkCgroupReadiness verifies unified Cgroups v2 and user slice controller delegation.
func checkCgroupReadiness(report *WrenchReport) {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Cgroups v2",
			Status: StatusFail,
			Detail: "unified Cgroups v2 hierarchy not mounted at /sys/fs/cgroup",
			Hint:   "Boot with systemd unified cgroup hierarchy enabled.",
		})
		return
	}

	if os.Geteuid() == 0 {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Cgroups v2",
			Status: StatusPass,
			Detail: "unified /sys/fs/cgroup active (root mode)",
		})
		return
	}

	userSlice := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service", os.Getuid(), os.Getuid())
	ctrlData, err := readTrimmedFile(filepath.Join(userSlice, "cgroup.controllers"))
	if err != nil {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Cgroups v2",
			Status: StatusFail,
			Detail: fmt.Sprintf("delegated slice %s unavailable (%v)", userSlice, err),
			Hint:   "Ensure systemd user session (user@.service) is active with Delegate=yes.",
		})
		return
	}

	hasMem := strings.Contains(ctrlData, "memory")
	hasPids := strings.Contains(ctrlData, "pids")
	if !hasMem || !hasPids {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Cgroups v2",
			Status: StatusWarn,
			Detail: fmt.Sprintf("slice controllers '%s' missing memory/pids delegation", ctrlData),
			Hint:   "Configure /etc/systemd/system/user@.service.d/delegate.conf with Delegate=cpu cpuset io memory pids",
		})
		return
	}

	if _, err := exec.LookPath("systemd-run"); err != nil {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Cgroups v2",
			Status: StatusWarn,
			Detail: "systemd-run not found in PATH; session scope migration unavailable",
			Hint:   "Install systemd or launch silo inside user@.service scope.",
		})
		return
	}

	report.Checks = append(report.Checks, WrenchCheck{
		Name:   "Cgroups v2",
		Status: StatusPass,
		Detail: fmt.Sprintf("delegated slice ready (%s) with controllers [%s]", userSlice, ctrlData),
	})
}

// checkStorageOwnership ensures ~/.silo and /tmp/silo-<uid> are writable by the current user.
func checkStorageOwnership(report *WrenchReport) {
	if os.Geteuid() == 0 {
		return
	}

	imagesDir := GetImagesDir()
	siloHome := filepath.Dir(imagesDir)
	candidates := []string{siloHome, imagesDir, GetRuntimeDir()}

	var bad []string
	for _, dir := range candidates {
		info, err := os.Stat(dir)
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		if int(stat.Uid) != os.Getuid() {
			bad = append(bad, dir)
		}
	}

	if len(bad) > 0 {
		report.NeedsDirOwnership = bad
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Storage Ownership",
			Status: StatusFail,
			Detail: fmt.Sprintf("owned by another UID (likely root from past sudo run): %s", strings.Join(bad, ", ")),
			Hint:   fmt.Sprintf("Fix ownership with: sudo chown -R %d:%d %s", os.Getuid(), os.Getgid(), strings.Join(bad, " ")),
		})
	} else {
		report.Checks = append(report.Checks, WrenchCheck{
			Name:   "Storage Ownership",
			Status: StatusPass,
			Detail: fmt.Sprintf("runtime (%s) and image cache (%s) writable", GetRuntimeDir(), imagesDir),
		})
	}
}

// printWrenchReport renders a clear diagnostic table to stderr/stdout.
func printWrenchReport(w io.Writer, report *WrenchReport) {
	fmt.Fprintf(w, "[silo:wrench] 🔧 Host Rootless Readiness Diagnostics\n")
	fmt.Fprintf(w, "  Binary : %s (uid=%d, gid=%d)\n", report.ExePath, report.UID, report.GID)
	fmt.Fprintf(w, "  Profile: %s\n\n", report.AppArmorLabel)

	for _, c := range report.Checks {
		icon := "✓"
		switch c.Status {
		case StatusWarn:
			icon = "!"
		case StatusFail:
			icon = "✗"
		}
		fmt.Fprintf(w, "  [%s] %-18s %s\n", icon, c.Name, c.Detail)
		if c.Hint != "" {
			fmt.Fprintf(w, "      ↳ Fix: %s\n", c.Hint)
		}
	}
	fmt.Fprintln(w)
}

// isInteractiveTTY reports whether stdin and stderr are attached to a live terminal.
func isInteractiveTTY() bool {
	inInfo, err1 := os.Stdin.Stat()
	errInfo, err2 := os.Stderr.Stat()
	if err1 != nil || err2 != nil {
		return false
	}
	return (inInfo.Mode()&os.ModeCharDevice) != 0 && (errInfo.Mode()&os.ModeCharDevice) != 0
}

// promptConfirm asks the user a yes/no question on an interactive terminal.
func promptConfirm(question string) bool {
	if !isInteractiveTTY() {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", question)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

// applyWrenchFixes previews and applies required sysctl, AppArmor, and ownership fixes via sudo.
func applyWrenchFixes(report *WrenchReport, autoYes bool) error {
	var scriptSteps []string
	var summary []string

	if report.NeedsSysctlUserns || report.NeedsMaxUserns {
		var sysctlLines []string
		sysctlLines = append(sysctlLines, "# Added by silo wrench to permit rootless container user namespaces")
		if report.NeedsSysctlUserns {
			sysctlLines = append(sysctlLines, "kernel.unprivileged_userns_clone = 1")
		}
		if report.NeedsMaxUserns {
			sysctlLines = append(sysctlLines, "user.max_user_namespaces = 15000")
		}
		sysctlContent := strings.Join(sysctlLines, "\n") + "\n"
		scriptSteps = append(scriptSteps,
			fmt.Sprintf("cat <<'EOF' > /etc/sysctl.d/99-silo-userns.conf\n%sEOF", sysctlContent),
			"sysctl -p /etc/sysctl.d/99-silo-userns.conf >/dev/null",
		)
		summary = append(summary, "Write /etc/sysctl.d/99-silo-userns.conf and apply via sysctl")
	}

	if report.NeedsAppArmor {
		profile := buildAppArmorProfile(report.ExePath)
		scriptSteps = append(scriptSteps,
			fmt.Sprintf("cat <<'EOF' > /etc/apparmor.d/silo\n%sEOF", profile),
			"apparmor_parser -r /etc/apparmor.d/silo",
		)
		summary = append(summary, fmt.Sprintf("Install AppArmor userns profile at /etc/apparmor.d/silo for %s", report.ExePath))
	}

	if len(report.NeedsDirOwnership) > 0 {
		quoted := make([]string, 0, len(report.NeedsDirOwnership))
		for _, d := range report.NeedsDirOwnership {
			quoted = append(quoted, fmt.Sprintf("%q", d))
		}
		scriptSteps = append(scriptSteps,
			fmt.Sprintf("chown -R %d:%d %s", report.UID, report.GID, strings.Join(quoted, " ")),
		)
		summary = append(summary, fmt.Sprintf("Restore ownership of %s to %d:%d", strings.Join(report.NeedsDirOwnership, ", "), report.UID, report.GID))
	}

	if len(scriptSteps) == 0 {
		fmt.Fprintln(os.Stderr, "[silo:wrench] Nothing to auto-fix automatically.")
		return nil
	}

	fmt.Fprintln(os.Stderr, "[silo:wrench] Planned one-time privileged setup steps:")
	for i, s := range summary {
		fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, s)
	}
	fmt.Fprintln(os.Stderr)

	if !autoYes {
		if !promptConfirm("[silo:wrench] Apply these changes now via sudo?") {
			return fmt.Errorf("aborted by user")
		}
	}

	fullScript := "set -e\n" + strings.Join(scriptSteps, "\n")
	var cmd *exec.Cmd
	if os.Geteuid() == 0 {
		cmd = exec.Command("sh", "-c", fullScript)
	} else {
		cmd = exec.Command("sudo", "sh", "-c", fullScript)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("privileged setup failed: %w", err)
	}

	// Re-verify live kernel probe immediately after applying fixes.
	if os.Geteuid() != 0 {
		if err := probeUserNamespace(report.ExePath); err != nil {
			return fmt.Errorf("setup applied, but CLONE_NEWUSER probe still failed: %w", err)
		}
	}

	fmt.Fprintln(os.Stderr, "[silo:wrench] ✓ Rootless container support verified and active!")
	return nil
}

// offerRootlessAutoFix diagnoses a non-root permission failure and prompts to fix it if interactive.
// Returns true if fixes were applied and verified so the caller can retry launching the container.
func offerRootlessAutoFix() bool {
	report := runWrenchDiagnose()
	fmt.Fprintln(os.Stderr)
	printWrenchReport(os.Stderr, report)

	canAutoFix := report.NeedsSysctlUserns || report.NeedsMaxUserns || report.NeedsAppArmor || len(report.NeedsDirOwnership) > 0
	if !canAutoFix {
		return false
	}

	if !isInteractiveTTY() {
		fmt.Fprintln(os.Stderr, "[silo:wrench] Non-interactive terminal detected. Run 'silo wrench fix' manually to enable rootless execution.")
		return false
	}

	if err := applyWrenchFixes(report, false); err != nil {
		fmt.Fprintf(os.Stderr, "[silo:wrench] Could not complete setup: %v\n", err)
		return false
	}
	return true
}

// runWrench executes the `silo wrench [check|fix]` subcommand.
func runWrench(args []string) int {
	mode := "check"
	autoYes := false

	for _, arg := range args {
		switch arg {
		case "check", "status":
			mode = "check"
		case "fix", "setup", "repair":
			mode = "fix"
		case "-y", "--yes":
			autoYes = true
		default:
			fmt.Fprintf(os.Stderr, "Unknown wrench argument: %s\nUsage: silo wrench [check|fix] [-y|--yes]\n", arg)
			return 1
		}
	}

	report := runWrenchDiagnose()
	printWrenchReport(os.Stdout, report)

	if mode == "check" {
		if report.HasFailures() {
			fmt.Fprintln(os.Stderr, "Run 'silo wrench fix' to apply rootless fixes automatically.")
			return 1
		}
		fmt.Println("[silo:wrench] ✓ System is ready for rootless containers.")
		return 0
	}

	// Fix mode
	canAutoFix := report.NeedsSysctlUserns || report.NeedsMaxUserns || report.NeedsAppArmor || len(report.NeedsDirOwnership) > 0
	if !canAutoFix {
		if report.HasFailures() {
			fmt.Fprintln(os.Stderr, "[silo:wrench] Remaining failures require manual system configuration (see hints above).")
			return 1
		}
		fmt.Println("[silo:wrench] ✓ Everything is already configured for rootless execution.")
		return 0
	}

	if err := applyWrenchFixes(report, autoYes); err != nil {
		fmt.Fprintf(os.Stderr, "[silo:wrench] Error: %v\n", err)
		return 1
	}
	return 0
}
