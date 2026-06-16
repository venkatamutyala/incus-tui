# incus-tui

[![CI](https://github.com/venkatamutyala/incus-tui/actions/workflows/ci.yml/badge.svg)](https://github.com/venkatamutyala/incus-tui/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/venkatamutyala/incus-tui)](https://github.com/venkatamutyala/incus-tui/releases)
[![Go Report Card](https://goreportcard.com/badge/github.com/venkatamutyala/incus-tui)](https://goreportcard.com/report/github.com/venkatamutyala/incus-tui)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A fast, keyboard-driven terminal cockpit for managing local [Incus](https://linuxcontainers.org/incus/)
virtual machines — think `k9s`/`lazydocker`, but for Incus VMs.

One live screen to watch your VMs, act on them with single keystrokes, shell in, and
**launch new VMs from cloud-init templates** without hand-assembling
`incus launch … --config cloud-init.user-data=…` commands.

```
 incus-tui  local · VMs
 NAME             STATUS     IPV4              IMAGE                        AGE   CPU   MEM
 db               Stopped    -                 Ubuntu noble amd64           5m    1     -
 web              Running    10.241.140.224    Ubuntu noble amd64           5m    2     229.5MiB
 2 VM(s)
 ? help • q quit • / filter • ↵ details • n new VM • s shell • l logs • d delete
```

## Features

- **Live instance table** — status, IPv4, image, age, and live CPU/memory, refreshed by
  Incus's event stream (instant status changes) plus a periodic tick (metrics/age).
- **Single-keystroke lifecycle** — start, graceful stop, restart, pause/resume, and a
  **guarded delete** that names the target VM. Long operations can be cancelled with `esc`.
- **Snapshots** — create, **restore**, and delete from a snapshot manager (`p`).
- **Edit CPU/RAM** on an existing VM (`limits.cpu` / `limits.memory`).
- **Copy a VM's IP** to the clipboard (`y`; OSC52, works over SSH).
- **Shell in** (`s`) — runs `incus exec <vm>` (bash, falling back to `sh`), gated on
  guest-agent readiness. The bare binary needs the `incus` CLI on `PATH`; the Docker image
  bundles it.
- **Logs** (`l`) — view a VM's serial console log, and toggle (`c`) to `cloud-init status`
  to see *why* a launch failed.
- **Launch wizard** (`n`) — browse VM-capable images (filtered to your host architecture),
  set size, pick a **cloud-init template**, edit it inline (validated), and launch. Templates
  live in `~/.config/incus-tui/templates` (seeded with starters).
- **Fuzzy filter** (`/`), responsive columns, contextual help bar, and a full cheat sheet (`?`).

The TUI is **storage-agnostic** — it manages whatever Incus is configured with and resolves
the launch disk pool from your default profile.

## Keybindings

| Key | Action | Key | Action |
|-----|--------|-----|--------|
| `↑/k` `↓/j` | move | `n` | new VM (launch wizard) |
| `g` / `G` | top / bottom | `s` | shell into VM |
| `↵` | details | `l` | logs / cloud-init status |
| `esc` / `q` | back / quit | `S` `t` `r` | start / stop / restart |
| `/` | fuzzy filter | `f` | pause / resume |
| `?` | full help | `p` | snapshots (create/restore/delete) |
| `R` | refresh | `e` `y` `d` | edit cpu·ram / copy IP / delete |

In the launch wizard, `ctrl+s` launches from the cloud-init editor and `esc` steps back.
During a long operation, `esc` cancels it.

## Install

Grab a prebuilt static binary (linux `amd64` / `arm64`) from the latest release — pick your arch:

```sh
curl -fsSL https://github.com/venkatamutyala/incus-tui/releases/latest/download/incus-tui_linux_amd64.tar.gz | tar -xz
sudo install incus-tui /usr/local/bin/
```

Or with Go:

```sh
go install github.com/venkatamutyala/incus-tui/cmd/incus-tui@latest
```

> The single binary needs the `incus` CLI on `PATH` only for the shell-in (`s`) action;
> everything else talks to the daemon directly.

## Build & run

Requires Go 1.26+ and a reachable local Incus daemon. It connects to the socket at
`$INCUS_SOCKET` (default `/var/lib/incus/unix.socket`).

```sh
go build -o bin/incus-tui ./cmd/incus-tui
./bin/incus-tui
```

If the daemon isn't reachable, the tool prints a friendly hint pointing at
`scripts/start-incusd.sh`.

### Run with Docker

A multi-arch image is published to GHCR. It talks to Incus over the host's unix socket, so
bind-mount the socket and allocate a TTY:

```sh
docker run -it --rm \
  -v /var/lib/incus/unix.socket:/var/lib/incus/unix.socket \
  ghcr.io/venkatamutyala/incus-tui:latest
```

The image bundles the `incus` client so shell-in (`s`) works, and sets `INCUS_SOCKET`.

> **Security:** the bind-mounted socket is the Incus **admin** API — mounting it grants the
> container host-root-equivalent control of Incus, so only do this on a trusted host. For most
> users the single static binary is simpler and safer — drop it on the Incus host and run it.

## Development environment

Development happens entirely inside a privileged, KVM-capable container (Incus boots
real VMs). There is no systemd, so the daemon is started manually.

```sh
scripts/setup-dev.sh      # install Go + Incus, start incusd, init storage/network, boot-test a VM
scripts/start-incusd.sh   # (re)start the daemon + prep VM devices — run on every container start
```

`scripts/start-incusd.sh` handles the VM-in-container device quirks (group-scoped `/dev/kvm`,
the host-builtin `vhost_vsock` module, recreating `/dev/vsock` and `/dev/vhost-vsock`, the
firewall guard) and starts `incusd` via its bundled wrapper. See
`.devcontainer/devcontainer.json` for the required device passthrough.

## Tests

```sh
go test ./internal/incus/...                       # fast unit tier (no daemon)
go test -tags integration ./internal/incus/...     # live tier: boots a real VM
```

## Architecture

```
cmd/incus-tui/        entry point
internal/incus/       service layer — TUI-agnostic wrapper over the Incus v7 Go client
internal/tui/         Bubble Tea v2 UI (table, detail, launch wizard, forms)
```

The service layer (`internal/incus`) has no Bubble Tea imports and is independently
integration-tested. The UI consumes it via typed messages; all state mutation happens in
the single-threaded `Update` loop. The Incus **event stream** (the live-status path) is
supervised and reconnects automatically with backoff, showing a `⟳` indicator while down.

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Bubbles](https://github.com/charmbracelet/bubbles), and
[huh](https://github.com/charmbracelet/huh) (v2), and the official
[Incus Go client](https://github.com/lxc/incus).

## Scope

**v1 (this release):** local host, VMs only — observe + lifecycle + launch-from-cloud-init.

**Deferred:** clone VM, persistent template-library management, interactive (VGA) console
attach (the `l` viewer already shows read-only console output), file push/pull, multi-project,
profile/network/storage browsers, remote/cluster support, system containers. The service layer
keeps the instance-type and connection behind a single seam so these can grow in later.
