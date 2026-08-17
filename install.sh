#!/bin/sh
# Install the latest relayflow release binary into ~/.local/bin.
# Usage: curl -fsSL https://raw.githubusercontent.com/rajpopat27/relayflow/main/install.sh | sh
set -e

REPO="rajpopat27/relayflow"
DEST="${RELAYFLOW_INSTALL_DIR:-$HOME/.local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"   # linux | darwin
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')"
[ -z "$VERSION" ] && { echo "could not resolve latest release" >&2; exit 1; }

URL="https://github.com/$REPO/releases/download/v${VERSION}/relayflow_${OS}_${ARCH}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "downloading relayflow v${VERSION} (${OS}/${ARCH})"
curl -fsSL "$URL" -o "$TMP/relayflow.tar.gz"
tar -xzf "$TMP/relayflow.tar.gz" -C "$TMP"

mkdir -p "$DEST"
install -m 755 "$TMP/relayflow" "$DEST/relayflow"
echo "installed to $DEST/relayflow"
case ":$PATH:" in *":$DEST:"*) ;; *) echo "note: add $DEST to your PATH" ;; esac
