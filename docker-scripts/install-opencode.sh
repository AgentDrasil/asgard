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

URL="https://github.com/anomalyco/opencode/releases/latest/download/opencode-linux-x64.tar.gz"

echo "Downloading package..."
curl -fsSL "$URL" -o /tmp/opencode.tar.gz || wget -q "$URL" -O /tmp/opencode.tar.gz

echo "Extracting binary..."
mkdir -p "$TARGET_DIR"
tar -xzf /tmp/opencode.tar.gz -C "$TARGET_DIR" opencode
chmod +x "$TARGET_DIR/opencode"

rm -f /tmp/opencode.tar.gz
echo "OpenCode CLI installed successfully to $TARGET_DIR/opencode"
