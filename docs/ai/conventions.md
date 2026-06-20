# Conventions

## Verify every change — run these before calling it done

```sh
go build ./... && go vet ./...
go test -race ./...                 # unit tier (no daemon needed)
golangci-lint run ./...             # expect "0 issues"
govulncheck ./...                   # expect "No vulnerabilities found"
goreleaser check                    # release config validates
actionlint                          # workflows
shellcheck install.sh scripts/*.sh  # shell
```

For UI changes, `go test` is **not** enough — rendering bugs (overflow, color bleed, clipping)
only show up on a real terminal. Drive the TUI live in tmux against a running daemon; see the
[dev environment guide](environment.md).

## Code style

- Match the surrounding code: comment density, naming, idiom. This codebase favors short, dense,
  well-commented functions where comments explain the **why** (the non-obvious), not the what.
- Wrap errors with `%w` and context: `fmt.Errorf("doing X for %q: %w", name, err)`.
- Keep `internal/incus` free of any `charm.land/*` import (the service/UI seam).
- Prefer the dedicated tools over shell when one fits; reference code as `file:line`.

## Common changes (recipes)

**Add a single-key VM action.** Bindings are a dense namespace — check `defaultKeys()` (keys.go) for
a free letter first (lowercase = the common action, uppercase = a stronger variant; the table's
paging keys are deliberately restricted in `New()` so action letters don't leak into navigation).
Then:
1. Add the `key.Binding` to the `keyMap` struct **and** `defaultKeys()` (keys.go).
2. Add a `key.Matches(k, m.keys.X)` case in `handleAction` (model.go) — this auto-wires it into
   **both** the list and detail views.
3. Resolve the target VM with `m.activeVM()` (not `m.current()`) so it works from list and detail/logs.
4. Add the binding to `ShortHelp` and/or `FullHelp` (keys.go) so it shows in the cheat sheet; update
   the detail-mode hint string in `bottomBar()` (view.go) if you want it advertised there too.
5. For a plain lifecycle op reuse `m.actionOp("x", (*xincus.Client).X)`; otherwise wrap the call in
   `m.busy(...)` (cancelable, reloads on completion).

**Add a table column.** Edit `allCols()` **and** append the column's title to `colDropOrder` (both in
view.go), then update the column-count assertions in `TestVisibleColsDropsAndStaysAligned` and
`TestSyncTableResizeShrinkNoPanic`. (A column missing from `colDropOrder` overflows narrow terminals —
see the gotchas guide.)

## Tests

- Service layer: daemon-free unit tests in `internal/incus/*_test.go`; live `-tags integration`
  tests boot a real VM.
- TUI: daemon-free tests for the pure helpers (formatting, validators, layout math, the
  launch-form → `CreateSpec` mapping). **Lock in every bug fix with a regression test** — several
  existing tests were written to fail before their fix and pass after.

## Commits & PRs

- Commit or push **only when asked**. If you're on the default branch and it's not a release, branch first.
- End commit messages with the agent's `Co-Authored-By:` line.
- **Never commit secrets.** Scan the diff before committing.
- Releases are the exception: they're cut from `main` by tag (see the [release guide](release.md)).
