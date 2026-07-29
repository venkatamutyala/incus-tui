package incus

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	incusclient "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// fakeAliasServer implements only the InstanceServer methods retargetLatest/aliasTargetIsOurs touch;
// every other method is the embedded nil interface and panics if called (proving those four are the
// entire surface). It records create/update calls so a test can assert the clobber guard's decision.
type fakeAliasServer struct {
	incusclient.InstanceServer
	getAliasEntry *api.ImageAliasesEntry
	getAliasErr   error
	images        map[string]*api.Image
	created       []api.ImageAliasesPost
	updated       []api.ImageAliasesEntryPut
}

func (f *fakeAliasServer) GetImageAlias(string) (*api.ImageAliasesEntry, string, error) {
	return f.getAliasEntry, "", f.getAliasErr
}

func (f *fakeAliasServer) GetImage(fp string) (*api.Image, string, error) {
	if img, ok := f.images[fp]; ok {
		return img, "", nil
	}
	return nil, "", api.StatusErrorf(http.StatusNotFound, "no image")
}

func (f *fakeAliasServer) CreateImageAlias(a api.ImageAliasesPost) error {
	f.created = append(f.created, a)
	return nil
}

func (f *fakeAliasServer) UpdateImageAlias(_ string, put api.ImageAliasesEntryPut, _ string) error {
	f.updated = append(f.updated, put)
	return nil
}

func entry(target string) *api.ImageAliasesEntry {
	return &api.ImageAliasesEntry{ImageAliasesEntryPut: api.ImageAliasesEntryPut{Target: target}}
}

// retargetLatest is the safety-critical clobber guard for the rolling glueops-codespace-latest alias.
// Pin every branch: create-when-absent, surface-a-transient-error (fix #5), refuse-when-not-ours,
// retarget-when-ours, and no-op-when-equal.
func TestRetargetLatest(t *testing.T) {
	ctx := context.Background()

	t.Run("creates when absent (404)", func(t *testing.T) {
		f := &fakeAliasServer{getAliasErr: api.StatusErrorf(http.StatusNotFound, "absent")}
		if err := (&Client{server: f}).retargetLatest(ctx, "fp1"); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 1 || f.created[0].Target != "fp1" || len(f.updated) != 0 {
			t.Fatalf("want one create->fp1, no update; got created=%+v updated=%+v", f.created, f.updated)
		}
	})

	t.Run("surfaces a transient error, does not silently no-op (fix #5)", func(t *testing.T) {
		f := &fakeAliasServer{getAliasErr: api.StatusErrorf(http.StatusInternalServerError, "boom")}
		err := (&Client{server: f}).retargetLatest(ctx, "fp1")
		if err == nil {
			t.Fatal("a transient lookup error must surface, not be treated as absent")
		}
		if len(f.created) != 0 || len(f.updated) != 0 {
			t.Error("must not create or update the alias on a transient error")
		}
	})

	t.Run("refuses to clobber a non-codespace target", func(t *testing.T) {
		// latest points at a user's image whose only glueops-codespace-* alias is `latest` itself
		// (i.e. hand-created) — must NOT count as ours.
		f := &fakeAliasServer{
			getAliasEntry: entry("userfp"),
			images:        map[string]*api.Image{"userfp": {Aliases: []api.ImageAlias{{Name: codespaceLatestAlias}}}},
		}
		if err := (&Client{server: f}).retargetLatest(ctx, "fp1"); err == nil {
			t.Fatal("expected refusal when latest points at a non-codespace image")
		}
		if len(f.updated) != 0 {
			t.Error("must NOT clobber a user's glueops-codespace-latest alias")
		}
	})

	t.Run("retargets when the current target is ours", func(t *testing.T) {
		f := &fakeAliasServer{
			getAliasEntry: entry("oldfp"),
			images:        map[string]*api.Image{"oldfp": {Aliases: []api.ImageAlias{{Name: codespaceAliasPrefix + "v1"}}}},
		}
		if err := (&Client{server: f}).retargetLatest(ctx, "newfp"); err != nil {
			t.Fatal(err)
		}
		if len(f.updated) != 1 || f.updated[0].Target != "newfp" {
			t.Fatalf("want one update->newfp, got %+v", f.updated)
		}
	})

	t.Run("no-op when already pointing at fp", func(t *testing.T) {
		f := &fakeAliasServer{getAliasEntry: entry("fp1")}
		if err := (&Client{server: f}).retargetLatest(ctx, "fp1"); err != nil {
			t.Fatal(err)
		}
		if len(f.created) != 0 || len(f.updated) != 0 {
			t.Errorf("target already == fp: want a no-op, got created=%+v updated=%+v", f.created, f.updated)
		}
	})
}

// sweepStaleCodespaceTemp removes leftover import dirs older than an hour but must NOT touch a fresh
// dir (which could be a concurrent import's live workspace) or unrelated dirs.
func TestSweepStaleCodespaceTemp(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // os.TempDir() honors TMPDIR on unix
	old := filepath.Join(tmp, "incus-tui-codespace-old")
	fresh := filepath.Join(tmp, "incus-tui-codespace-fresh")
	other := filepath.Join(tmp, "unrelated")
	for _, d := range []string{old, fresh, other} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, twoHoursAgo, twoHoursAgo); err != nil {
		t.Fatal(err)
	}

	sweepStaleCodespaceTemp()

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("a stale (2h-old) import temp dir should have been removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh import temp dir must be kept (it may be a concurrent import's live dir)")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("an unrelated dir must never be touched")
	}
}
