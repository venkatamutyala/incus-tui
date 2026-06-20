# Architecture

incus-tui is a keyboard-driven terminal UI for managing **local Incus virtual machines** —
"k9s/lazydocker for Incus VMs". One screen lists VMs and acts on them with single keystrokes;
a wizard launches new VMs from cloud-init templates.

## Layers — the one seam that matters

```
cmd/incus-tui/      entry point: connect to the daemon, run the Bubble Tea program
internal/incus/     SERVICE layer — wraps the Incus v7 Go client. NO Bubble Tea imports.
internal/tui/       UI layer — Bubble Tea v2. Consumes the service layer via typed messages.
```

**Keep the seam clean:** `internal/incus` must never import `charm.land/*` (Bubble Tea, bubbles,
lipgloss, huh). It exposes flattened value types (`VM`, `Image`, `Template`, `CreateSpec`) so the
UI never touches the raw Incus API and the service layer stays independently integration-testable.

## internal/incus (service)

- `client.go` — `Connect()` (unix socket), `waitOp(ctx, op)` (ctx-cancelable op wait that also
  cancels the server-side op), `Disconnect()`. The lazy image-server cache is guarded by a mutex.
- `instances.go` — `ListVMs`/`GetVM` (+ `toVM` flattening), lifecycle (`Start`/`Stop`/`ForceStop`/
  `Restart`/`Freeze`/`Unfreeze`), `Delete` (force-stops a non-stopped VM first), `SetLimits` (etag
  read-modify-write), snapshots (`Snapshot`/`RestoreSnapshot`/`DeleteSnapshot`), `CreateVM`
  (resolves the root disk pool from the default profile — storage-agnostic), `hostArch`/`normalizeArch`.
- `images.go` — `ListVMImages`: host-arch filter + dedup of daily-build serials to one entry per
  product (os/release/variant/arch), keeping the newest; cloud variants sorted first.
- `templates.go` — cloud-init starter templates seeded to `~/.config/incus-tui/templates`
  (additively, by filename); the `# name:` display-name override; `ValidateCloudInit`.
- `logs.go` — `ConsoleLog` (serial-console snapshot) and `CloudInitStatus` (exec `cloud-init status --long`).
- `events.go` — the supervised Incus event stream (auto-reconnect with backoff). `WatchEvents`
  runs in a goroutine and pushes `Event`s onto a channel the UI reads.

## internal/tui (UI)

- `model.go` — the root Bubble Tea `model` and the `Update` loop. Modes: list / detail / form /
  launch-editor / logs / busy. **The model is passed BY VALUE through `Update`** (see gotchas).
  `layout()` sizes every component to the terminal; `helpRows()` reserves the help-bar height.
- `view.go` — `View()` (a `JoinVertical` of header / body / status / help), the responsive table
  columns (`allCols`/`visibleCols`), and detail rendering.
- `forms.go` — huh forms (launch wizard, edit cpu/ram, snapshot manager, delete confirm), the
  validators, and the size helpers `withUnit` / `normalizeMem`.
- `keys.go` — the keymap. `styles.go` — lipgloss styles + cell formatters. `messages.go` — the
  `tea.Cmd` constructors and message types.

## Data flow

1. `New(client)` builds the model; `Init()` starts the event-stream goroutine, the first VM load,
   and a 3-second tick.
2. Both the event stream and the tick refresh the VM list (`loadVMs` → `vmsMsg`); a `loadingVMs`
   guard avoids piling up loads on a slow daemon.
3. Key presses route through `handleKey` → a mode-specific handler → a service-layer call wrapped
   in `busy()` (cancelable via `esc`, backstopped at 15 min) → `opDoneMsg` → toast + reload.
