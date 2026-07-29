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
  help/status bar off the bottom. The frame must be **exactly `m.height` rows by `m.width` cols** —
  and just as bad, a frame that is *too short* leaves the previous (taller) frame's bottom rows on
  screen as **garbled leftover text** (the AltScreen / tmux don't clear cells you didn't repaint).
  `frameView()` (view.go) enforces this: it pins the body to `bodyH` and then clamps the whole frame
  to `m.height × m.width`. **huh forms render a variable height** (their content height, which *grows*
  when an inline validation error appears), so a form/editor body **must** go through that pin — never
  emit a form frame whose height you assume is fixed. `formHeight()` budgets the form to leave room
  for the box border + huh's footer + a 1–2 line error inside `bodyH`. Regression: `TestFrameAlwaysExactlyFillsScreen`.

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
  fields append a unit via `withUnit(v, "MiB"|"GiB")`. `validateSize` enforces whole-number-plus-**IEC**
  unit only — it rejects decimals (`1.5GiB`), spaces (`2 GiB`), **and SI units** (`10GB`): every field
  advertises IEC, and `10GB` (≈9.31GiB) under a "GiB" label would read as a shrink on the disk field.
- The edit-cpu/ram/disk form seeds memory **and disk** through `normalizeByteSize()`, so a pre-existing
  **bare-byte** value isn't re-scaled by `withUnit` when re-submitted untouched.
- **VM disk resize** (`SetResources` disk arg, `internal/incus/instances.go`): three linked rules,
  each with a regression test — re-breaking any is a regression.
  - **Grow-only.** `growOnly()` rejects a shrink before the daemon round-trip (a VM block disk can't
    shrink). This helper was previously deleted with the storage-pool feature; it now backs disk resize.
    An inline `growOnlyValidator` also catches a shrink in-field.
  - **Requires a STOPPED VM.** Growing a running VM's block disk is driver-dependent (a `dir` pool holds
    the image open and rejects it). `SetResources` refuses it authoritatively; `newEditForm` **omits the
    editable disk field** for a non-stopped VM (showing a note instead) so a cpu/ram edit is never
    dropped just because the form also had a disk field. Don't collapse these two into one.
  - **An unchanged edit must not pin the disk.** The disk field is *seeded* with the current size, so
    `diskResizeArg` returns `""` (leave the root device alone) unless the value actually changed —
    otherwise a cpu/ram-only edit would rewrite `root.size` and convert a profile-inherited disk into a
    pinned instance override.

## Signing / release

- Releases are signed with **cosign v3** into a single Sigstore **bundle** (`checksums.txt.bundle`),
  **not** split `.sig`/`.pem`. `install.sh` verifies with `cosign verify-blob --bundle`, gated on
  cosign **v3+** (older/absent → checksum-only with a loud warning).
- cosign-installer v4.1.2 ships cosign **v3.x**, whose `--new-bundle-format`/`--use-signing-config`
  default to `true`, so the old split-file flags (`--output-signature`/`--output-certificate`) are
  silently ignored. `.goreleaser.yaml`'s signs block uses `--bundle`. Don't "fix" it back to the
  deprecated flags.
