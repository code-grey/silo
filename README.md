# Silo

> **Current Status:** v0.1-alpha (Chroot Implementation)  
> *Migrating to `pivot_root` and Cgroups v2 for v0.2*

**Silo** is a lightweight container runtime written in Go from scratch. 
It is designed as an educational deep-dive into the Linux Kernel primitives that power tools like Docker and Kubernetes.

## Goals
- **Demystify Containers:** No magic. Just raw Linux syscalls.
- **Security First:** Moving from `chroot` (v0.1) to `pivot_root` (v0.2) to prevent jailbreaks.
- **Resource Control:** Implementing Cgroups v2 for memory and CPU constraints.

## Architecture
- **Language:** Go (Golang)
- **Isolation:** Linux Namespaces (`CLONE_NEWPID`, `CLONE_NEWNS`, `CLONE_NEWUTS`)
- **Filesystem:** `pivot_root` based root filesystem swapping (In Progress)
- **Networking:** Bridge networking with veth pairs (Planned)

## Usage (v0.1)
```bash
# Build
go build -o silo main.go

# Run a shell inside the container (Requires Root)
sudo ./silo run /bin/sh
```


## Instruction
For v0.1-alpha you must create this and download the alpine image (tmp folder so that these things are flushed away on reboot)
```
mkdir -p /tmp/silo-container/rootfs

cd /tmp/silo-container

curl -o alpine.tar.gz https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-minirootfs-3.19.0-x86_64.tar.gz

tar -xvf alpine.tar.gz -C rootfs

```