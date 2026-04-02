#!/usr/bin/env bash
set -euo pipefail

REPO="OverloadBlitz/cloudcent-cli"
BINARY="cloudcent"
INSTALL_DIR="/usr/local/bin"

# Detect OS and architecture
detect_platform() {
    local os arch

    os="$(uname -s)"
    arch="$(uname -m)"

    case "$os" in
        Linux)  os="linux" ;;
        Darwin) os="macos" ;;
        *)
            echo "Error: unsupported OS: $os"
            exit 1
            ;;
    esac

    case "$arch" in
        x86_64|amd64)  arch="x86_64" ;;
        arm64|aarch64) arch="aarch64" ;;
        *)
            echo "Error: unsupported architecture: $arch"
            exit 1
            ;;
    esac

    echo "${os}-${arch}"
}

# Get latest release tag from GitHub
get_latest_version() {
    local url="https://api.github.com/repos/${REPO}/releases/latest"
    if command -v curl &>/dev/null; then
        curl -fsSL "$url" | grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/'
    elif command -v wget &>/dev/null; then
        wget -qO- "$url" | grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/'
    else
        echo "Error: curl or wget required"
        exit 1
    fi
}

main() {
    local platform version archive_name url tmp_dir

    platform="$(detect_platform)"
    echo "Detected platform: ${platform}"

    echo "Fetching latest release..."
    version="$(get_latest_version)"
    if [ -z "$version" ]; then
        echo "Error: could not determine latest version. Check https://github.com/${REPO}/releases"
        exit 1
    fi
    echo "Latest version: ${version}"

    archive_name="${BINARY}-${platform}.tar.gz"
    url="https://github.com/${REPO}/releases/download/${version}/${archive_name}"

    tmp_dir="$(mktemp -d)"
    trap 'rm -rf "$tmp_dir"' EXIT

    echo "Downloading ${url}..."
    if command -v curl &>/dev/null; then
        curl -fSL "$url" -o "${tmp_dir}/${archive_name}"
    else
        wget -q "$url" -O "${tmp_dir}/${archive_name}"
    fi

    echo "Extracting..."
    tar -xzf "${tmp_dir}/${archive_name}" -C "$tmp_dir"

    echo "Installing to ${INSTALL_DIR}/${BINARY}..."
    if [ -w "$INSTALL_DIR" ]; then
        mv "${tmp_dir}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
    else
        sudo mv "${tmp_dir}/${BINARY}" "${INSTALL_DIR}/${BINARY}"
    fi
    chmod +x "${INSTALL_DIR}/${BINARY}"

    echo ""
    echo "Done! Run 'cloudcent' to get started."
}

main
