package tests

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// siloBinaryPath locates the compiled silo binary at the repository root
func siloBinaryPath(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}

	// If running from inside tests/, binary is in ..
	candidate := filepath.Join(wd, "..", "silo")
	if _, err := os.Stat(candidate); err == nil {
		abs, _ := filepath.Abs(candidate)
		return abs
	}

	// If running from repo root
	candidate = filepath.Join(wd, "silo")
	if _, err := os.Stat(candidate); err == nil {
		abs, _ := filepath.Abs(candidate)
		return abs
	}

	t.Fatalf("Could not locate compiled 'silo' binary. Please run 'go build -o silo .' first.")
	return ""
}

// runSilo executes the compiled silo binary with given arguments and captures stdout/stderr
func runSilo(t *testing.T, cwd string, args ...string) (string, string, int) {
	binary := siloBinaryPath(t)
	cmd := exec.Command(binary, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return stdout.String(), stderr.String(), exitCode
}

// =========================================================================
// TEST SUITE 1: Namespaces & Core Process Isolation
// =========================================================================

func TestE2E_Namespaces(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("E2E namespace tests require root privileges. Run with sudo.")
	}

	t.Run("UTS_Hostname_Isolation", func(t *testing.T) {
		hostHostname, _ := os.Hostname()
		stdout, stderr, code := runSilo(t, "", "run", "/bin/sh", "-c", "hostname")

		if code != 0 {
			t.Fatalf("Container failed with exit code %d. Stderr: %s", code, stderr)
		}
		if !strings.Contains(stdout, "silo-container") {
			t.Errorf("Expected container hostname 'silo-container', got: %s", stdout)
		}

		// Ensure host hostname was not modified
		currentHost, _ := os.Hostname()
		if currentHost != hostHostname {
			t.Errorf("Host hostname was corrupted! Want %s, got %s", hostHostname, currentHost)
		}
	})

	t.Run("PID_1_Process_Isolation", func(t *testing.T) {
		stdout, stderr, code := runSilo(t, "", "run", "/bin/sh", "-c", "echo $$")
		if code != 0 {
			t.Fatalf("Container failed with exit code %d. Stderr: %s", code, stderr)
		}
		trimmed := strings.TrimSpace(stdout)
		if trimmed != "1" {
			t.Errorf("Expected payload to run as PID 1, got PID '%s'", trimmed)
		}
	})

	t.Run("ProcFS_Isolation", func(t *testing.T) {
		stdout, stderr, code := runSilo(t, "", "run", "/bin/sh", "-c", "ls -d /proc/[0-9]*")
		if code != 0 {
			t.Fatalf("Container failed with exit code %d. Stderr: %s", code, stderr)
		}
		// In an isolated PID namespace, only PID 1 (and the transient subshell/ls) should exist
		lines := strings.Split(strings.TrimSpace(stdout), "\n")
		if len(lines) > 5 {
			t.Errorf("Leaked host /proc! Container saw %d processes: %v", len(lines), lines)
		}
	})
}

// =========================================================================
// TEST SUITE 2: Cgroups v2 Resource Enforcement & Crash Post-Mortems
// =========================================================================

