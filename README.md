# Silo

> **Current Status:** pivotRoot implemented (untested with exploits yet)  
> **cgroups are to be added next** 

**Silo** is a lightweight container runtime written in Go from scratch. 
It is designed as an educational deep-dive into the Linux Kernel primitives that power tools like Docker and Kubernetes.

## Goals
- **Demystify Containers:** No magic. Just raw Linux syscalls.
- **Security First:** Implemented pivot_root.
- **Resource Control:** Implementing Cgroups v2 for memory and CPU constraints.

## Architecture
- **Language:** Go (Golang)
- **Isolation:** Linux Namespaces (`CLONE_NEWPID`, `CLONE_NEWNS`, `CLONE_NEWUTS`)
- **Filesystem:** `pivot_root` based root filesystem swapping [✔️]
- **Control groups for resource limiting (in progress) []**
- **Networking:** Bridge networking with veth pairs (Planned)

## Usage (v0.1.1)
```bash
# Build
go build -o silo main.go

# Run a shell inside the container (Requires Root)
sudo ./silo run /bin/sh
```


## Instruction
For v0.1.1 you must create this and download the alpine image (tmp folder so that these things are flushed away on reboot)
```
mkdir -p /tmp/silo-container/rootfs

cd /tmp/silo-container

curl -o alpine.tar.gz https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-minirootfs-3.19.0-x86_64.tar.gz

tar -xvf alpine.tar.gz -C rootfs
```

## Testing Resource Limits (Cgroups v2)

You can verify that Silo's Cgroups v2 resource limits and the Catastrophe Reporter are working using two terminal windows.

### Terminal 1: Run the Container
```bash
cd silo-core
go build -o silo .
sudo ./silo run /bin/sh
```

### Terminal 2: Inspect Limits from the Host
While the container is running in Terminal 1, open a second terminal on your host:

```bash
# 1. Verify the per-container cgroup directory was created
ls -d /sys/fs/cgroup/silo-*

# 2. Check the 50 MB memory ceiling
cat /sys/fs/cgroup/silo-*/memory.max
# Output: 52428800

# 3. Check the process limit (max 50 processes)
cat /sys/fs/cgroup/silo-*/pids.max
# Output: 50

# 4. Check that the container PID is actively tracked
cat /sys/fs/cgroup/silo-*/cgroup.procs
```

### Triggering the OOM Killer & Catastrophe Reporter
Back in **Terminal 1** (inside the container `/ #`), test the memory limit by exhausting RAM with a string-doubling loop:

```sh
x=a; while true; do x=$x$x; done
```

When memory usage crosses 50 MB, the Linux kernel invokes the Out-Of-Memory (OOM) killer. Silo intercepts the crash and prints the built-in catastrophe post-mortem before cleaning up:

```text
================== SILO CATASTROPHE POST-MORTEM ==================
CRITICAL: Container was terminated by the Linux OOM Killer!
• Failure Cause : Memory ceiling exceeded (52428800 bytes / ~50 MB)
• Peak Memory   : 52428800 bytes
• OOM Kills     : 1
• Limit Hit Max : 1 times
==================================================================
```

Once the container exits, check **Terminal 2** again with `ls -d /sys/fs/cgroup/silo-*` to confirm that the cgroup directory was cleanly deleted.

