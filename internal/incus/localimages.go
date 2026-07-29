package incus

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/lxc/incus/v7/shared/units"
)

// LocalImage is a flattened view of an image in the local Incus store, for the images-management
// view. (Incus doesn't expose a used-by count on api.Image; delete relies on the daemon refusing
// an in-use image instead.)
type LocalImage struct {
	Fingerprint string
	Aliases     []string
	Description string
	Size        int64
	CreatedAt   time.Time
	Type        string // "virtual-machine" | "container"
}

// ListLocalImages returns the images in the local store, newest first.
func (c *Client) ListLocalImages(ctx context.Context) ([]LocalImage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	imgs, err := c.server.GetImages()
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}
	out := make([]LocalImage, 0, len(imgs))
	for _, im := range imgs {
		li := LocalImage{
			Fingerprint: im.Fingerprint,
			Description: im.Properties["description"],
			Size:        im.Size,
			CreatedAt:   im.CreatedAt,
			Type:        im.Type,
		}
		for _, a := range im.Aliases {
			li.Aliases = append(li.Aliases, a.Name)
		}
		out = append(out, li)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// DeleteImage removes an image from the local store. The daemon refuses (and this surfaces the
// reason) when the image is still in use by an instance — we never force.
func (c *Client) DeleteImage(ctx context.Context, fingerprint string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	op, err := c.server.DeleteImage(fingerprint)
	if err != nil {
		return fmt.Errorf("deleting image %.12s: %w", fingerprint, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("deleting image %.12s: %w", fingerprint, err)
	}
	return nil
}

// CheckLaunchSpace refuses a launch whose root disk can't fit the target storage pool — the same
// blast-radius guard the importer applies, exported for the launch path. size is the requested disk
// (e.g. "50GiB"); an empty or unparseable size skips the check (fail-open).
func (c *Client) CheckLaunchSpace(ctx context.Context, size string) error {
	need, err := units.ParseByteSizeString(size)
	if err != nil || need <= 0 {
		return nil
	}
	return c.checkPoolSpace(ctx, need)
}

// checkPoolSpace refuses if the storage pool backing new instances lacks `need` bytes free — the
// blast-radius guard so an import (or a launch) can't drive the shared pool to ENOSPC and corrupt
// EXISTING running VMs. It fails open (returns nil) when the pool/space can't be determined.
func (c *Client) checkPoolSpace(ctx context.Context, need int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pool, err := c.defaultRootPool()
	if err != nil || pool == "" {
		return nil
	}
	res, err := c.server.GetStoragePoolResources(pool)
	if err != nil {
		return nil
	}
	free := int64(res.Space.Total) - int64(res.Space.Used)
	if res.Space.Total > 0 && free < need {
		return fmt.Errorf("storage pool %q has ~%d GiB free; need ~%d GiB — delete some images/VMs first", pool, free>>30, need>>30)
	}
	return nil
}

// defaultRootPool resolves the storage pool the default profile's root disk device uses.
func (c *Client) defaultRootPool() (string, error) {
	prof, _, err := c.server.GetProfile("default")
	if err != nil {
		return "", err
	}
	root, ok := prof.Devices["root"]
	if !ok {
		return "", nil
	}
	return root["pool"], nil
}
