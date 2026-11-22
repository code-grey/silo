package main

import (
	"bytes"
	"debug/elf"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PayloadType classifies the executable payload passed to Silo
type PayloadType int

const (
	PayloadUnknown PayloadType = iota
	PayloadStaticELF
	PayloadGlibcELF
	PayloadMuslELF
	PayloadScript
	PayloadBytecode
)

// InspectionResult holds diagnostic classification and execution strategy
type InspectionResult struct {
	Type             PayloadType
	RecommendedImage string   // e.g. "alpine", "debian", "python", "node", "ruby", "perl", "php"
	Runner           []string // e.g. ["python3"], ["node"], ["java", "-jar"], nil for native ELFs
	Interpreter      string   // Human-readable diagnostic (e.g. "python3", "/lib64/ld-linux-x86-64.so.2")
	Reason           string   // Explanation logged to Silo Live Thinking
}

// InspectPayload examines an arbitrary host file to determine its runtime dependencies.
// It is non-blocking, reading only header bytes (max 512B) and ELF headers without loading file bodies.
func InspectPayload(hostPath string) InspectionResult {
	defaultResult := InspectionResult{
		Type:             PayloadUnknown,
		RecommendedImage: "alpine",
		Runner:           nil,
		Interpreter:      "/bin/sh",
		Reason:           "Defaulting to lightweight Alpine base",
	}

	info, err := os.Stat(hostPath)
	if err != nil || info.IsDir() {
		return defaultResult
	}

	// 1. Check if it is an ELF binary (compiled C, C++, Go, Rust, Zig)
	if elfFile, err := elf.Open(hostPath); err == nil {
		defer elfFile.Close()

		var interp string
		for _, prog := range elfFile.Progs {
			if prog.Type == elf.PT_INTERP {
				buf := make([]byte, prog.Filesz)
				if _, err := prog.ReadAt(buf, 0); err == nil {
					interp = strings.TrimRight(string(buf), "\x00")
				}
				break
			}
		}

		if interp == "" {
			return InspectionResult{
				Type:             PayloadStaticELF,
				RecommendedImage: "alpine",
				Runner:           nil,
				Interpreter:      "none (static)",
				Reason:           "Statically linked ELF binary (zero shared library dependencies)",
			}
		}

		if strings.Contains(interp, "musl") {
			return InspectionResult{
				Type:             PayloadMuslELF,
				RecommendedImage: "alpine",
				Runner:           nil,
				Interpreter:      interp,
				Reason:           "Dynamically linked against musl libc (native to Alpine)",
			}
		}

		// Dynamically linked against glibc (native to Debian/Ubuntu)
		return InspectionResult{
			Type:             PayloadGlibcELF,
			RecommendedImage: "debian",
			Runner:           nil,
			Interpreter:      interp,
			Reason:           "Dynamically linked against glibc (native to Debian)",
		}
	}

	// 2. Read first 512 bytes for magic bytes, shebang, and extension sniffing
	file, err := os.Open(hostPath)
	if err != nil {
		return defaultResult
	}
	defer file.Close()

	header := make([]byte, 512)
	n, _ := io.ReadFull(file, header)
	header = header[:n]

	// Check Bytecode Magic Bytes:
	// Java .class: 0xCAFEBABE
	if len(header) >= 4 && bytes.Equal(header[:4], []byte{0xCA, 0xFE, 0xBA, 0xBE}) {
		baseName := filepath.Base(hostPath)
		className := strings.TrimSuffix(baseName, filepath.Ext(baseName))
		return InspectionResult{
			Type:             PayloadBytecode,
			RecommendedImage: "java",
			Runner:           []string{"java", "-cp", "/bin", className},
			Interpreter:      "java",
			Reason:           "Java compiled bytecode (magic 0xCAFEBABE)",
		}
	}

	// Java .jar: PK zip header 0x50 0x4B 0x03 0x04 with .jar extension
	ext := strings.ToLower(filepath.Ext(hostPath))
	if len(header) >= 4 && bytes.Equal(header[:4], []byte{0x50, 0x4B, 0x03, 0x04}) && ext == ".jar" {
		return InspectionResult{
			Type:             PayloadBytecode,
			RecommendedImage: "java",
			Runner:           []string{"java", "-jar"},
			Interpreter:      "java -jar",
			Reason:           "Java JAR executable archive (ZIP format)",
		}
	}

	// Check Shebang (#!) on first line
	if bytes.HasPrefix(header, []byte("#!")) {
		firstLineEnd := bytes.IndexByte(header, '\n')
		if firstLineEnd == -1 {
			firstLineEnd = len(header)
		}
		shebang := strings.ToLower(string(header[2:firstLineEnd]))

		switch {
		case strings.Contains(shebang, "python"):
			return InspectionResult{
				Type:             PayloadScript,
				RecommendedImage: "python",
				Runner:           []string{"python3"},
				Interpreter:      "python3",
				Reason:           "Python script detected via shebang",
			}
		case strings.Contains(shebang, "node"):
			return InspectionResult{
				Type:             PayloadScript,
				RecommendedImage: "node",
				Runner:           []string{"node"},
				Interpreter:      "node",
				Reason:           "Node.js script detected via shebang",
			}
		case strings.Contains(shebang, "ruby"):
			return InspectionResult{
				Type:             PayloadScript,
				RecommendedImage: "ruby",
				Runner:           []string{"ruby"},
				Interpreter:      "ruby",
				Reason:           "Ruby script detected via shebang",
			}
		case strings.Contains(shebang, "perl"):
			return InspectionResult{
				Type:             PayloadScript,
				RecommendedImage: "perl",
				Runner:           []string{"perl"},
				Interpreter:      "perl",
				Reason:           "Perl script detected via shebang",
			}
		case strings.Contains(shebang, "php"):
			return InspectionResult{
				Type:             PayloadScript,
				RecommendedImage: "php",
				Runner:           []string{"php"},
				Interpreter:      "php",
				Reason:           "PHP script detected via shebang",
			}
		case strings.Contains(shebang, "bash"):
			return InspectionResult{
				Type:             PayloadScript,
				RecommendedImage: "debian",
				Runner:           []string{"bash"},
				Interpreter:      "bash",
				Reason:           "GNU Bash script detected via shebang (requires Debian base)",
			}
		case strings.Contains(shebang, "sh"):
			return InspectionResult{
				Type:             PayloadScript,
				RecommendedImage: "alpine",
				Runner:           []string{"sh"},
				Interpreter:      "sh",
				Reason:           "POSIX shell script detected via shebang",
			}
		}
	}

	// 3. Extension Sniffing (Handles shebang-less user scripts!)
	switch ext {
	case ".py":
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "python",
			Runner:           []string{"python3"},
			Interpreter:      "python3",
			Reason:           "Python script detected via extension (.py)",
		}
	case ".js", ".mjs", ".cjs":
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "node",
			Runner:           []string{"node"},
			Interpreter:      "node",
			Reason:           "JavaScript file detected via extension (" + ext + ")",
		}
	case ".rb":
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "ruby",
			Runner:           []string{"ruby"},
			Interpreter:      "ruby",
			Reason:           "Ruby script detected via extension (.rb)",
		}
	case ".pl":
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "perl",
			Runner:           []string{"perl"},
			Interpreter:      "perl",
			Reason:           "Perl script detected via extension (.pl)",
		}
	case ".php":
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "php",
			Runner:           []string{"php"},
			Interpreter:      "php",
			Reason:           "PHP script detected via extension (.php)",
		}
	case ".sh":
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "alpine",
			Runner:           []string{"sh"},
			Interpreter:      "sh",
			Reason:           "POSIX shell script detected via extension (.sh)",
		}
	case ".bash":
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "debian",
			Runner:           []string{"bash"},
			Interpreter:      "bash",
			Reason:           "Bash script detected via extension (.bash)",
		}
	}

	// 4. Content Heuristics Sniffing (No extension, no shebang)
	contentStr := string(header)
	if strings.Contains(contentStr, "import ") || strings.Contains(contentStr, "def ") || strings.Contains(contentStr, "print(") {
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "python",
			Runner:           []string{"python3"},
			Interpreter:      "python3",
			Reason:           "Python syntax heuristics detected in header",
		}
	}
	if strings.Contains(contentStr, "console.log") || strings.Contains(contentStr, "require(") || strings.Contains(contentStr, "const ") {
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "node",
			Runner:           []string{"node"},
			Interpreter:      "node",
			Reason:           "JavaScript syntax heuristics detected in header",
		}
	}
	if strings.Contains(contentStr, "<?php") {
		return InspectionResult{
			Type:             PayloadScript,
			RecommendedImage: "php",
			Runner:           []string{"php"},
			Interpreter:      "php",
			Reason:           "PHP tag detected in header",
		}
	}

	return defaultResult
}
