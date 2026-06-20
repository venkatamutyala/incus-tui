# Release process

Releases are tagged, signed, and gated. Cut one only when asked.

## Cut a release

1. Ensure `main` is green in CI and the full verify suite passes locally (see
   [conventions](conventions.md)).
2. Tag and push:
   ```sh
   git tag -a vX.Y.Z -m "incus-tui vX.Y.Z — <summary>"
   git push origin vX.Y.Z
   ```
3. The `release` workflow runs and **pauses at the protected `release` environment** — a human must
   approve it (GitHub → Actions → the run → **Review deployments** → check `release` → **Approve and
   deploy**). This is a deliberate supply-chain gate. Do **not** approve it programmatically on the
   maintainer's behalf; surface the run URL and let them click.
4. After approval, in parallel:
   - GoReleaser builds the multi-arch linux binaries, signs `checksums.txt` into a cosign **keyless
     Sigstore bundle** (`checksums.txt.bundle`), publishes the GitHub Release, and attaches SLSA
     build-provenance.
   - A second job builds + pushes the multi-arch GHCR image, also SLSA-attested.

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
