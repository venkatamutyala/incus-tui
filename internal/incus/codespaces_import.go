package incus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/lxc/incus/v7/shared/api"

	incusclient "github.com/lxc/incus/v7/client"
)

// imageAliasPost builds the (nested) api.ImageAliasesPost for creating an alias.
func imageAliasPost(name, target, desc string) api.ImageAliasesPost {
	return api.ImageAliasesPost{ImageAliasesEntry: api.ImageAliasesEntry{
		ImageAliasesEntryPut: api.ImageAliasesEntryPut{Description: desc, Target: target},
		Name:                 name,
	}}
}

// isAlreadyExists matches the daemon's duplicate-image / duplicate-alias error (no typed status is
// returned, so string-matching is the only option; the alias-gate keeps us off this path normally).
func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// ImportCodespaceImage resolves a release (tag "" / "latest" → GitHub's latest), and if not
// already imported, downloads + reassembles the split qcow2 and registers it as a virtual-machine
// image aliased glueops-codespace-<tag> (+ a rolling glueops-codespace-latest). It reports
// progress and is fully ctx-cancelable; all temp files live under one dir removed on every path.
func (c *Client) ImportCodespaceImage(ctx context.Context, tag string, onProgress func(ImportProgress)) (string, error) {
	prog := func(p ImportProgress) {
		if onProgress != nil {
			onProgress(p)
		}
	}

	prog(ImportProgress{Phase: "resolve", Step: 1})
	rel, err := c.resolveRelease(ctx, tag)
	if err != nil {
		return "", err
	}
	if !rel.HasImage {
		return "", fmt.Errorf("release %s carries no codespace image (expected *.qcow2.tar.part_* assets — the publish format may have changed)", rel.Tag)
	}
	if runtime.GOARCH != "amd64" {
		return "", fmt.Errorf("the codespace image is x86_64-only; this host is %s", runtime.GOARCH)
	}
	alias := codespaceAliasPrefix + rel.Tag

	// Alias-gate: if this tag is already imported, skip the whole download and just refresh latest.
	if fp, ok := c.imageFingerprintByAlias(alias); ok {
		if err := c.retargetLatest(ctx, fp); err != nil {
			return "", err
		}
		return fp, nil
	}

	// Blast-radius guard: refuse if the target storage pool can't hold the image, rather than
	// letting the daemon hit ENOSPC and break running VMs. The stored image is ~the download size.
	if err := c.checkPoolSpace(ctx, rel.SizeBytes+512<<20); err != nil {
		return "", err
	}

	// Best-effort sweep of temp dirs a previous run orphaned (e.g. ctrl+c, which quits before the
	// deferred cleanup below can run). Bounds unbounded $TMPDIR growth. Only touches dirs older than
	// an hour so a (hypothetical) concurrent import's live dir is never removed.
	sweepStaleCodespaceTemp()

	dir, err := os.MkdirTemp("", "incus-tui-codespace-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }() // covers success, every error, ctx-cancel, and panic

	// Reassembly needs room for the parts + the tar + the extracted qcow2 (delete-as-we-go keeps
	// peak near 2× the download); require ~3× to be safe.
	if err := checkTempSpace(dir, rel.SizeBytes*3); err != nil {
		return "", err
	}

	qcowPath := filepath.Join(dir, "image.qcow2")
	if err := c.downloadAndAssemble(ctx, rel, dir, qcowPath, prog); err != nil {
		return "", err
	}

	prog(ImportProgress{Phase: "import", Step: 4})
	fp, err := c.createVMImage(ctx, qcowPath, rel)
	if err != nil {
		return "", err
	}

	// Alias the image; on a real failure roll back the just-created image so it can't orphan pool
	// space. A duplicate-fingerprint / duplicate-alias means it's effectively already there.
	if err := c.server.CreateImageAlias(imageAliasPost(alias, fp, "GlueOps Codespace "+rel.Tag)); err != nil && !isAlreadyExists(err) {
		c.rollbackImage(ctx, fp)
		return "", fmt.Errorf("aliasing %s: %w", alias, err)
	}
	if err := c.retargetLatest(ctx, fp); err != nil {
		return "", err
	}
	return fp, nil
}

// downloadAndAssemble streams the part assets to temp files (with byte progress), concatenates
// them via the pure assemble() into a tar, then extracts the single qcow2 member — deleting the
// intermediates as it goes to keep peak disk down. Every read loop is ctx-cancelable.
func (c *Client) downloadAndAssemble(ctx context.Context, rel CodespaceRelease, dir, qcowPath string, prog func(ImportProgress)) error {
	parts := make([]ghAsset, len(rel.parts))
	copy(parts, rel.parts)
	sort.SliceStable(parts, func(i, j int) bool { return partSuffix(parts[i].Name) < partSuffix(parts[j].Name) })

	var done int64
	files := make([]namedReader, 0, len(parts))
	var handles []*os.File
	defer func() {
		for _, h := range handles {
			_ = h.Close()
		}
	}()
	for _, a := range parts {
		p := filepath.Join(dir, a.Name)
		if err := c.downloadPart(ctx, a.URL, p, rel.SizeBytes, &done, prog); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		handles = append(handles, f)
		files = append(files, namedReader{name: a.Name, r: &ctxReader{ctx: ctx, r: f}})
	}

	prog(ImportProgress{Phase: "assemble", Step: 3})
	tarPath := filepath.Join(dir, "image.tar")
	tarFile, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	if err := assemble(files, tarFile); err != nil {
		_ = tarFile.Close()
		return err
	}
	if err := tarFile.Close(); err != nil {
		return err
	}
	for _, h := range handles { // free the part files before extract to halve peak disk
		_ = h.Close()
		_ = os.Remove(h.Name())
	}
	handles = nil

	if err := extractQcow2(ctx, tarPath, qcowPath); err != nil {
		return err
	}
	_ = os.Remove(tarPath)
	return nil
}

// downloadPart GETs one asset from the release CDN (browser_download_url — no API rate-limit) and
// streams it to disk with cumulative progress. A stale GITHUB_TOKEN yielding 401 is retried
// unauthenticated (the assets are public).
func (c *Client) downloadPart(ctx context.Context, url, dest string, total int64, done *int64, prog func(ImportProgress)) error {
	resp, err := ghGet(ctx, url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", filepath.Base(dest), err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		_ = resp.Body.Close()
		if resp, err = ghGetNoAuth(ctx, url); err != nil {
			return fmt.Errorf("downloading %s: %w", filepath.Base(dest), err)
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", filepath.Base(dest), resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	pr := &progressReader{r: resp.Body, done: done, onRead: func(d int64) {
		prog(ImportProgress{Phase: "download", Step: 2, Done: d, Total: total})
	}}
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(f, &ctxReader{ctx: ctx, r: pr}, buf); err != nil {
		return fmt.Errorf("downloading %s: %w", filepath.Base(dest), err)
	}
	return nil
}

// extractQcow2 pulls the single *.qcow2 member out of the reassembled tar into dest.
func extractQcow2(ctx context.Context, tarPath, dest string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	tr := tar.NewReader(&ctxReader{ctx: ctx, r: f})
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("no .qcow2 member found in the reassembled image (publish format may have changed)")
		}
		if err != nil {
			return fmt.Errorf("reading image tar: %w", err)
		}
		if h.Typeflag != tar.TypeReg || filepath.Ext(h.Name) != ".qcow2" {
			continue
		}
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		buf := make([]byte, 1<<20) // stream — never preallocate from the (untrusted) header size
		if _, err := io.CopyBuffer(out, &ctxReader{ctx: ctx, r: tr}, buf); err != nil {
			_ = out.Close()
			return fmt.Errorf("extracting qcow2: %w", err)
		}
		return out.Close()
	}
}

