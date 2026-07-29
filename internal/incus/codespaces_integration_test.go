//go:build integration

// De-risks the highest-unknown part of the codespace importer: that a qcow2 registered via
// CreateImage(Type:"virtual-machine") actually lands as a VM image (not a container), can be
// aliased/listed, and deletes cleanly. It uses a tiny SYNTHETIC qcow2 — it proves the import
// MECHANISM, not that the real ~20 GB GlueOps image boots (that's the separate real-image
// acceptance step). Runs against a live daemon; no /dev/kvm needed (import doesn't launch).
//
//	go test -tags integration ./internal/incus/...
package incus

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func requireLiveDaemon(t *testing.T) *Client {
	t.Helper()
	c, err := Connect()
	if err != nil {
		t.Skipf("no reachable Incus daemon: %v", err)
	}
	return c
}

// makeTinyQcow2 builds a small, format-valid qcow2 the daemon will accept as an image blob.
func makeTinyQcow2(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not available to build a synthetic qcow2")
	}
	path := filepath.Join(t.TempDir(), "tiny.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", path, "1M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v: %s", err, out)
	}
	return path
}

func TestLiveCodespaceImportMechanism(t *testing.T) {
	c := requireLiveDaemon(t)
	defer c.Disconnect()
	ctx := context.Background()

	qcow := makeTinyQcow2(t)
	rel := CodespaceRelease{Tag: "itest", PublishedAt: time.Unix(1700000000, 0).UTC()}

	fp, err := c.createVMImage(ctx, qcow, rel)
	if err != nil {
		t.Fatalf("createVMImage: %v", err)
	}
	t.Cleanup(func() { _ = c.DeleteImage(context.Background(), fp) })

	// The load-bearing assertion: the Type field made the daemon register a VM, not a container.
	img, _, err := c.server.GetImage(fp)
	if err != nil {
		t.Fatalf("GetImage: %v", err)
	}
	if img.Type != "virtual-machine" {
		t.Fatalf("image Type = %q, want virtual-machine (the CreateImage Type field is load-bearing)", img.Type)
	}

	// Alias it and confirm the alias-gate (which drives idempotency) resolves it.
	alias := codespaceAliasPrefix + rel.Tag
	if err := c.server.CreateImageAlias(imageAliasPost(alias, fp, "itest codespace")); err != nil {
		t.Fatalf("CreateImageAlias: %v", err)
	}
	t.Cleanup(func() { _ = c.server.DeleteImageAlias(alias) })
	if got, ok := c.imageFingerprintByAlias(alias); !ok || got != fp {
		t.Fatalf("imageFingerprintByAlias = %q,%v; want %q,true", got, ok, fp)
	}

	// A re-import of the same tag must be idempotent: same deterministic fingerprint, no error.
	if fp2, err := c.createVMImage(ctx, qcow, rel); err != nil {
		if !isAlreadyExists(err) {
			t.Fatalf("re-import: %v", err)
		}
	} else if fp2 != fp {
		t.Fatalf("re-import fingerprint = %.12s, want deterministic %.12s", fp2, fp)
	}

	// ListLocalImages surfaces it, flattened and typed.
	imgs, err := c.ListLocalImages(ctx)
	if err != nil {
		t.Fatalf("ListLocalImages: %v", err)
	}
	var found *LocalImage
	for i := range imgs {
		if imgs[i].Fingerprint == fp {
			found = &imgs[i]
		}
	}
	if found == nil {
		t.Fatalf("ListLocalImages did not include the imported image %.12s", fp)
	}
	if found.Type != "virtual-machine" {
		t.Errorf("ListLocalImages Type = %q, want virtual-machine", found.Type)
	}

	// The launch wizard (ListVMImages) must ALSO surface the imported image, marked Local — this is
	// the bug where an imported codespace showed in the images view but the create-VM wizard said it
	// wasn't imported (it only listed remote catalog images).
	vmImgs, err := c.ListVMImages()
	if err != nil {
		t.Fatalf("ListVMImages: %v", err)
	}
	var inWizard *Image
	for i := range vmImgs {
		if vmImgs[i].Fingerprint == fp {
			inWizard = &vmImgs[i]
		}
	}
	if inWizard == nil {
		t.Fatalf("ListVMImages did not include the imported image %.12s — it would be unlaunchable", fp)
	}
	if !inWizard.Local {
		t.Errorf("imported image should be marked Local (launches from the local store, not remote)")
	}
	if inWizard.Alias != alias {
		t.Errorf("wizard label = %q, want the codespace alias %q (drives the launch breadcrumb)", inWizard.Alias, alias)
	}

	// DeleteImage removes it.
	if err := c.DeleteImage(ctx, fp); err != nil {
		t.Fatalf("DeleteImage: %v", err)
	}
	if _, _, err := c.server.GetImage(fp); err == nil {
		t.Error("image still present after DeleteImage")
	}
	_ = os.Remove(qcow)
}