- **Releases are Release-Please-driven; the trigger is `push: main`, not a tag.** `release.yml` runs
  Release Please on every push to `main` to maintain the release PR; when that PR merges it creates
  the tag + a **draft** release, and only then (`release_created == 'true'`) do the approve/goreleaser/
  docker jobs run. The default `GITHUB_TOKEN` a tag Release Please pushes would **not** trigger a
  separate `on: push: tags` workflow, which is why build+publish live in the *same* workflow gated on
  `release_created`. Version/changelog config lives in `release-please-config.json` +
  `.release-please-manifest.json` (manifest mode). `include-component-in-tag: false` keeps tags `vX.Y.Z`;
  `force-tag-creation: true` makes the tag exist even though the release starts as a draft (GitHub
  normally defers a draft's tag).
- **GitHub "immutable releases" ⇒ attach assets to a DRAFT, publish LAST.** Immutable releases lock a
  release the instant it is *published*; any asset upload after that fails with `422 Cannot upload
  assets to an immutable release`. So Release Please creates the release as a **draft** (`draft: true`),
  GoReleaser **uploads into that existing draft** (`use_existing_draft: true`, `mode: keep-existing`,
  `replace_existing_artifacts: true` in `.goreleaser.yaml`) rather than creating/publishing its own,
  and `release.yml` flips it live with `gh release edit "$TAG" --draft=false` **as the last step**,
  after the uploads and the attestation. That publish is the atomic transition that makes the release
  immutable *with* its assets. Don't set `draft: false`, don't let GoReleaser own the release object,
  and don't move the publish step ahead of artifact upload.

## Templates

- Starters are seeded **additively by filename** (only missing ones are written), so a new baked-in
  template reaches existing installs without overwriting a user's edits.
- A `# name: <label>` header comment overrides the picker's display name (filenames can't contain
  `/` or spaces). The body must still start with `#cloud-config` and pass `ValidateCloudInit`.

## Codespace image import (`I`) / images view (`i`)

- **`Type: "virtual-machine"` in `CreateImage` is load-bearing.** The Incus client derives the
  multipart rootfs field name from `ImageCreateArgs.Type`; only `"virtual-machine"` makes the daemon
  register a **VM** image (otherwise it's a *container* image that can't launch as a VM). This is the
  one assertion the live integration test pins (`TestLiveCodespaceImportMechanism`). Don't drop or
  rename the field.
- **Idempotency is deterministic, not incidental.** The import fingerprint is stable because
  `buildMetadata` writes `creation_date` from the release's `published_at` as a **fixed UTC Unix
  timestamp** — same tag → same fingerprint → re-import is a no-op. Change that layout (local time, a
  wall-clock `now`, RFC3339) and every re-import re-downloads 5 GB and orphans the old image. The
  service also **alias-gates** (`glueops-codespace-<tag>`): if the alias exists it skips the download
  entirely and just refreshes the rolling `latest`.
- **Progress-busy is a distinct variant of `busy()` — mind the channel ownership.** `busyProgress()`
  (model.go) streams `ImportProgress` over a **cap-1** channel while the op runs. The **producer
  goroutine is the sole owner** and closes it via `defer close(progCh)` on every exit
  (success/error/cancel); the listener (`listenProgress`) only *reads* and re-arms — it must **never**
  close the channel, and cancel is **ctx-cancel**, never an external close (send-on-closed panics the
  TUI). Coalescing + the ~200 ms throttle live in the `onProgress` adapter, so the service callback
  stays a dumb synchronous function. `opDoneMsg` is still the terminal signal; `busyProgressMsg` only
  refreshes the status line. The rich single-line status must not wrap — `frameView()`'s width clamp
  truncates it (leftmost = highest priority).
- **Preflight the POOL, not TMPDIR, before import *and* launch.** On a `dir` pool the image store and
  every VM's `root.img` share one filesystem, so filling it gives **running VMs ENOSPC** (guest
  corruption, not a clean failure). Both paths call `checkPoolSpace`/`CheckLaunchSpace`
  (`GetStoragePoolResources` on the default profile's root pool) and refuse with a concrete message.
  This is the mandatory mitigation for the roomy **50 GiB** default launch disk.
- **`retargetLatest` never clobbers a user's alias.** `UpdateImageAlias` is a blind PUT, so before
  moving `glueops-codespace-latest` it checks the current target actually carries a
  `glueops-codespace-*` alias (i.e. we own it); otherwise it refuses.
- **The split-asset naming is the one brittle contract.** `codespacePartRe`
  (`\.qcow2\.tar\.part_[a-z]+$`) is the single source of truth used by both `HasImage` detection and
  the downloader. If a release has assets but none match, the importer emits "publish format may have
  changed" rather than silently doing the wrong thing. `assemble()` sorts by the fixed-width base-26
  `split` suffix and **verifies contiguity** — a gap is an error, not a corrupt concatenation (don't
  trust GitHub's asset array order).

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