// createVMImage imports the qcow2 as a virtual-machine image and returns its fingerprint. The
// Type field is load-bearing: it makes the client name the multipart part "rootfs.img", which is
// how the daemon decides the image is a VM (not a container). The qcow2 is uploaded as-is (Incus
// converts qcow2→raw on first launch). The *os.File stays open until the op completes.
func (c *Client) createVMImage(ctx context.Context, qcowPath string, rel CodespaceRelease) (string, error) {
	meta, err := buildMetadata(rel)
	if err != nil {
		return "", err
	}
	rootfs, err := os.Open(qcowPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = rootfs.Close() }()

	op, err := c.server.CreateImage(api.ImagesPost{}, &incusclient.ImageCreateArgs{
		Type:     "virtual-machine",
		MetaFile: bytes.NewReader(meta),
		MetaName: "metadata.tar.gz",
		// Wrap the rootfs so a mid-upload esc/ctx-cancel aborts the (multi-GB) stream — the client
		// reads it synchronously, so a bare *os.File would ignore cancellation until waitOp. Seek
		// passes through; only Read gains the check (RootfsFile needs an io.ReadSeeker, so ctxReader
		// won't do). Over a local socket the upload is bounded, so this is belt-and-suspenders.
		RootfsFile: &ctxReadSeeker{ctx: ctx, File: rootfs},
		RootfsName: "rootfs.qcow2",
	})
	if err != nil {
		// An "already exists" here means the image blob is present but its alias was missing (the
		// alias-gate keyed on the alias, so we got this far). Point the user at the fix rather than
		// failing cryptically — the deterministic fingerprint makes the orphan unusable otherwise.
		if isAlreadyExists(err) {
			return "", fmt.Errorf("this codespace image is already in the store but has no alias — delete it in the images view (press i), then re-import: %w", err)
		}
		return "", fmt.Errorf("creating image: %w", err)
	}
	if err := waitOp(ctx, op); err != nil {
		return "", fmt.Errorf("creating image: %w", err)
	}
	fp, _ := op.Get().Metadata["fingerprint"].(string)
	if fp == "" {
		return "", fmt.Errorf("image imported but no fingerprint returned")
	}
	return fp, nil
}

