#!/usr/bin/env bash
# scripts/build.sh — cross-compile cfgsync for linux/amd64 and windows/amd64.
#
# Usage:
#   bash scripts/build.sh                                # both targets
#   TARGETS="linux/amd64" bash scripts/build.sh         # one target
#   OUTPUT_DIR=/tmp/art bash scripts/build.sh           # custom output dir
#
# Runs on Linux, macOS, and Windows (Git Bash). The pure-Go sqlite driver
# (modernc.org/sqlite) lets us cross-compile without a C toolchain, so
# CGO_ENABLED=0 is set explicitly.

set -euo pipefail

OUTPUT_DIR="${OUTPUT_DIR:-./dist}"
TARGETS="${TARGETS:-linux/amd64 windows/amd64}"

# Resolve repo root via git so CWD doesn't matter; fall back to script dir.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if REPO_ROOT="$(cd "$SCRIPT_DIR" && git rev-parse --show-toplevel 2>/dev/null)"; then
    :
else
    REPO_ROOT="$SCRIPT_DIR"
fi
cd "$REPO_ROOT"

# Sanity checks.
if ! command -v go >/dev/null 2>&1; then
    echo "ERROR: go not found in PATH (install from https://go.dev/dl/)" >&2
    exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
    SHA_CMD=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
    SHA_CMD=(shasum -a 256)
else
    echo "ERROR: neither sha256sum nor shasum available" >&2
    exit 1
fi

mkdir -p "$OUTPUT_DIR"

echo "==> go mod download"
go mod download

build_one() {
    local target="$1"
    local goos="${target%/*}"
    local goarch="${target#*/}"
    local ext=""
    [ "$goos" = "windows" ] && ext=".exe"
    local out="$OUTPUT_DIR/cfgsync-${goos}-${goarch}${ext}"

    echo "==> building $out (GOOS=$goos GOARCH=$goarch)"
    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
        go build -trimpath -ldflags="-s -w" -o "$out" ./cmd/server

    local sha size
    sha="$("${SHA_CMD[@]}" "$out" | awk '{print $1}')"
    size="$(du -h "$out" | awk '{print $1}')"
    echo "$sha" > "$out.sha256"
    echo "    size:   $size"
    echo "    sha256: $sha"
}

for target in $TARGETS; do
    build_one "$target"
done

echo
echo "DONE. Artifacts in $OUTPUT_DIR:"
( cd "$OUTPUT_DIR" && ls -la cfgsync-* )