func TestE2E_Cgroups(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("E2E Cgroups tests require root privileges. Run with sudo.")
	}

	t.Run("Memory_Limit_OOM_Kill_And_Crash_Dump", func(t *testing.T) {
		crashDir := "/tmp/silo/crashes"
		_ = os.RemoveAll(crashDir)

		// OOM allocation loop in shell
		_, _, code := runSilo(t, "", "run", "/bin/sh", "-c", "x=a; while true; do x=$x$x; done")
		if code == 0 {
			t.Errorf("Expected OOM failure, but container exited with code 0")
		}

		// Verify structured crash dump was written
		entries, err := os.ReadDir(crashDir)
		if err != nil || len(entries) == 0 {
			t.Fatalf("Expected crash dump in %s, but none found", crashDir)
		}

		crashFile := filepath.Join(crashDir, entries[0].Name())
		content, err := os.ReadFile(crashFile)
		if err != nil {
			t.Fatalf("Failed to read crash dump: %v", err)
		}

		var report map[string]interface{}
		if err := json.Unmarshal(content, &report); err != nil {
			t.Fatalf("Invalid JSON crash report: %v", err)
		}

		var oomKills float64
		switch v := report["oom_kills"].(type) {
		case float64:
			oomKills = v
		case int:
			oomKills = float64(v)
		case string:
			fmt.Sscanf(v, "%f", &oomKills)
		}

		if oomKills < 1 {
			t.Errorf("Expected crash report oom_kills >= 1, got %v", report["oom_kills"])
		}
	})

	t.Run("PID_Ceiling_Enforcement", func(t *testing.T) {
		// Fork bomb attempts to exceed pids.max = 50
		_, _, code := runSilo(t, "", "run", "/bin/sh", "-c", ":(){ :|:& };:")
		if code == 0 {
			t.Errorf("Expected fork bomb to trigger failure, but exited with 0")
		}
	})
}

// =========================================================================
// TEST SUITE 3: Pre-Flight & Multi-Language Auto-Wrapping
// =========================================================================

