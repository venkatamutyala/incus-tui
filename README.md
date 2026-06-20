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
 NAME             STATUS       IPV4              IMAGE                      AGE   CPU   MEM
 db               ○ Stopped    -                 Ubuntu noble amd64         5m    1     -
 web              ● Running    10.241.140.224    Ubuntu noble amd64         5m    2     11%
 2 VM(s)
 ? help • q quit • / filter • ↵ details • n new VM • s shell • l logs • d delete
```

## Features

- **Live instance table** — at-a-glance status (a colored glyph: `●` running, `○` stopped,
  `◐` frozen, `✗` error), IPv4, image, age, and live CPU / memory %, refreshed by Incus's
  event stream (instant status changes) plus a periodic tick (metrics/age).
- **Single-keystroke lifecycle** — start, graceful stop, restart, pause/resume, and a
  **guarded delete** that names the target VM and warns when it will be force-stopped. Long
  operations can be cancelled with `esc`.
- **Snapshots** — create, **restore**, and delete from a snapshot manager (`p`).
- **Edit CPU/RAM** on an existing VM (`limits.cpu` / `limits.memory`).
- **Grow a storage pool** (`P`) — when a loop-backed pool (btrfs/zfs/lvm on a file) is filling
  up, resize it in place from a form that shows each pool's live usage. Pools can only grow,
  never shrink.
- **Copy a VM's IP** to the clipboard (`y`; OSC52, works over SSH).
- **Shell in** (`s`) — runs `incus exec <vm>` (bash, falling back to `sh`), gated on
  guest-agent readiness. The bare binary needs the `incus` CLI on `PATH`; the Docker image
  bundles it.
- **Logs** (`l`) — view a VM's serial console log, toggle (`c`) to `cloud-init status` to see
  *why* a launch failed, and **auto-refresh** to tail it live (on by default; toggle with `a`).
- **Launch wizard** (`n`) — browse VM-capable images (filtered to your host architecture),
  set size, pick a **cloud-init template**, edit it inline (validated), and launch. Templates
  live in `~/.config/incus-tui/templates` (seeded with starters).
- **Fuzzy filter** (`/`), responsive columns, contextual help bar, and a full cheat sheet (`?`).

The TUI is **storage-agnostic** — it manages whatever Incus is configured with, resolves the
launch disk pool from your default profile, and can **grow a loop-backed pool** in place (`P`)
when it runs low on space.

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
| | | `P` | resize (grow) a storage pool |

In the launch wizard, `ctrl+s` launches from the cloud-init editor and `esc` steps back.
During a long operation, `esc` cancels it.

## Install

Install (or upgrade) the latest release in one line — safe to re-run any time to update:

```sh
curl -fsSL https://raw.githubusercontent.com/venkatamutyala/incus-tui/main/install.sh | sh
```

The script ([`install.sh`](install.sh)) detects your architecture (linux `amd64`/`arm64`),
verifies the download, and installs to `/usr/local/bin`. It is **fail-closed**: it always
checks the SHA-256 checksum and refuses to install on any mismatch or missing checksum. If
[`cosign`](https://github.com/sigstore/cosign) **v3+** is installed it *also* verifies the
release signature (authenticity — see [Verify a download](#verify-a-download)); if not, it
prints a warning that it can confirm only integrity, not authenticity, and continues. Override with
`INSTALL_DIR=…` or pin a release with `INCUS_TUI_VERSION=v0.0.4`. It needs `curl`, `tar`,
`sha256sum`, and `sudo` (for `/usr/local/bin`).

Prefer to do it by hand, or use Go?

```sh
# manual download (pick amd64 or arm64)
curl -fsSL https://github.com/venkatamutyala/incus-tui/releases/latest/download/incus-tui_linux_amd64.tar.gz | tar -xz
sudo install incus-tui /usr/local/bin/

# or with Go
go install github.com/venkatamutyala/incus-tui/cmd/incus-tui@latest
```

> The single binary needs the `incus` CLI on `PATH` only for the shell-in (`s`) action;
> everything else talks to the daemon directly.

## Verify a download

Releases are **keyless-signed** (cosign) and carry **SLSA build provenance** — no maintainer
key; the signature is bound to this repo's release workflow identity and logged in Sigstore's
public transparency log. So you can confirm a download was built by *this* repo's CI, independent
of GitHub's release storage. The signature ships as a single **Sigstore bundle**
(`checksums.txt.bundle`, holding the signature + Fulcio cert + transparency-log entry), which
needs **cosign v3+** to verify.

**What the one-liner guarantees:** `install.sh` always verifies the SHA-256 checksum and aborts
on a mismatch (**integrity**). It verifies the cosign signature (**authenticity**) *only if
`cosign` v3+ is installed* — otherwise it warns that it cannot prove authenticity and continues on
the checksum alone. The checksum and the binary live in the same release, so a checksum match alone
does **not** prove a release wasn't tampered with at the source; only the signature does. For an
authenticity guarantee, install `cosign` v3+ before running the one-liner, or verify by hand below.

```sh
# Fetch the archive + the signed checksums and its Sigstore bundle.
v=v0.0.4; a=incus-tui_linux_amd64.tar.gz
base=https://github.com/venkatamutyala/incus-tui/releases/download/$v
curl -fsSLO "$base/$a"
curl -fsSLO "$base/checksums.txt"
curl -fsSLO "$base/checksums.txt.bundle"

# cosign v3+ — no key, no login. Proves checksums.txt was signed by this repo's release.yml.
cosign verify-blob checksums.txt \
  --bundle checksums.txt.bundle \
  --certificate-identity-regexp '^https://github\.com/venkatamutyala/incus-tui/\.github/workflows/release\.yml@refs/tags/v.+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

# Then confirm your archive matches the now-verified checksums.
sha256sum --check --ignore-missing checksums.txt

# or SLSA provenance via gh (needs `gh auth login`, even for a public repo):
gh attestation verify "$a" --repo venkatamutyala/incus-tui \
  --signer-workflow venkatamutyala/incus-tui/.github/workflows/release.yml

# the multi-arch image is attested too:
gh attestation verify oci://ghcr.io/venkatamutyala/incus-tui:latest --repo venkatamutyala/incus-tui
```

> Why not just the checksum? `checksums.txt` lives in the same release as the binary, so whoever
> can tamper with one can tamper with both — it proves *integrity*, not *authenticity*. The
> signature's trust comes from a key no attacker holds, independent of where the file is hosted.

## Build & run

Requires Go 1.26+ and a reachable local Incus daemon. It connects over the local Incus
unix socket — `$INCUS_SOCKET` if set, otherwise the daemon's default
(`/run/incus/unix.socket`, falling back to `/var/lib/incus/unix.socket`).

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

## Contributing (incl. with AI)

Working on this codebase — by hand or with an AI agent — starts at [`AGENTS.md`](AGENTS.md): a lean
guide that imports focused docs under [`docs/ai/`](docs/ai/) (architecture, conventions, the gotchas
that have actually bitten this code, the release process, and the live-verification harness).
`CLAUDE.md` just points there.

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
