# Gotchas

The subtle traps this codebase has actually hit. Re-introducing any of these is a regression —
each was found by a review or a live test, not by `go build`.

## Bubble Tea v2 — the value-receiver model

- `Update` and `View` take the model **by value**. Pointer-receiver helpers (`setToast`, `layout`,
  `periodicLoad`, `applyFilter`) mutate the **local** copy — the change only persists if you `return m`.
- Watch evaluation order in `return m, m.mutatingMethod()`. The established `return m, m.setToast(...)`
  idiom works (gc evaluates the call before copying `m`), but for new code prefer the explicit local:
  `cmd := m.method(); return m, cmd`.
- The AltScreen renderer **CLIPS** overflow — horizontal off the right edge, vertical off the bottom.
  It does **not** wrap. A too-wide line silently loses its right side; an extra row clips the
  help/status bar off the bottom. Keep the rendered frame at exactly `m.height`.

## Tables (bubbles `table`)

- **Do not put ANSI-colored strings in table cells.** A per-cell color emits a reset that terminates
  the row-level selection highlight mid-line, leaving every column to the right unhighlighted on the
  cursor row. Convey state with a **glyph** (e.g. `● ○ ◐ ✗`); keep color in the detail pane.
- `table.SetColumns` re-renders existing rows against the new column slice and **panics**
  (index-out-of-range) when shrinking the column set. In `syncTable`, call `SetRows(nil)` **before**
  `SetColumns`.
- **Adding a column?** Edit `allCols()` AND append the column's title to `colDropOrder` (both in
  view.go). A column missing from `colDropOrder` is never a drop candidate, so on a narrow terminal
  `minSum` exceeds the width and the AltScreen silently clips it. (`min`/`flex` live in the `colSpec`,
  and the cell func + title share that struct, so there's no index drift.) The column **count** is
  asserted in `TestVisibleColsDropsAndStaysAligned` and `TestSyncTableResizeShrinkNoPanic` — update
  those when you add/remove a column.

## Guest exec (`ExecInstance`)

The Incus client mirrors a guest command's stdout and stderr on **separate goroutines**, so:
- Give stdout and stderr their **own** `bytes.Buffer` — a single shared buffer is written
  concurrently and trips `go test -race`.
- `waitOp` returning does **not** mean the copies have flushed. Pass a `DataDone` channel in
  `InstanceExecArgs` and block on it (bounded by `ctx`) **before** reading the buffers; on `ctx`
  timeout, return **without** reading (the goroutines may still be writing).
- Reference implementation: `CloudInitStatus` in `internal/incus/logs.go`.

## Layout / help bar

- `helpRows()` reserves the multi-line cheat-sheet height **only** in list/busy modes; `layout()`
  and `bottomBar()` both use it, and `clampLines()` caps the rendered help so it can never overflow.
- Detail/logs viewports share `bodyH` with the table, so call `m.layout()` when the mode changes
  into **and out of** detail/logs (otherwise the viewport is left oversized, or the list help clipped).
- Size forms with `formWidth(m.width)` (fits inside the bordered box). Never floor a width above the
  terminal width.

## Sizes / Incus units

- Incus reads a **unit-less** `limits.memory` / disk size as **bytes**. The launch form's number
  fields append a unit via `withUnit(v, "MiB"|"GiB")`. Incus also rejects decimals (`1.5GiB`) and
  embedded spaces (`2 GiB`) — `validateSize` enforces whole-number-plus-unit only.
- The edit-cpu/ram form seeds memory through `normalizeMem()`, so a pre-existing **bare-byte**
  `limits.memory` isn't re-scaled by `withUnit` when re-submitted untouched.

## Signing / release

- Releases are signed with **cosign v3** into a single Sigstore **bundle** (`checksums.txt.bundle`),
  **not** split `.sig`/`.pem`. `install.sh` verifies with `cosign verify-blob --bundle`, gated on
  cosign **v3+** (older/absent → checksum-only with a loud warning).
- cosign-installer v4.1.2 ships cosign **v3.x**, whose `--new-bundle-format`/`--use-signing-config`
  default to `true`, so the old split-file flags (`--output-signature`/`--output-certificate`) are
  silently ignored. `.goreleaser.yaml`'s signs block uses `--bundle`. Don't "fix" it back to the
  deprecated flags.

## Templates

- Starters are seeded **additively by filename** (only missing ones are written), so a new baked-in
  template reaches existing installs without overwriting a user's edits.
- A `# name: <label>` header comment overrides the picker's display name (filenames can't contain
  `/` or spaces). The body must still start with `#cloud-config` and pass `ValidateCloudInit`.

## Other

- The `loadingVMs` guard only debounces the **tick/event** path (`periodicLoad()`). Direct
  `loadVMs()` callers (`Init`, the `R` refresh key, `opDoneMsg`, `execDoneMsg`) deliberately bypass
  it, and any `vmsMsg` clears the flag — so loads aren't strictly serialized when a direct load
  interleaves a tick load. For true single-flight, route direct callers through `periodicLoad()` or
  use an in-flight counter, not a bool.
- AltScreen is set on **each** `tea.View` (`v.AltScreen = true` in `View()`), not via a program
  option — there is intentionally no `tea.WithAltScreen()`. Every render path must set it.
- `copy IP` (`y`) is an optimistic OSC52 write (`tea.SetClipboard`); the "copied" toast does **not**
  confirm the terminal accepted it (it can fail silently under some tmux/SSH setups).
