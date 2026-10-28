#!/bin/bash
set -euo pipefail

TARGET_DIR="/usr/local/bin"
VERSION="stable"

# Parse arguments
while [ "$#" -gt 0 ]; do
    case "$1" in
        -d|--dir)
            TARGET_DIR="$2"
            shift
            ;;
        -v|--version)
            VERSION="$2"
            shift
            ;;
        -h|--help)
            echo "Usage: $0 [-d|--dir <directory>] [-v|--version <stable|latest|X.Y.Z>]"
            exit 0
            ;;
        *)
            echo "Error: Unknown option '$1'" >&2
            exit 1
            ;;
    esac
    shift
done

# The official installer puts a self-contained binary under $HOME; install
# into a throwaway HOME, then copy the real binary into TARGET_DIR so it is
# reachable from every user and from the bwrap sandbox.
WORK_HOME="$(mktemp -d)"
trap 'rm -rf "$WORK_HOME"' EXIT

curl -fsSL -o "$WORK_HOME/install.sh" https://claude.ai/install.sh
HOME="$WORK_HOME" bash "$WORK_HOME/install.sh" "$VERSION"

mkdir -p "$TARGET_DIR"
cp -L "$WORK_HOME/.local/bin/claude" "$TARGET_DIR/claude"
chmod 755 "$TARGET_DIR/claude"

"$TARGET_DIR/claude" --version
