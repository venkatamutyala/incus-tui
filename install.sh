#!/bin/sh
#
# incus-tui installer — fetch the latest (or a pinned) release binary and install it.
# Safe to run repeatedly: re-run any time to upgrade to the newest release.
#
#   curl -fsSL https://raw.githubusercontent.com/venkatamutyala/incus-tui/main/install.sh | sh
#
# Environment overrides:
#   INCUS_TUI_VERSION=v0.0.1       install a specific release (default: latest)
#   INSTALL_DIR=$HOME/.local/bin   install location (default: /usr/local/bin)
#
set -eu

REPO="venkatamutyala/incus-tui"
BIN="incus-tui"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
# Prefixed name so a stray VERSION in the caller's shell can't redirect the download.
VERSION="${INCUS_TUI_VERSION:-latest}"

err() { echo "install: $*" >&2; exit 1; }

[ "$(uname -s)" = "Linux" ] || err "only Linux is supported (got $(uname -s))"
case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) err "unsupported architecture: $(uname -m) (need x86_64 or aarch64)" ;;
esac

command -v curl >/dev/null 2>&1 || err "curl is required"
command -v tar >/dev/null 2>&1 || err "tar is required"

asset="${BIN}_linux_${arch}.tar.gz"
if [ "$VERSION" = "latest" ]; then
	base="https://github.com/${REPO}/releases/latest/download"
else
	base="https://github.com/${REPO}/releases/download/${VERSION}"
fi

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading ${asset} (${VERSION})…"
curl -fSL --proto '=https' "${base}/${asset}" -o "${tmp}/${asset}" \
	|| err "download failed: ${base}/${asset}"

# Verify the checksum when sha256sum and the checksums file are both available.
if command -v sha256sum >/dev/null 2>&1 \
	&& curl -fsSL "${base}/checksums.txt" -o "${tmp}/checksums.txt" 2>/dev/null; then
	want="$(awk -v f="$asset" '$2 == f {print $1}' "${tmp}/checksums.txt")"
	if [ -n "$want" ]; then
		got="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
		[ "$want" = "$got" ] || err "checksum mismatch for ${asset}"
		echo "Checksum OK."
	fi
fi

tar -xzf "${tmp}/${asset}" -C "${tmp}"
[ -f "${tmp}/${BIN}" ] || err "archive did not contain ${BIN}"

sudo=""
if [ ! -w "$INSTALL_DIR" ]; then
	if command -v sudo >/dev/null 2>&1; then
		sudo="sudo"
	else
		err "no write access to ${INSTALL_DIR}; re-run as root or set INSTALL_DIR"
	fi
fi
$sudo install -d "$INSTALL_DIR"
$sudo install -m 0755 "${tmp}/${BIN}" "${INSTALL_DIR}/${BIN}"

echo "Installed $("${INSTALL_DIR}/${BIN}" --version 2>/dev/null || echo "$BIN") → ${INSTALL_DIR}/${BIN}"
case ":${PATH}:" in
	*":${INSTALL_DIR}:"*) ;;
	*) echo "note: ${INSTALL_DIR} is not on your PATH" ;;
esac
