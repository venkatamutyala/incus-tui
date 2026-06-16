#!/usr/bin/env bash
#
# start-incusd.sh — idempotently prepare VM devices and start the Incus daemon.
#
# This container has NO systemd, so the zabbly package's systemd unit never runs.
# We start incusd manually via its wrapper (which sets LD_LIBRARY_PATH + the bundled
# qemu/edk2 paths) after wiring up the VM devices the host exposes but doesn't fully
# surface into the container. Safe to run repeatedly (e.g. from a devcontainer
# postStartCommand, since the daemon does not persist across container restarts).
#
set -euo pipefail
log() { printf '\033[1;36m==>\033[0m %s\n' "$*"; }

INCUSD_WRAPPER=/opt/incus/lib/systemd/incusd
# Socket is owned by this group so the unprivileged dev user can reach it without sudo.
SOCKET_GROUP="${INCUS_SOCKET_GROUP:-$(id -gn)}"
KREL="$(uname -r)"

# dev_perms group-scopes a device to the dev user's group (rw) instead of granting
# world access — incusd/qemu run as root and can still use it.
dev_perms() { sudo chgrp "$SOCKET_GROUP" "$1" 2>/dev/null || true; sudo chmod 0660 "$1" 2>/dev/null || true; }

# 1. /dev/kvm (ships root:... 0660): incusd runs as root so it can already open it;
#    just make it group-accessible to the dev user rather than world-writable.
[ -e /dev/kvm ] && dev_perms /dev/kvm

# 2. vhost_vsock: it's builtin on the host kernel, but Incus's capability check does
#    `modprobe -b vhost_vsock` (after checking /sys/module). With no /lib/modules tree
#    that fails, so Incus marks the VM type "not operational". Make modprobe treat the
#    module as builtin so the check passes. (Dev-container shim; harmless on a host with
#    a real /lib/modules tree because the early /sys/module guard skips it.)
if [ ! -e /sys/module/vhost_vsock ] && ! grep -qw vhost_vsock /proc/modules 2>/dev/null; then
  sudo mkdir -p "/lib/modules/$KREL"
  sudo touch "/lib/modules/$KREL/modules.order"
  printf 'kernel/drivers/vhost/vhost.ko\nkernel/drivers/vhost/vhost_vsock.ko\nkernel/net/vmw_vsock/vsock.ko\n' \
    | sudo tee "/lib/modules/$KREL/modules.builtin" >/dev/null
  sudo depmod "$KREL" 2>/dev/null || true
fi

# 3. /dev/vsock and /dev/vhost-vsock: the host registers these misc devices but their
#    nodes aren't always passed into the container. Recreate from the host's misc minor
#    (needs cap_mknod). VMs need BOTH; warn if a minor can't be found.
make_misc_node() {
  local name="$1" path="$2"
  [ -e "$path" ] && { dev_perms "$path"; return; }
  local minor; minor="$(awk -v n="$name" '$2==n{print $1}' /proc/misc)"
  if [ -n "$minor" ]; then
    sudo mknod "$path" c 10 "$minor" && dev_perms "$path"
  else
    log "WARNING: $name not in /proc/misc; VM boot may fail without $path"
  fi
}
make_misc_node vsock /dev/vsock
make_misc_node vhost-vsock /dev/vhost-vsock

# 4. Outbound networking for VMs: enable forwarding; ACCEPT in DOCKER-USER only if that
#    chain exists (it's absent inside a container — Incus's own incusbr0 rules suffice).
sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
if sudo iptables -L DOCKER-USER -n >/dev/null 2>&1; then
  sudo iptables -C DOCKER-USER -j ACCEPT 2>/dev/null || sudo iptables -I DOCKER-USER -j ACCEPT
fi

# 5. Start the daemon if it isn't already answering.
if incus admin waitready --timeout=2 >/dev/null 2>&1; then
  log "incusd already running"
  exit 0
fi
log "starting incusd (socket group: $SOCKET_GROUP)"
sudo sh -c "mkdir -p /var/log/incus; nohup '$INCUSD_WRAPPER' --group '$SOCKET_GROUP' \
  >/var/log/incus/incusd.boot.log 2>&1 & echo \$! >/run/incusd.pid"
incus admin waitready --timeout=60
log "incusd ready (socket: /var/lib/incus/unix.socket)"
