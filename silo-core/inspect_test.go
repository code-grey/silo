package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectPayloadAllLanguages(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name          string
		filename      string
		content       []byte
		expectedType  PayloadType
		expectedImage string
		expectedRun   string
	}{
		{
			name:          "Shebang-less Python (.py)",
			filename:      "app.py",
			content:       []byte("print('hello world')\n"),
			expectedType:  PayloadScript,
			expectedImage: "python",
			expectedRun:   "python3",
		},
		{
			name:          "Shebang-less Node (.js)",
			filename:      "index.js",
			content:       []byte("console.log('hello');\n"),
			expectedType:  PayloadScript,
			expectedImage: "node",
			expectedRun:   "node",
		},
		{
			name:          "Shebang-less Ruby (.rb)",
			filename:      "main.rb",
			content:       []byte("puts 'hello'\n"),
			expectedType:  PayloadScript,
			expectedImage: "ruby",
			expectedRun:   "ruby",
		},
		{
			name:          "Shebang-less Perl (.pl)",
			filename:      "script.pl",
			content:       []byte("print 'hello\\n';\n"),
			expectedType:  PayloadScript,
			expectedImage: "perl",
			expectedRun:   "perl",
		},
		{
			name:          "Shebang-less PHP (.php)",
			filename:      "index.php",
			content:       []byte("<?php echo 'hello'; ?>\n"),
			expectedType:  PayloadScript,
			expectedImage: "php",
			expectedRun:   "php",
		},
		{
			name:          "Java compiled bytecode (.class)",
			filename:      "App.class",
			content:       []byte{0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x00, 0x00, 0x34},
			expectedType:  PayloadBytecode,
			expectedImage: "java",
			expectedRun:   "java",
		},
		{
			name:          "Java executable archive (.jar)",
			filename:      "app.jar",
			content:       []byte{0x50, 0x4B, 0x03, 0x04, 0x14, 0x00, 0x08, 0x00},
			expectedType:  PayloadBytecode,
			expectedImage: "java",
			expectedRun:   "java",
		},
		{
			name:          "No extension with Python heuristics",
			filename:      "worker",
			content:       []byte("import os\ndef run():\n    print('worker')\n"),
			expectedType:  PayloadScript,
			expectedImage: "python",
			expectedRun:   "python3",
		},
		{
			name:          "No extension with Node heuristics",
			filename:      "service",
			content:       []byte("const http = require('http');\nconsole.log('starting');\n"),
			expectedType:  PayloadScript,
			expectedImage: "node",
			expectedRun:   "node",
		},
		{
			name:          "Python with shebang",
			filename:      "cli",
			content:       []byte("#!/usr/bin/env python3\nprint('hello')\n"),
			expectedType:  PayloadScript,
			expectedImage: "python",
			expectedRun:   "python3",
		},
		{
			name:          "GNU Bash with shebang",
			filename:      "deploy.sh",
			content:       []byte("#!/bin/bash\necho 'deploying'\n"),
			expectedType:  PayloadScript,
			expectedImage: "debian",
			expectedRun:   "bash",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			filePath := filepath.Join(tempDir, tc.filename)
			if err := os.WriteFile(filePath, tc.content, 0755); err != nil {
				t.Fatalf("Failed to create test file: %v", err)
			}

			result := InspectPayload(filePath)

			if result.Type != tc.expectedType {
				t.Errorf("Type mismatch: got %v, want %v", result.Type, tc.expectedType)
			}
			if result.RecommendedImage != tc.expectedImage {
				t.Errorf("RecommendedImage mismatch: got %v, want %v", result.RecommendedImage, tc.expectedImage)
			}
			if len(result.Runner) > 0 && result.Runner[0] != tc.expectedRun {
				t.Errorf("Runner mismatch: got %v, want %v", result.Runner[0], tc.expectedRun)
			}
		})
	}

	// Test host glibc binary if /bin/ls exists
	if _, err := os.Stat("/bin/ls"); err == nil {
		t.Run("Host ELF (/bin/ls)", func(t *testing.T) {
			result := InspectPayload("/bin/ls")
			if result.Type != PayloadGlibcELF {
				t.Errorf("Expected PayloadGlibcELF for /bin/ls, got %v", result.Type)
			}
			if result.RecommendedImage != "debian" {
				t.Errorf("Expected debian image for /bin/ls, got %v", result.RecommendedImage)
			}
			if len(result.Runner) != 0 {
				t.Errorf("Expected no runner for native ELF binary, got %v", result.Runner)
			}
		})
	}
}
