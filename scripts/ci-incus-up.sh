#!/usr/bin/env bash
#
# ci-incus-up.sh — install and initialize Incus on a GitHub-hosted ubuntu runner
# (which HAS systemd) for the VM-booting integration tests. Run as root (via sudo).
#
# This is distinct from scripts/start-incusd.sh, which targets the no-systemd dev
# container and applies VM-in-container device workarounds not needed on a runner.
#
set -euo pipefail
log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }

# Without KVM the integration test skips itself, so do nothing and exit cleanly rather
# than failing the job.
if [ ! -e /dev/kvm ]; then
	log "no /dev/kvm on this runner — skipping Incus setup (integration test will skip)"
	exit 0
fi

log "adding zabbly Incus repo + installing incus (bundles its own qemu/edk2)"
install -d /etc/apt/keyrings
curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc
. /etc/os-release
cat > /etc/apt/sources.list.d/zabbly-incus-stable.sources <<EOF
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/stable
Suites: ${VERSION_CODENAME}
Components: main
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/zabbly.asc
EOF
apt-get update -y -o Acquire::Retries=3
DEBIAN_FRONTEND=noninteractive apt-get install -y -o Acquire::Retries=3 incus

log "starting daemon (systemd) and waiting until ready"
systemctl enable --now incus.service
incus admin waitready --timeout=120

if incus storage show default >/dev/null 2>&1; then
	log "incus already initialized"
else
	log "initializing storage (dir) + NAT bridge"
	incus admin init --preseed <<'EOF'
config: {}
networks:
- name: incusbr0
  type: bridge
  config: {ipv4.address: auto, ipv4.nat: "true", ipv6.address: none}
storage_pools:
- name: default
  driver: dir
profiles:
- name: default
  devices:
    root: {path: /, pool: default, type: disk}
    eth0: {name: eth0, network: incusbr0, type: nic}
EOF
fi

# Open the socket to the (non-root) test runner. World-writable is acceptable ONLY
# because this is an ephemeral, single-tenant CI runner; start-incusd.sh uses the
# group-based (--group) approach for any longer-lived host.
chmod 0666 /var/lib/incus/unix.socket

incus version
log "Incus ready for integration tests"
