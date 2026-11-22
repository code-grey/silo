package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{104857600, "100.0 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tc := range tests {
		got := formatBytes(tc.bytes)
		if got != tc.expected {
			t.Errorf("formatBytes(%d) = %s, want %s", tc.bytes, got, tc.expected)
		}
	}
}

func TestGetDirSize(t *testing.T) {
	tempDir := t.TempDir()
	f1 := filepath.Join(tempDir, "file1.txt")
	f2 := filepath.Join(tempDir, "file2.txt")

	_ = os.WriteFile(f1, []byte("12345"), 0644)  // 5 bytes
	_ = os.WriteFile(f2, []byte("1234567"), 0644) // 7 bytes

	size, err := getDirSize(tempDir)
	if err != nil {
		t.Fatalf("getDirSize failed: %v", err)
	}
	if size != 12 {
		t.Errorf("getDirSize = %d, want 12", size)
	}
}

func TestParseRunConfig(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantImage   string
		wantWorkdir string
		wantVols    int
		wantPayload string
	}{
		{
			name:        "Bare -v shorthand",
			args:        []string{"-v", "python3", "app.py"},
			wantImage:   "",
			wantWorkdir: "/workspace",
			wantVols:    1,
			wantPayload: "python3",
		},
		{
			name:        "Explicit volume with read-only and custom workdir",
			args:        []string{"-v", "/tmp:/data:ro", "-w", "/app", "--image", "debian", "./binary"},
			wantImage:   "debian",
			wantWorkdir: "/app",
			wantVols:    1,
			wantPayload: "./binary",
		},
		{
			name:        "Standard single script execution",
			args:        []string{"./script.py", "arg1", "arg2"},
			wantImage:   "",
			wantWorkdir: "",
			wantVols:    0,
			wantPayload: "./script.py",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseRunConfig(tc.args)
			if err != nil {
				t.Fatalf("parseRunConfig failed: %v", err)
			}
			if cfg.Image != tc.wantImage {
				t.Errorf("Image = %s, want %s", cfg.Image, tc.wantImage)
			}
			if cfg.Workdir != tc.wantWorkdir {
				t.Errorf("Workdir = %s, want %s", cfg.Workdir, tc.wantWorkdir)
			}
			if len(cfg.Volumes) != tc.wantVols {
				t.Errorf("Volumes count = %d, want %d", len(cfg.Volumes), tc.wantVols)
			}
			if len(cfg.PayloadArgs) == 0 || cfg.PayloadArgs[0] != tc.wantPayload {
				t.Errorf("Payload[0] = %s, want %s", cfg.PayloadArgs[0], tc.wantPayload)
			}
			if tc.wantVols > 0 && strings.Contains(tc.args[0], "-v") {
				if !strings.Contains(cfg.Volumes[0], ":") {
					t.Errorf("Expected formatted volume string, got %s", cfg.Volumes[0])
				}
			}
		})
	}
}
