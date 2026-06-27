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

echo "Querying release manifest..."
MANIFEST_URL="https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_amd64.json"
MANIFEST_JSON=$(curl -fsSL "$MANIFEST_URL" || wget -q -O - "$MANIFEST_URL")
URL=$(echo "$MANIFEST_JSON" | sed -n 's/.*"url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

if [ -z "$URL" ]; then
    echo "Error: Failed to parse download URL from manifest." >&2
    exit 1
fi

echo "Downloading package..."
curl -fsSL "$URL" -o /tmp/agy.tar.gz || wget -q "$URL" -O /tmp/agy.tar.gz

echo "Extracting binary..."
mkdir -p "$TARGET_DIR"
tar -xzf /tmp/agy.tar.gz -C "$TARGET_DIR" antigravity
mv "$TARGET_DIR/antigravity" "$TARGET_DIR/agy"
chmod +x "$TARGET_DIR/agy"

rm -f /tmp/agy.tar.gz
echo "Antigravity CLI installed successfully to $TARGET_DIR/agy"
