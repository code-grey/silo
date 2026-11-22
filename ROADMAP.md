# Silo Roadmap

This document outlines the development milestones for Silo, tracking our progress from a simple `chroot` wrapper to a fully-featured container runtime.

## v0.1 - The Basics (Completed)
- [x] **chroot Isolation:** Basic filesystem jailing using `chroot`.
- [x] **Basic Namespaces:** Implement foundational Linux Namespaces (`CLONE_NEWPID`, `CLONE_NEWNS`, `CLONE_NEWUTS`).
- [x] **PID 1 Execution:** Using `syscall.Exec` to ensure the container payload runs as PID 1 inside the container (instead of the Go process).

## v0.2 - Security & Resources (Completed)
- [x] **pivot_root Migration:** Move from `chroot` to `pivot_root` to securely swap the root filesystem and prevent jailbreaks.
- [x] **Resource Control (Cgroups v2):** Implement memory and CPU constraints using the Cgroups v2 `/sys/fs/cgroup` interface.
- [x] **Catastrophe Post-Mortem:** Extract kernel cgroup events (`memory.events`, `memory.peak`, `pids.events`) on crash to diagnose OOM kills and fork-bombs.

## v0.3 - Storage, Images & Developer Ergonomics (In Progress)
- [x] **OverlayFS Layering:** Implement OverlayFS to separate a read-only base image (`lowerdir`) from an ephemeral container writable layer (`upperdir` + `workdir`).
- [x] **Single-File Drop-in Runner:** Ingest local scripts or binaries directly into the ephemeral upper layer (`/bin/`) without exposing host directories.
- [x] **Structured Crash Dump:** Output machine-readable JSON post-mortem reports (`silo-crash-<id>.json`) for automated pipelines and autograders.
- [ ] **Image Cache & Auto-Pull:** Automatically pull, verify, and locally cache base rootfs images (Alpine, Ubuntu, etc.) with offline-first local cache fallback.
- [ ] **Smart ELF Binary Inspection (`debug/elf`):** Inspect target binary headers (`PT_INTERP`) using Go's standard library to detect static vs. glibc vs. musl dependencies, automatically picking the matching rootfs and preventing cryptic "file not found" dynamic linker errors.
- [ ] **Volume / Bind Mounts (`-v`):** Support mounting host directories or project files into the container (e.g. `-v $(pwd):/app`) for seamless script and artifact execution.

## v0.4 - Rootless Containers (Planned)
- [ ] **User Namespaces:** Implement User Namespaces (`CLONE_NEWUSER`) along with UID/GID mappings. This will allow users to run `silo` containers without requiring `sudo` privileges on the host.

## v0.5 - Networking (Planned)
- [ ] **Bridge Networking:** Set up bridge networking with `veth` pairs to provide network isolation and grant internet access to the containers.

## v0.6 - Evaluation & Autograding Engine ("Silo Judge" Planned)
- [ ] **Batch Testcase Evaluation:** Automated stdin injection and stdout/stderr capture against expected outputs with detailed diff reporting (AC, WA, TLE, MLE, RTE verdicts).
- [ ] **Anti-Cheat Countermeasures:** Hidden test suites, dynamic randomized input generation, and resource enforcement to prevent hardcoded print matching.
- [ ] **High-Throughput gRPC Daemon:** Pre-warmed container worker pool communicating over UNIX domain sockets with gRPC for sub-millisecond test execution.

## Performance & Daemonless Speedups (Planned)
- [ ] **RAM-Backed OverlayFS:** Mount instance directories on `tmpfs` to eliminate NVMe/SSD disk write latency.
- [ ] **Zero-Copy Payload Ingestion:** Use `os.Link()` (hardlinks) instead of byte-copying for local payload injection on the same filesystem.
- [ ] **Raw Syscall Process Spawning:** Explore raw `clone3`/`vfork` syscall invocations to bypass Go runtime thread locks on process spawn.
- [ ] **Transient Worker Streaming:** Stream testcases over anonymous UNIX domain sockets or stdin/stdout within a single persistent sandbox for high-throughput batch grading.
