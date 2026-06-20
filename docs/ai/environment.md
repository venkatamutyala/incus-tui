# Development environment

Development happens inside a privileged, KVM-capable container — Incus boots **real VMs**. There is
no systemd, so the daemon is started manually.

## Bring up the daemon

```sh
scripts/setup-dev.sh      # first time: install Go + Incus, start incusd, init storage/network, boot-test a VM
scripts/start-incusd.sh   # every container start: (re)start incusd + prep the VM devices
```

`start-incusd.sh` handles the VM-in-container quirks (group-scoped `/dev/kvm`, the host-builtin
`vhost_vsock` module, recreating `/dev/vsock` & `/dev/vhost-vsock`, the firewall guard). The TUI and
the `incus` CLI talk to the daemon over `$INCUS_SOCKET`; in this dev container it's
`/var/lib/incus/unix.socket`.

## Build & run

```sh
go build -o bin/incus-tui ./cmd/incus-tui
INCUS_SOCKET=/var/lib/incus/unix.socket ./bin/incus-tui
```

## Verify UI changes live (the tmux harness)

Rendering bugs don't show up in `go test`. Drive the real TUI in a detached tmux session and capture
frames:

```sh
S=verify
tmux new-session -d -s "$S" -x 100 -y 30
tmux set-option -t "$S" window-size manual; tmux resize-window -t "$S" -x 100 -y 30
tmux send-keys -t "$S" "INCUS_SOCKET=/var/lib/incus/unix.socket ./bin/incus-tui" Enter; sleep 3
tmux send-keys -t "$S" "n"; sleep 4            # e.g. open the launch wizard
tmux capture-pane -t "$S" -p                    # plain text frame (read the layout)
tmux capture-pane -t "$S" -p -e                 # with ANSI escapes (check colors / the cursor-row highlight)
tmux kill-session -t "$S"
```

- **Test edge sizes** — most rendering bugs live at narrow/short terminals (`-x 30`, `-y 8`). The
  `window-size manual` + `resize-window` is required, otherwise tmux ignores `-x/-y`.
- **Need a VM in the table?** `incus init <cached-vm-image-fp> tmp --vm` makes a stopped VM fast
  (find a fingerprint with `incus image list type=virtual-machine --format csv -c f`); `incus start tmp`
  boots it. Clean up with `incus delete --force tmp`.
- To inspect a specific cell's styling, `capture-pane -p -e | grep <name> | cat -v` shows the raw
  escape codes (this is how the status-cell highlight regression was caught).

## Tests

```sh
go test ./internal/incus/...                    # fast unit tier (no daemon)
go test -tags integration ./internal/incus/...  # live tier: boots a real VM (needs the daemon up)
```
