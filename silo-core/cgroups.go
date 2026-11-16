package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	memoryLimitBytes = "52428800" // 50 MB
	pidsLimit        = "50"       // Max 50 processes
)

// ensureSubtreeControllers ensures root cgroup exports memory and pids to children
func ensureSubtreeControllers() {
	subtreePath := "/sys/fs/cgroup/cgroup.subtree_control"
	data, err := os.ReadFile(subtreePath)
	if err != nil {
		return
	}
	content := string(data)
	var toEnable []string
	if !strings.Contains(content, "memory") {
		toEnable = append(toEnable, "+memory")
	}
	if !strings.Contains(content, "pids") {
		toEnable = append(toEnable, "+pids")
	}
	if len(toEnable) > 0 {
		_ = os.WriteFile(subtreePath, []byte(strings.Join(toEnable, " ")), 0644)
	}
}

func setupCgroup(pid int) (func(), error) {
	ensureSubtreeControllers()

	cgroupDir := fmt.Sprintf("/sys/fs/cgroup/silo-%d", pid)

	// 1. Create per-container cgroup directory
	if err := os.MkdirAll(cgroupDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cgroup directory: %w", err)
	}

	cleanup := func() {
		// Kill all remaining processes in the cgroup (Linux 5.14+)
		_ = os.WriteFile(filepath.Join(cgroupDir, "cgroup.kill"), []byte("1"), 0644)
		// Small breather to let kernel reap
		time.Sleep(10 * time.Millisecond)
		_ = os.Remove(cgroupDir)
	}

	// 2. Set memory limit (50 MB)
	if err := os.WriteFile(filepath.Join(cgroupDir, "memory.max"), []byte(memoryLimitBytes), 0644); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to set memory.max: %w", err)
	}

	// Disable swap escape (memory.swap.max = 0)
	_ = os.WriteFile(filepath.Join(cgroupDir, "memory.swap.max"), []byte("0"), 0644)

	// 3. Set pids limit (50)
	_ = os.WriteFile(filepath.Join(cgroupDir, "pids.max"), []byte(pidsLimit), 0644)

	// 4. Attach container PID to cgroup.procs
	procsPath := filepath.Join(cgroupDir, "cgroup.procs")
	if err := os.WriteFile(procsPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to attach pid to cgroup.procs: %w", err)
	}

	return cleanup, nil
}

// inspectCgroup analyzes cgroup events and prints a diagnostic post-mortem before teardown
func inspectCgroup(pid int) {
	cgroupDir := fmt.Sprintf("/sys/fs/cgroup/silo-%d", pid)

	// 1. Check memory events (OOM)
	eventsData, err := os.ReadFile(filepath.Join(cgroupDir, "memory.events"))
	if err != nil {
		return
	}

	peakData, _ := os.ReadFile(filepath.Join(cgroupDir, "memory.peak"))
	peakStr := strings.TrimSpace(string(peakData))
	if peakStr == "" {
		peakStr = "N/A"
	}

	events := make(map[string]string)
	for _, line := range strings.Split(string(eventsData), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			events[parts[0]] = parts[1]
		}
	}

	oomKills := events["oom_kill"]
	if oomKills != "" && oomKills != "0" {
		fmt.Println("\n================== SILO CATASTROPHE POST-MORTEM ==================")
		fmt.Println("CRITICAL: Container was terminated by the Linux OOM Killer!")
		fmt.Printf("• Failure Cause : Memory ceiling exceeded (%s bytes / ~50 MB)\n", memoryLimitBytes)
		fmt.Printf("• Peak Memory   : %s bytes\n", peakStr)
		fmt.Printf("• OOM Kills     : %s\n", oomKills)
		if maxHits, ok := events["max"]; ok {
			fmt.Printf("• Limit Hit Max : %s times\n", maxHits)
		}
		fmt.Println("==================================================================")
		return
	}

	// 2. Check PIDs events (Fork bomb protection)
	pidsData, err := os.ReadFile(filepath.Join(cgroupDir, "pids.events"))
	if err == nil {
		pidsEvents := make(map[string]string)
		for _, line := range strings.Split(string(pidsData), "\n") {
			parts := strings.Fields(line)
			if len(parts) == 2 {
				pidsEvents[parts[0]] = parts[1]
			}
		}
		if maxHits, ok := pidsEvents["max"]; ok && maxHits != "0" {
			fmt.Println("\n================== SILO CATASTROPHE POST-MORTEM ==================")
			fmt.Println("WARNING: Process creation limit was breached (Fork Bomb Prevention)!")
			fmt.Printf("• Process Limit : %s max processes\n", pidsLimit)
			fmt.Printf("• Breached Count: %s times fork() failed\n", maxHits)
			fmt.Println("==================================================================")
		}
	}
}
