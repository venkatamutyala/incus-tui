#!/bin/sh
#
# incus-tui installer — fetch the latest (or a pinned) release binary and install it.
# Safe to run repeatedly: re-run any time to upgrade to the newest release.
#
#   curl -fsSL https://raw.githubusercontent.com/venkatamutyala/incus-tui/main/install.sh | sh
#
# Environment overrides:
#   INCUS_TUI_VERSION=v0.0.4       install a specific release (default: latest)
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

# --- Verify the download (fail closed) ---------------------------------------
# checksums.txt ships with every release and is REQUIRED: a fetch failure is a hard
# error, never a silent skip.
curl -fSL --proto '=https' "${base}/checksums.txt" -o "${tmp}/checksums.txt" \
	|| err "could not download checksums.txt — refusing to install unverified"

# Authenticity: cosign keyless verification (no key, no login) proves checksums.txt was
# signed by THIS repo's release workflow, which integrity (checksum) alone cannot — an
# attacker who can swap the binary on the release storage can swap checksums.txt too.
# The release is signed into a single Sigstore bundle, which needs cosign v3+ to verify;
# if cosign v3+ is present the signature MUST verify (fail closed), otherwise we fall
# back to integrity only and say so LOUDLY.
cosign_major=""
if command -v cosign >/dev/null 2>&1; then
	cosign_major="$(cosign version 2>/dev/null | sed -n 's/.*GitVersion:[[:space:]]*v\{0,1\}\([0-9][0-9]*\).*/\1/p' | head -1)"
fi
if [ -n "$cosign_major" ] && [ "$cosign_major" -ge 3 ] 2>/dev/null; then
	curl -fSL --proto '=https' "${base}/checksums.txt.bundle" -o "${tmp}/checksums.txt.bundle" \
		|| err "cosign is installed but checksums.txt.bundle is missing — refusing to install"
	# Bind to this repo's release.yml at a v* tag. Dots are escaped so they match
	# literally rather than any character.
	cert_id_re="^https://github\.com/${REPO}/\.github/workflows/release\.yml@refs/tags/v.+\$"
	cosign verify-blob "${tmp}/checksums.txt" \
		--bundle "${tmp}/checksums.txt.bundle" \
		--certificate-identity-regexp "$cert_id_re" \
		--certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
		>/dev/null 2>&1 || err "cosign signature verification failed — refusing to install"
	echo "✓ Signature verified (cosign keyless bundle — built by ${REPO} CI)."
else
	echo "install: WARNING — cosign v3+ not found; skipping SIGNATURE verification." >&2
	echo "install:   The checksum below proves integrity (no corruption in transit) but" >&2
	echo "install:   NOT authenticity: it cannot distinguish a genuine release from a" >&2
	echo "install:   tampered one if the release storage itself is compromised." >&2
	echo "install:   For full verification, install cosign v3+ and re-run, or verify by hand:" >&2
	echo "install:   https://github.com/${REPO}#verify-a-download" >&2
fi

# Integrity: the archive MUST match the (now-possibly-signed) checksums. sha256sum is
# required; if it is missing we cannot verify and refuse rather than install blind.
command -v sha256sum >/dev/null 2>&1 || err "sha256sum is required to verify the download"
want="$(awk -v f="$asset" '$2 == f {print $1}' "${tmp}/checksums.txt")"
[ -n "$want" ] || err "${asset} is not listed in checksums.txt — refusing to install"
got="$(sha256sum "${tmp}/${asset}" | awk '{print $1}')"
[ "$want" = "$got" ] || err "checksum mismatch for ${asset} — refusing to install"
echo "✓ Checksum OK."

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
