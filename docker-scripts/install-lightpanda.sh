#!/bin/bash
set -euo pipefail

TARGET_DIR="/usr/local/bin"

# Parse arguments
while [ "$#" -gt 0 ]; do
    case "$1" in
        -d|--dir)
            TARGET_DIR="$2"
            shift
            ;;
    esac
    shift
done

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)
        LP_ARCH="x86_64"
        ;;
    aarch64|arm64)
        LP_ARCH="aarch64"
        ;;
    *)
        echo "Error: Unsupported architecture: $ARCH" >&2
        exit 1
        ;;
esac

URL="https://github.com/lightpanda-io/browser/releases/download/nightly/lightpanda-${LP_ARCH}-linux"

echo "Downloading Lightpanda (${LP_ARCH})..."
mkdir -p "$TARGET_DIR"
curl -fsSL "$URL" -o "$TARGET_DIR/lightpanda" || wget -q "$URL" -O "$TARGET_DIR/lightpanda"
chmod +x "$TARGET_DIR/lightpanda"

echo "Lightpanda installed successfully to $TARGET_DIR/lightpanda"
