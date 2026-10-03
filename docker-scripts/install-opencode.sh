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
        -h|--help)
            echo "Usage: $0 [-d|--dir <directory>]"
            exit 0
            ;;
        *)
            echo "Error: Unknown option '$1'" >&2
            exit 1
            ;;
    esac
    shift
done

# Detect OS
raw_os=$(uname -s)
os=$(echo "$raw_os" | tr '[:upper:]' '[:lower:]')
case "$raw_os" in
    Darwin*) os="darwin" ;;
    Linux*) os="linux" ;;
    MINGW*|MSYS*|CYGWIN*) os="windows" ;;
esac

# Detect Architecture
arch=$(uname -m)
case "$arch" in
    x86_64) arch="x64" ;;
    aarch64|arm64) arch="arm64" ;;
    *)
        echo "Error: Unsupported architecture: $arch" >&2
        exit 1
        ;;
esac

# Check for AVX2 support on x64
needs_baseline=false
if [ "$arch" = "x64" ]; then
    if [ "$os" = "linux" ] && ! grep -qwi avx2 /proc/cpuinfo 2>/dev/null; then
        needs_baseline=true
    elif [ "$os" = "darwin" ]; then
        avx2=$(sysctl -n hw.optional.avx2_0 2>/dev/null || echo 0)
        if [ "$avx2" != "1" ]; then
            needs_baseline=true
        fi
    fi
fi

# Check for musl libc on Linux
is_musl=false
if [ "$os" = "linux" ]; then
    if [ -f /etc/alpine-release ]; then
        is_musl=true
    elif command -v ldd >/dev/null 2>&1 && ldd --version 2>&1 | grep -qi musl; then
        is_musl=true
    fi
fi

target="$os-$arch"
if [ "$needs_baseline" = "true" ]; then
    target="$target-baseline"
fi
if [ "$is_musl" = "true" ]; then
    target="$target-musl"
fi

echo "Fetching latest version metadata..."
metadata=$(curl -fsSL https://opencode.ai/update/api/latest/cli/npm 2>/dev/null || wget -q -O - https://opencode.ai/update/api/latest/cli/npm 2>/dev/null || true)
specific_version=$(echo "$metadata" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
package=$(echo "$metadata" | sed -n 's/.*"package"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')

if [ -z "$specific_version" ] || [ -z "$package" ]; then
    echo "Error: Failed to fetch version information from metadata API" >&2
    exit 1
fi
package_scope="${package%/cli}"

package_name="$package_scope/cli-$target"
filename="cli-$target-$specific_version.tgz"
url="https://registry.npmjs.org/$package_name/-/$filename"

# Check availability with a HEAD request (a GET would download the tarball twice)
http_status=$(curl -fsSI -o /dev/null -w "%{http_code}" "$url" 2>/dev/null || echo 000)
if [ "$http_status" != "200" ] && [ "$http_status" != "000" ]; then
    echo "Warning: Received HTTP $http_status checking $url, attempting download anyway..." >&2
fi

echo "Downloading OpenCode CLI ($specific_version for $target)..."
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

curl -fsSL "$url" -o "$TMP_DIR/$filename" || wget -q "$url" -O "$TMP_DIR/$filename"

echo "Extracting binary..."
mkdir -p "$TARGET_DIR"
tar -xzf "$TMP_DIR/$filename" -C "$TMP_DIR"

binary_name="opencode"
if [ "$os" = "windows" ]; then
    binary_name="opencode.exe"
fi

if [ ! -f "$TMP_DIR/package/bin/$binary_name" ]; then
    echo "Error: unexpected package layout: $TMP_DIR/package/bin/$binary_name not found" >&2
    exit 1
fi
mv "$TMP_DIR/package/bin/$binary_name" "$TARGET_DIR/$binary_name"
chmod 755 "$TARGET_DIR/$binary_name"

echo "OpenCode CLI ($specific_version) installed successfully to $TARGET_DIR/$binary_name"
