#!/usr/bin/env bash
#
# setup-dev.sh — one-time dev environment bootstrap for incus-tui.
#
# Target: Ubuntu 22.04, no systemd, privileged + KVM-capable container
# (/dev/kvm, /dev/vhost-vsock present; full caps; passwordless sudo).
# Installs Go + Incus, starts the daemon, initializes storage/network, and
# (by default) boots a throwaway VM as a self-test. Idempotent.
#
#   ./scripts/setup-dev.sh              # full setup + VM self-test
#   SKIP_VM_CHECK=1 ./scripts/setup-dev.sh   # skip the boot self-test
#
set -euo pipefail
log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }
HERE="$(cd "$(dirname "$0")" && pwd)"
GO_VERSION="${GO_VERSION:-1.26.4}"

# --- Go toolchain (pinned) ---
if ! /usr/local/bin/go version 2>/dev/null | grep -q "go${GO_VERSION}"; then
  log "installing Go ${GO_VERSION}"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tgz
  sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz
  sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go
  sudo ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi
/usr/local/bin/go version

# --- Incus (zabbly repo). Incus bundles its own qemu + edk2/OVMF, so no distro qemu/ovmf. ---
if ! command -v incus >/dev/null; then
  log "adding zabbly Incus apt repo + installing incus"
  sudo mkdir -p /etc/apt/keyrings
  sudo curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc
  . /etc/os-release
  sudo tee /etc/apt/sources.list.d/zabbly-incus-stable.sources >/dev/null <<EOF
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/stable
Suites: ${VERSION_CODENAME}
Components: main
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/zabbly.asc
EOF
  sudo apt-get update -y -o Acquire::Retries=3
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y incus incus-client
fi
incus version 2>/dev/null || true

# --- prepare VM devices + start the daemon (no systemd) ---
"$HERE/start-incusd.sh"

# --- initialize storage + network (idempotent) ---
if incus storage show default >/dev/null 2>&1; then
  log "incus already initialized"
else
  # btrfs gives cheap CoW snapshots; fall back to dir when the host kernel lacks btrfs.
  if grep -qw btrfs /proc/filesystems && [ -e /dev/loop-control ]; then DRV=btrfs; else DRV=dir; fi
  log "initializing incus (storage driver: $DRV)"
  {
    echo "config: {}"
    echo "networks:"
    echo "- name: incusbr0"
    echo "  type: bridge"
    echo "  config: {ipv4.address: auto, ipv4.nat: \"true\", ipv6.address: none}"
    echo "storage_pools:"
    echo "- name: default"
    echo "  driver: $DRV"
    [ "$DRV" = btrfs ] && echo "  config: {size: 25GiB}"
    echo "profiles:"
    echo "- name: default"
    echo "  devices:"
    echo "    root: {path: /, pool: default, type: disk}"
    echo "    eth0: {name: eth0, network: incusbr0, type: nic}"
  } | incus admin init --preseed
fi
incus storage list

# --- optional VM boot self-test ---
if [ "${SKIP_VM_CHECK:-0}" != "1" ]; then
  log "self-test: booting a throwaway VM (images:ubuntu/24.04/cloud)"
  incus delete -f _selftest >/dev/null 2>&1 || true
  incus launch images:ubuntu/24.04/cloud _selftest --vm -c security.secureboot=false
  for _ in $(seq 1 36); do incus exec _selftest -- true 2>/dev/null && break; sleep 5; done
  incus exec _selftest -- cloud-init status --wait || true
  incus list _selftest -c ns4t
  incus delete -f _selftest
  log "self-test PASSED: VM booted, cloud-init done, deleted"
fi

log "DONE — Incus ready at /var/lib/incus/unix.socket (socket group: $(id -gn))"