// buildMetadata produces the metadata.tar.gz Incus requires: a gzipped tar with one metadata.yaml
// member. creation_date is the release's published_at (fixed, UTC) so the same tag always hashes
// to the same fingerprint — making re-import idempotent.
func buildMetadata(rel CodespaceRelease) ([]byte, error) {
	// Quote the description: the tag is external input, and a YAML metacharacter in it (#, @, …)
	// would otherwise produce a malformed metadata.yaml the daemon rejects. %q yields a valid
	// double-quoted YAML scalar for the ASCII characters a git tag can contain.
	yaml := fmt.Sprintf("architecture: x86_64\ncreation_date: %d\nproperties:\n  description: %q\n  os: Debian\n",
		rel.PublishedAt.UTC().Unix(), "GlueOps Codespace "+rel.Tag)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "metadata.yaml", Mode: 0o644, Size: int64(len(yaml))}); err != nil {
		return nil, err
	}
	if _, err := tw.Write([]byte(yaml)); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// imageFingerprintByAlias returns the image fingerprint an alias points at, and whether the alias
// exists (any lookup error is treated as "absent" — this only drives the skip-download gate).
func (c *Client) imageFingerprintByAlias(alias string) (string, bool) {
	entry, _, err := c.server.GetImageAlias(alias)
	if err != nil || entry == nil || entry.Target == "" {
		return "", false
	}
	return entry.Target, true
}

