#!/bin/sh
# install.sh — fetch a published thicket release and drop it on $PATH.
#
# Usage:
#   curl -fsSL https://github.com/uribrecher/thicket/releases/latest/download/install.sh | sh
#
# Optional env vars:
#   THICKET_VERSION  pin a specific tag, e.g. v0.2.1. Default: latest.
#   INSTALL_DIR      target dir. Default: $HOME/.local/bin (no sudo).
#                    Set to /usr/local/bin if you want a system-wide install.

set -eu

REPO="uribrecher/thicket"
VERSION="${THICKET_VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

# ---- OS / arch detection ----
os="$(uname -s)"
case "$os" in
	Darwin) OS=darwin ;;
	Linux)  OS=linux ;;
	*) echo "thicket: unsupported OS: $os (only darwin/linux are published)" >&2; exit 1 ;;
esac
arch="$(uname -m)"
case "$arch" in
	x86_64|amd64)   ARCH=amd64 ;;
	arm64|aarch64)  ARCH=arm64 ;;
	*) echo "thicket: unsupported arch: $arch" >&2; exit 1 ;;
esac

# ---- pick required tool: prefer curl, fall back to wget ----
if command -v curl >/dev/null 2>&1; then
	DL='curl -fsSL'
elif command -v wget >/dev/null 2>&1; then
	DL='wget -qO-'
else
	echo "thicket: need curl or wget on PATH" >&2; exit 1
fi

# ---- resolve VERSION → tag ----
# Use the GitHub REST API (works with both curl and wget; same path
# whether DL is curl -fsSL or wget -qO-).
if [ "$VERSION" = "latest" ]; then
	api_json="$($DL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null || true)"
	# Parse "tag_name": "vX.Y.Z" without depending on jq.
	VERSION="$(printf '%s' "$api_json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -1)"
fi
if [ -z "$VERSION" ] || ! echo "$VERSION" | grep -Eq '^v[0-9]'; then
	echo "thicket: could not resolve latest release (try setting THICKET_VERSION=vX.Y.Z)" >&2
	exit 1
fi
SEMVER="${VERSION#v}"

# ---- download tarball + checksums ----
base="https://github.com/$REPO/releases/download/$VERSION"
tarball="thicket_${SEMVER}_${OS}_${ARCH}.tar.gz"
checks="checksums.txt"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "thicket: downloading $VERSION for $OS/$ARCH"
$DL "$base/$tarball" > "$tmp/$tarball" || {
	echo "thicket: failed to download $base/$tarball" >&2; exit 1; }
$DL "$base/$checks" > "$tmp/$checks" || {
	echo "thicket: failed to download $base/$checks" >&2; exit 1; }

# ---- verify checksum ----
if command -v shasum >/dev/null 2>&1; then
	hashcmd='shasum -a 256'
elif command -v sha256sum >/dev/null 2>&1; then
	hashcmd='sha256sum'
else
	echo "thicket: warning — neither shasum nor sha256sum on PATH; skipping verification" >&2
	hashcmd=''
fi
if [ -n "$hashcmd" ]; then
	expected="$(grep "  $tarball$" "$tmp/$checks" | awk '{print $1}')"
	actual="$(cd "$tmp" && $hashcmd "$tarball" | awk '{print $1}')"
	if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
		echo "thicket: checksum mismatch (expected=$expected actual=$actual)" >&2
		exit 1
	fi
fi

# ---- extract + install ----
tar -xzf "$tmp/$tarball" -C "$tmp"
mkdir -p "$INSTALL_DIR"
mv "$tmp/thicket" "$INSTALL_DIR/thicket"
chmod +x "$INSTALL_DIR/thicket"

echo "thicket: installed $VERSION to $INSTALL_DIR/thicket"
case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*) printf '\nthicket: %s is not on $PATH yet. Add this to your shell rc:\n  export PATH="%s:$PATH"\n' \
		"$INSTALL_DIR" "$INSTALL_DIR" ;;
esac
