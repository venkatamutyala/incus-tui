//go:build integration

package incus

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

// TestLiveStoragePoolResize exercises the full storage-pool path against a live daemon:
// it stands up a throwaway btrfs loop-backed pool, confirms ListStoragePools surfaces it
// as resizable with usage, grows it via ResizeStoragePool, and asserts a shrink is refused.
// It SKIPs (not fails) when there is no daemon or btrfs isn't available, so a runner without
// btrfs-progs goes neutral instead of red.
func TestLiveStoragePoolResize(t *testing.T) {
	c, err := Connect()
	if err != nil {
		t.Skipf("no reachable Incus daemon: %v", err)
	}
	defer c.Disconnect()

	name := fmt.Sprintf("tuitest-%d", time.Now().UnixNano()%100000)
	if err := c.server.CreateStoragePool(api.StoragePoolsPost{
		Name:           name,
		Driver:         "btrfs",
		StoragePoolPut: api.StoragePoolPut{Config: map[string]string{"size": "256MiB"}},
	}); err != nil {
		t.Skipf("cannot create a btrfs loop pool (btrfs unavailable?): %v", err)
	}
	t.Cleanup(func() { _ = c.server.DeleteStoragePool(name) })

	// ListStoragePools must surface it as resizable, with its configured size and live usage.
	pools, err := c.ListStoragePools()
	if err != nil {
		t.Fatalf("ListStoragePools: %v", err)
	}
	var got *StoragePool
	for i := range pools {
		if pools[i].Name == name {
			got = &pools[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("ListStoragePools did not include %q", name)
	}
	if !got.Resizable || got.SizeConfig != "256MiB" {
		t.Fatalf("pool %q: Resizable=%v SizeConfig=%q, want true/256MiB", name, got.Resizable, got.SizeConfig)
	}
	if got.TotalBytes <= 0 {
		t.Fatalf("pool %q: TotalBytes=%d, want > 0 (usage not reported)", name, got.TotalBytes)
	}
	t.Logf("pool %q: %d/%d bytes (%d%%)", name, got.UsedBytes, got.TotalBytes, got.UsedPct())

	// Grow it, then confirm the daemon recorded the new size.
	ctx := context.Background()
	if err := c.ResizeStoragePool(ctx, name, "512MiB"); err != nil {
		t.Fatalf("ResizeStoragePool grow: %v", err)
	}
	pool, _, err := c.server.GetStoragePool(name)
	if err != nil {
		t.Fatalf("GetStoragePool after grow: %v", err)
	}
	if pool.Config["size"] != "512MiB" {
		t.Fatalf("after grow, size=%q, want 512MiB", pool.Config["size"])
	}

	// A shrink must be refused up front by growOnly (never reaching the daemon).
	if err := c.ResizeStoragePool(ctx, name, "128MiB"); err == nil {
		t.Fatal("ResizeStoragePool shrink: expected an error, got nil")
	}
}
