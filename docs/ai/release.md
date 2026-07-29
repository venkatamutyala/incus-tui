# Release process

Releases are **driven by Release Please** from the Conventional Commit history, then signed and
gated. You don't tag by hand — you merge a release PR.

## Cut a release

1. Land your changes on `main` with **Conventional Commit** messages (`feat:`, `fix:`, `docs:`,
   `ci:`, …). Pre-1.0 the config bumps a **patch** for `feat`/`fix` and a **minor** only for a
   breaking change (`!` / `BREAKING CHANGE`) — see `release-please-config.json`.
2. On each push to `main`, the `release-please` job opens/updates a **"chore(main): release X.Y.Z"
   PR** that maintains `CHANGELOG.md` and the version in `.release-please-manifest.json`. Review it
   like any PR.
3. **Merge that release PR.** That is the "cut a release" action. Release Please then creates the git
   tag `vX.Y.Z` and a **draft** GitHub Release (changelog as the body).
4. The workflow **pauses at the protected `release` environment** — a human must approve it
   (GitHub → Actions → the run → **Review deployments** → check `release` → **Approve and deploy**).
   A deliberate supply-chain gate on top of the PR merge. Do **not** approve programmatically on the
   maintainer's behalf; surface the run URL and let them click.
5. After approval, in parallel:
   - GoReleaser builds the multi-arch linux binaries, signs `checksums.txt` into a cosign **keyless
     Sigstore bundle** (`checksums.txt.bundle`), **uploads them into the draft** release
     (`use_existing_draft`), attaches SLSA build-provenance, then **publishes** the draft
     (`gh release edit --draft=false`) — the atomic step that makes the release immutable *with* its
     assets (see the immutable-releases gotcha).
   - A second job builds + pushes the multi-arch GHCR image, also SLSA-attested.

> Why merge-not-tag: the default `GITHUB_TOKEN` a tag pushed by Release Please would **not** trigger
> a separate `on: push: tags` workflow, so build+publish live in the *same* workflow, gated on Release
> Please's `release_created` output.

**One-off / manual version:** add `Release-As: X.Y.Z` to a commit body, or a `release-as:` label, to
force the next version. Reverting a bad release still means a new tag — tags/releases are immutable.

## Verify a published release

```sh
v=vX.Y.Z; a=incus-tui_linux_amd64.tar.gz
base=https://github.com/venkatamutyala/incus-tui/releases/download/$v
curl -fsSLO "$base/$a"; curl -fsSLO "$base/checksums.txt"; curl -fsSLO "$base/checksums.txt.bundle"
cosign verify-blob checksums.txt --bundle checksums.txt.bundle \
  --certificate-identity-regexp '^https://github\.com/venkatamutyala/incus-tui/\.github/workflows/release\.yml@refs/tags/v.+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
```
Then run `install.sh` (with cosign v3+ on PATH) pinned to the tag and confirm it prints
"Signature verified (cosign keyless bundle …)".

## Supply-chain policy

- Every GitHub Action `uses:` is SHA-pinned to a release that has been public **≥30 days**
  (Dependabot enforces this via a 30-day `cooldown` in `.github/dependabot.yml`). To update a pin:
  resolve the latest non-prerelease tag whose release is ≥30 days old, resolve that tag to its
  **commit** SHA (deref annotated tags), confirm the SHA matches the version comment, then pin.
- The Dockerfile pins the Zabbly apt key by fingerprint, so a swapped key fails the image build.
- `install.sh` is **fail-closed**: `checksums.txt` is mandatory; the checksum must match; with
  cosign v3+ the bundle signature must verify; otherwise it warns loudly and continues on the
  checksum (integrity, not authenticity).