// retargetLatest points glueops-codespace-latest at fp, creating it if absent. It NEVER hijacks a
// user's same-named alias: if latest already exists and points at an image that is NOT one of ours
// (no glueops-codespace-* alias), it refuses rather than clobbering.
func (c *Client) retargetLatest(ctx context.Context, fp string) error {
	create := func() error {
		if err := c.server.CreateImageAlias(imageAliasPost(codespaceLatestAlias, fp, "GlueOps Codespace (latest imported)")); err != nil && !isAlreadyExists(err) {
			return fmt.Errorf("creating %s alias: %w", codespaceLatestAlias, err)
		}
		return nil
	}
	entry, _, err := c.server.GetImageAlias(codespaceLatestAlias)
	switch {
	case err != nil && api.StatusErrorCheck(err, http.StatusNotFound):
		return create() // alias genuinely absent
	case err != nil:
		// A transient/permission error — surface it instead of silently leaving latest stale while
		// reporting the import as a success.
		return fmt.Errorf("checking %s alias: %w", codespaceLatestAlias, err)
	case entry == nil:
		return create()
	}
	if entry.Target == fp {
		return nil
	}
	if !c.aliasTargetIsOurs(entry.Target) {
		return fmt.Errorf("%s already exists and points at a non-codespace image; not overwriting it", codespaceLatestAlias)
	}
	if err := c.server.UpdateImageAlias(codespaceLatestAlias, api.ImageAliasesEntryPut{Target: fp}, ""); err != nil {
		return fmt.Errorf("updating %s alias: %w", codespaceLatestAlias, err)
	}
	return nil
}

// aliasTargetIsOurs reports whether the image at fingerprint fp carries a glueops-codespace-* alias
// (i.e. we manage it) — used so retargetLatest never clobbers an unrelated user alias.
func (c *Client) aliasTargetIsOurs(fp string) bool {
	img, _, err := c.server.GetImage(fp)
	if err != nil || img == nil {
		return false
	}
	for _, a := range img.Aliases {
		if isOurCodespaceAlias(a.Name) {
			return true
		}
	}
	return false
}

// isOurCodespaceAlias reports whether an alias name marks an image we imported: a glueops-codespace-*
// per-tag alias — but NOT `latest` itself, since a user may have hand-created glueops-codespace-latest
// and counting that as "ours" would defeat the very guard meant to protect it from being clobbered.
func isOurCodespaceAlias(name string) bool {
	return name != codespaceLatestAlias && strings.HasPrefix(name, codespaceAliasPrefix)
}

// rollbackImage best-effort deletes a just-created image after a later step failed, so a partial
// import can't leave an aliasless image occupying pool space.
func (c *Client) rollbackImage(ctx context.Context, fp string) {
	if op, err := c.server.DeleteImage(fp); err == nil {
		_ = waitOp(ctx, op)
	}
}

// ctxReadSeeker makes the rootfs upload abort on ctx-cancel. The Incus client needs an
// io.ReadSeeker for RootfsFile (so the un-seekable ctxReader won't do); embedding *os.File keeps
// Seek/Close, and only Read gains the cancellation check.
type ctxReadSeeker struct {
	ctx context.Context
	*os.File
}

func (c *ctxReadSeeker) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.File.Read(p)
}

// sweepStaleCodespaceTemp best-effort removes import temp dirs older than an hour that a previous
// run left behind — e.g. a ctrl+c that quit the process before the deferred RemoveAll could run.
// The age gate means a (hypothetical) concurrent import's live dir is never removed.
func sweepStaleCodespaceTemp() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "incus-tui-codespace-*"))
	cutoff := time.Now().Add(-time.Hour)
	for _, d := range matches {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() && fi.ModTime().Before(cutoff) {
			_ = os.RemoveAll(d)
		}
	}
}

// ghGetNoAuth is ghGet without the Authorization header — the retry when a stale GITHUB_TOKEN
// turns a public request into a 401.
func ghGetNoAuth(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	return httpClient().Do(req)
}

// checkTempSpace refuses up front if the temp filesystem lacks `need` bytes, rather than failing
// deep in a multi-minute reassembly.
func checkTempSpace(dir string, need int64) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return nil // can't check → don't block
	}
	avail := int64(st.Bavail) * st.Bsize
	if avail < need {
		return fmt.Errorf("not enough temp space in %s: need ~%d GB, have ~%d GB", os.TempDir(), need>>30, avail>>30)
	}
	return nil
}
