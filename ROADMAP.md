# Silo Roadmap

This document outlines the development milestones for Silo, tracking our progress from a simple `chroot` wrapper to a fully-featured container runtime.

## v0.1 - The Basics (Completed)
- [x] **chroot Isolation:** Basic filesystem jailing using `chroot`.
- [x] **Basic Namespaces:** Implement foundational Linux Namespaces (`CLONE_NEWPID`, `CLONE_NEWNS`, `CLONE_NEWUTS`).
- [x] **PID 1 Execution:** Using `syscall.Exec` to ensure the container payload runs as PID 1 inside the container (instead of the Go process).

## v0.2 - Security & Resources (In Progress)
- [x] **pivot_root Migration:** Move from `chroot` to `pivot_root` to securely swap the root filesystem and prevent jailbreaks.
- [ ] **Resource Control (Cgroups v2):** Implement memory and CPU constraints using the Cgroups v2 `/sys/fs/cgroup` interface.

## v0.3 - Image Layers (Planned)
- [ ] **OverlayFS:** Implement OverlayFS to separate a read-only base image (`lowerdir`) from the container's writable layer (`upperdir`). This prevents destruction of the base rootfs and allows multiple containers to share the same base image seamlessly.

## v0.4 - Rootless Containers (Planned)
- [ ] **User Namespaces:** Implement User Namespaces (`CLONE_NEWUSER`) along with UID/GID mappings. This will allow users to run `silo` containers without requiring `sudo` privileges on the host.

## v0.5 - Networking (Planned)
- [ ] **Bridge Networking:** Set up bridge networking with `veth` pairs to provide network isolation and grant internet access to the containers.
