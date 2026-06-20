package incus

import (
	"context"
	"fmt"

	"github.com/lxc/incus/v7/shared/units"
)

// StoragePool is a flattened, UI-friendly view of an Incus storage pool. Like VM it
// holds only plain values so it can cross the goroutine boundary into the Bubble Tea
// update loop without sharing state.
type StoragePool struct {
	Name       string
	Driver     string
	SizeConfig string // the pool's configured "size" (e.g. "1TiB"); "" when not loop-backed
	UsedBytes  int64
	TotalBytes int64
	Resizable  bool // a loop-backed pool carries a "size" config key and can be grown in place
}

// UsedPct returns used space as a whole-number percentage of total, or 0 when the total
// is unknown (some drivers don't report resources).
func (p StoragePool) UsedPct() int {
	if p.TotalBytes <= 0 {
		return 0
	}
	return int(p.UsedBytes * 100 / p.TotalBytes)
}

// ListStoragePools returns every storage pool with its current space usage. A pool whose
// resources can't be read (some drivers don't report them) still appears, just without
// usage figures, so the caller always gets the full set.
func (c *Client) ListStoragePools() ([]StoragePool, error) {
	pools, err := c.server.GetStoragePools()
	if err != nil {
		return nil, fmt.Errorf("listing storage pools: %w", err)
	}
	out := make([]StoragePool, 0, len(pools))
	for _, p := range pools {
		sp := StoragePool{
			Name:       p.Name,
			Driver:     p.Driver,
			SizeConfig: p.Config["size"],
			Resizable:  p.Config["size"] != "",
		}
		if res, err := c.server.GetStoragePoolResources(p.Name); err == nil {
			sp.UsedBytes = int64(res.Space.Used)
			sp.TotalBytes = int64(res.Space.Total)
		}
		out = append(out, sp)
	}
	return out, nil
}

// ResizeStoragePool grows a loop-backed pool by read-modify-writing its "size" config —
// the same change `incus storage set <pool> size=<n>` makes. Loop-backed pools can only
// grow, never shrink, so a shrink is rejected up front (Incus rejects it too, but with a
// murkier message). The update is synchronous; there is no Operation to await.
func (c *Client) ResizeStoragePool(ctx context.Context, name, newSize string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pool, etag, err := c.server.GetStoragePool(name)
	if err != nil {
		return fmt.Errorf("reading storage pool %q: %w", name, err)
	}
	if pool.Config["size"] == "" {
		return fmt.Errorf("storage pool %q is not resizable (no size set; not loop-backed)", name)
	}
	if err := growOnly(pool.Config["size"], newSize); err != nil {
		return fmt.Errorf("resizing storage pool %q: %w", name, err)
	}
	put := pool.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	put.Config["size"] = newSize
	if err := c.server.UpdateStoragePool(name, put, etag); err != nil {
		return fmt.Errorf("resizing storage pool %q: %w", name, err)
	}
	return nil
}

// growOnly returns an error unless requested is a valid size that is >= current. Both
// sizes are parsed the same way Incus parses pool sizes (IEC units like "1TiB").
func growOnly(current, requested string) error {
	cur, err := units.ParseByteSizeString(current)
	if err != nil {
		return fmt.Errorf("parsing current size %q: %w", current, err)
	}
	req, err := units.ParseByteSizeString(requested)
	if err != nil {
		return fmt.Errorf("parsing new size %q: %w", requested, err)
	}
	if req < cur {
		return fmt.Errorf("pools can only grow: current %s > requested %s", current, requested)
	}
	return nil
}