func TestE2E_PreFlight_Languages(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("E2E language tests require root privileges. Run with sudo.")
	}

	t.Run("Shebangless_Python_Auto_Runner", func(t *testing.T) {
		tempDir := t.TempDir()
		script := filepath.Join(tempDir, "test.py")
		_ = os.WriteFile(script, []byte("import sys\nprint('PYTHON_OK', sys.argv[1])\n"), 0755)

		stdout, stderr, code := runSilo(t, "", "run", script, "arg_test")
		if code != 0 {
			t.Fatalf("Python execution failed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "PYTHON_OK arg_test") {
			t.Errorf("Expected 'PYTHON_OK arg_test', got: %s", stdout)
		}
	})

	t.Run("Shebangless_Node_Auto_Runner", func(t *testing.T) {
		tempDir := t.TempDir()
		script := filepath.Join(tempDir, "test.js")
		_ = os.WriteFile(script, []byte("console.log('NODE_OK', process.argv[2]);\n"), 0755)

		stdout, stderr, code := runSilo(t, "", "run", script, "node_arg")
		if code != 0 {
			t.Fatalf("Node execution failed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "NODE_OK node_arg") {
			t.Errorf("Expected 'NODE_OK node_arg', got: %s", stdout)
		}
	})

	t.Run("GNU_Bash_Debian_Base_Preservation", func(t *testing.T) {
		tempDir := t.TempDir()
		script := filepath.Join(tempDir, "test.bash")
		_ = os.WriteFile(script, []byte("arr=('BASH' 'OK')\necho \"${arr[@]}\"\n"), 0755)

		stdout, stderr, code := runSilo(t, "", "run", script)
		if code != 0 {
			t.Fatalf("Bash execution failed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "BASH OK") {
			t.Errorf("Expected 'BASH OK', got: %s", stdout)
		}
	})

	t.Run("Host_Glibc_ELF_Binary", func(t *testing.T) {
		stdout, stderr, code := runSilo(t, "", "run", "/bin/ls", "-d", "/bin")
		if code != 0 {
			t.Fatalf("Host binary failed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "/bin") {
			t.Errorf("Expected '/bin' in output, got: %s", stdout)
		}
	})
}

// =========================================================================
// TEST SUITE 4: Storage, Volume Mounts (-v) & Working Directory (-w)
// =========================================================================

func TestE2E_VolumeMounts(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("E2E volume tests require root privileges. Run with sudo.")
	}

	t.Run("Shorthand_Volume_And_Host_Write_Persistence", func(t *testing.T) {
		projectDir := t.TempDir()

		// Multi-file python project
		helper := filepath.Join(projectDir, "helper.py")
		_ = os.WriteFile(helper, []byte("def val(): return 'MULTIFILE_SUCCESS'\n"), 0644)

		main := filepath.Join(projectDir, "main.py")
		_ = os.WriteFile(main, []byte(`
import helper
import os
print("IMPORT:", helper.val())
print("CWD:", os.getcwd())
with open("output.txt", "w") as f:
    f.write("PERSISTED_DATA")
`), 0755)

		// Run with bare -v shorthand directly from the project directory
		stdout, stderr, code := runSilo(t, projectDir, "run", "-v", "main.py")
		if code != 0 {
			t.Fatalf("Volume execution failed (code %d): %s", code, stderr)
		}

		if !strings.Contains(stdout, "IMPORT: MULTIFILE_SUCCESS") {
			t.Errorf("Multi-file import failed! Stdout: %s", stdout)
		}
		if !strings.Contains(stdout, "CWD: /workspace") {
			t.Errorf("Expected CWD /workspace, got: %s", stdout)
		}

		// Verify host disk persistence
		outFile := filepath.Join(projectDir, "output.txt")
		content, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("File output.txt was not written to host SSD: %v", err)
		}
		if string(content) != "PERSISTED_DATA" {
			t.Errorf("Content mismatch in persisted file: got '%s'", string(content))
		}
	})

	t.Run("ReadOnly_Volume_Mount_Security", func(t *testing.T) {
		tempDir := t.TempDir()
		volSpec := fmt.Sprintf("%s:/data:ro", tempDir)

		stdout, stderr, code := runSilo(t, "", "run", "-v", volSpec, "/bin/sh", "-c", "touch /data/fail.txt")
		if code == 0 {
			t.Errorf("Expected write to :ro volume to fail, but succeeded! Stdout: %s", stdout)
		}
		if !strings.Contains(stderr, "Read-only file system") && !strings.Contains(stdout, "Read-only file system") {
			t.Errorf("Expected 'Read-only file system' error, got stdout: '%s', stderr: '%s'", stdout, stderr)
		}
	})
}

// =========================================================================
// TEST SUITE 5: Virtual /dev Filesystem Health
// =========================================================================

func TestE2E_VirtualDevFS(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("E2E /dev tests require root privileges. Run with sudo.")
	}

	t.Run("DevNull_Urandom_Shm_Available", func(t *testing.T) {
		cmdStr := "echo 'hello' > /dev/null && head -c 16 /dev/urandom | wc -c && df -h /dev/shm"
		stdout, stderr, code := runSilo(t, "", "run", "/bin/sh", "-c", cmdStr)
		if code != 0 {
			t.Fatalf("/dev verification failed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "16") {
			t.Errorf("Expected 16 bytes read from /dev/urandom, got: %s", stdout)
		}
		if !strings.Contains(stdout, "tmpfs") && !strings.Contains(stdout, "shm") {
			t.Errorf("Expected /dev/shm tmpfs mount, got: %s", stdout)
		}
	})
}

// =========================================================================
// TEST SUITE 6: CLI Commands (version, images, purge)
// =========================================================================

func TestE2E_CLI_Commands(t *testing.T) {
	t.Run("Version_Command", func(t *testing.T) {
		stdout, stderr, code := runSilo(t, "", "version")
		if code != 0 {
			t.Fatalf("Version failed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "v0.3.3") && !strings.Contains(stdout, "Silo Container Runtime") {
			t.Errorf("Unexpected version output: %s", stdout)
		}
	})

	t.Run("Images_Command", func(t *testing.T) {
		stdout, stderr, code := runSilo(t, "", "images")
		if code != 0 {
			t.Fatalf("Images failed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "IMAGE") || !strings.Contains(stdout, "STATUS") {
			t.Errorf("Expected images table headers, got: %s", stdout)
		}
	})
}
