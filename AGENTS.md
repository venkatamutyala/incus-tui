# AGENTS.md — working on incus-tui with AI

incus-tui is a keyboard-driven terminal UI ("k9s/lazydocker") for managing **local Incus VMs**: a
single static Go binary built on Bubble Tea v2 over the Incus v7 Go client. Releases are cosign-signed.

This is the entry point for AI agents. It keeps the always-loaded context small: the three focused
guides below are `@import`ed; deeper, situational docs are plain links you read only when the task
needs them.

## Start here — verify every change

```sh
go build ./... && go vet ./... && go test -race ./... && golangci-lint run ./...
```

UI changes also need a **live** check — rendering bugs (overflow, color bleed, clipping) don't show
in `go test`. See the dev-environment guide ([docs/ai/environment.md](docs/ai/environment.md)) for the
tmux harness.

## Golden rules

- **Keep the seam.** `internal/incus` (service layer) never imports `charm.land/*` (Bubble Tea,
  lipgloss, huh). All UI lives in `internal/tui`.
- **The model is passed by value** through Bubble Tea's `Update`; pointer-receiver helpers mutate the
  local copy, so you must `return m`. There are several more traps like this — read the gotchas guide
  before touching the TUI or the release pipeline.
- **Verify for real, then claim done.** Run the suite; for UI, drive it in tmux and capture frames.
  Lock in every fix with a regression test.
- **No secrets in commits.** Scan the diff. End commits with the `Co-Authored-By:` line. Commit/push
  only when asked.

## Core guides (auto-loaded)

@docs/ai/architecture.md
@docs/ai/conventions.md
@docs/ai/gotchas.md

## Read on demand (not auto-loaded)

- Cutting & verifying a release, the approval gate, the supply-chain policy →
  [docs/ai/release.md](docs/ai/release.md)
- The dev container, starting `incusd`, and the live tmux verify harness →
  [docs/ai/environment.md](docs/ai/environment.md)

## Map

- `cmd/incus-tui/` — entry point
- `internal/incus/` — service layer (Incus v7 client wrapper, no Bubble Tea)
- `internal/tui/` — Bubble Tea v2 UI
- Full map + data flow is in the architecture guide above.
