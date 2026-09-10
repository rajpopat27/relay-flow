#!/bin/sh
# Install the latest relay-flow release binary into ~/.local/bin.
# Usage: curl -fsSL https://raw.githubusercontent.com/rajpopat27/relay-flow/main/install.sh | sh
set -e

REPO="rajpopat27/relay-flow"
DEST="${RELAY_FLOW_INSTALL_DIR:-$HOME/.local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"   # linux | darwin
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 1 ;;
esac

VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')"
[ -z "$VERSION" ] && { echo "could not resolve latest release" >&2; exit 1; }

URL="https://github.com/$REPO/releases/download/v${VERSION}/relay-flow_${OS}_${ARCH}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "downloading relay-flow v${VERSION} (${OS}/${ARCH})"
curl -fsSL "$URL" -o "$TMP/relay-flow.tar.gz"
tar -xzf "$TMP/relay-flow.tar.gz" -C "$TMP"

mkdir -p "$DEST"
install -m 755 "$TMP/relay-flow" "$DEST/relay-flow"
echo "installed to $DEST/relay-flow"
case ":$PATH:" in *":$DEST:"*) ;; *) echo "note: add $DEST to your PATH" ;; esac
