#!/usr/bin/env bash
set -e

# Silo Automated Test Runner
# Rebuilds the root binary and executes both unit and end-to-end integration tests.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# Locate Go compiler
GO_BIN="$(command -v go || true)"
if [ -z "$GO_BIN" ]; then
    if [ -x "/usr/local/go/bin/go" ]; then
        GO_BIN="/usr/local/go/bin/go"
        export PATH="/usr/local/go/bin:$PATH"
    else
        echo "[ERROR] Go compiler not found in PATH or /usr/local/go/bin" >&2
        exit 1
    fi
fi

echo "=================================================="
echo " [SILO TEST HARNESS] Building binary at repo root"
echo "=================================================="
printf "Compiling ./silo (-ldflags=\"-s -w\") from ./silo-core ... "
(cd silo-core && "$GO_BIN" build -ldflags="-s -w" -o ../silo .)
echo "✓ Done ($(ls -lh silo | awk '{print $5}'))"
echo ""

echo "=================================================="
echo " [SILO TEST HARNESS] Running silo-core unit tests"
echo "=================================================="
"$GO_BIN" test -v ./silo-core/...
echo ""

echo "=================================================="
echo " [SILO TEST HARNESS] Running E2E integration tests"
echo "=================================================="
echo "Running E2E tests in unprivileged rootless mode (no sudo required)..."
"$GO_BIN" test -v -timeout 120s ./tests/...

echo ""
echo "=================================================="
echo " ✓ All Silo test suites passed successfully!"
echo "=================================================="
