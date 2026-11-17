#!/usr/bin/env bash
# ==============================================================================
# Silo Dev Utility: Alpine RootFS Installer
# Sets up a pristine Alpine minirootfs at /tmp/silo-container/rootfs for testing.
# ==============================================================================

set -euo pipefail

TARGET_DIR="/tmp/silo-container"
ROOTFS_DIR="${TARGET_DIR}/rootfs"
ALPINE_VERSION="3.19.0"

# 1. Dynamic Architecture Detection
RAW_ARCH="$(uname -m)"
case "${RAW_ARCH}" in
    x86_64)  ARCH="x86_64" ;;
    aarch64|arm64) ARCH="aarch64" ;;
    *)
        echo -e "\033[1;31m[Error]\033[0m Unsupported architecture: ${RAW_ARCH}" >&2
        exit 1
        ;;
esac

ALPINE_URL="https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/${ARCH}/alpine-minirootfs-${ALPINE_VERSION}-${ARCH}.tar.gz"
ARCHIVE_PATH="${TARGET_DIR}/alpine.tar.gz"
TMP_ARCHIVE="${ARCHIVE_PATH}.tmp"

# Clean up temp file on exit or interrupt
trap 'rm -f "${TMP_ARCHIVE}"' EXIT INT TERM

echo -e "\033[1;34m[Silo Setup]\033[0m Initializing local container rootfs for \033[1m${ARCH}\033[0m..."

# 2. Dependency Checks (Only tools we strictly require)
for cmd in curl tar; do
    if ! command -v "$cmd" &>/dev/null; then
        echo -e "\033[1;31m[Error]\033[0m Required tool '$cmd' is not installed." >&2
        exit 1
    fi
done

# 3. Prepare Target Directories
mkdir -p "${TARGET_DIR}"

# 4. Validate Existing Archive (Uses tar -tzf rather than external 'file' command)
if [ -f "${ARCHIVE_PATH}" ]; then
    if ! tar -tzf "${ARCHIVE_PATH}" &>/dev/null; then
        echo -e "\033[1;33m[Silo Setup]\033[0m Existing archive is corrupted or not a valid gzip. Re-downloading..."
        rm -f "${ARCHIVE_PATH}"
    fi
fi

# 5. Atomic Download
if [ ! -f "${ARCHIVE_PATH}" ]; then
    echo -e "\033[1;34m[Silo Setup]\033[0m Downloading Alpine minirootfs v${ALPINE_VERSION}..."
    curl -fSL --progress-bar "${ALPINE_URL}" -o "${TMP_ARCHIVE}"
    mv "${TMP_ARCHIVE}" "${ARCHIVE_PATH}"
    echo -e "\033[1;32m[Silo Setup]\033[0m Download complete."
else
    echo -e "\033[1;32m[Silo Setup]\033[0m Valid cached archive found at ${ARCHIVE_PATH}."
fi

# 6. Extract Rootfs (Atomic clean recreate)
echo -e "\033[1;34m[Silo Setup]\033[0m Extracting into ${ROOTFS_DIR}..."
rm -rf "${ROOTFS_DIR}"
mkdir -p "${ROOTFS_DIR}"
tar -xzf "${ARCHIVE_PATH}" -C "${ROOTFS_DIR}"

# 7. Verify Extraction Integrity
if [ ! -x "${ROOTFS_DIR}/bin/sh" ]; then
    echo -e "\033[1;31m[Error]\033[0m Rootfs extraction failed: ${ROOTFS_DIR}/bin/sh not found or not executable." >&2
    exit 1
fi

echo -e "\033[1;32m[Silo Setup] Success!\033[0m Alpine rootfs is ready at: ${ROOTFS_DIR}"
