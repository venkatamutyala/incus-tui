package incus

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

// Image is a flattened, UI-friendly view of a VM-capable image the launcher can offer.
type Image struct {
	Fingerprint string
	Alias       string // a display label: real alias if any, else os/release/variant
	Cloud       bool   // cloud variant (ships the guest agent → exec/cloud-init work)
	Description string
	Arch        string
	SizeBytes   int64
	Local       bool // already in the local store (e.g. an imported codespace) → launches instantly
}

// ListVMImages returns VM-capable images the launch wizard can offer for the host architecture:
// both **locally-imported** images (e.g. a GlueOps codespace, which live in the local daemon and
// would otherwise never appear here) and the public simplestreams catalog. Cloud variants sort
// first; the newest build wins per product so a cached remote image isn't duplicated by its catalog
// entry. The remote catalog is best-effort — if the image server is unreachable we still return the
// local images, so an imported codespace stays launchable offline.
func (c *Client) ListVMImages() ([]Image, error) {
	host := c.hostArch() // "" if undetermined → don't filter

	// The server publishes several daily-build serials per product, so the same image appears ~3×.
	// Group by product and keep the newest build so the launcher shows one clean row per image.
	type entry struct {
		img     Image
		created time.Time
	}
	best := map[string]*entry{}
	consider := func(im *api.Image, local bool) {
		if im.Type != "virtual-machine" {
			return
		}
		if host != "" && normalizeArch(im.Architecture) != host {
			return // an image of another arch can't boot here
		}
		key := productKey(im)
		// Newest build wins per product. A custom local image (no os/release properties, e.g. the
		// codespace) keys by fingerprint via productKey, so it always gets its own row; a cached
		// remote image shares its product's key and thus collapses with the catalog entry.
		if e, ok := best[key]; ok && !im.CreatedAt.After(e.created) {
			return
		}
		best[key] = &entry{
			created: im.CreatedAt,
			img: Image{
				Fingerprint: im.Fingerprint,
				Alias:       imageLabel(im),
				Cloud:       im.Properties["variant"] == "cloud",
				Description: im.Properties["description"],
				Arch:        im.Architecture,
				SizeBytes:   im.Size,
				Local:       local,
			},
		}
	}

	// Local images first (so a local build wins ties against an equal-dated catalog serial).
	if local, err := c.server.GetImages(); err == nil {
		for i := range local {
			consider(&local[i], true)
		}
	}

	// Remote simplestreams catalog — best-effort so a network failure doesn't hide local images.
	var remoteErr error
	if is, err := c.imageServer(); err != nil {
		remoteErr = err
	} else if raw, err := is.GetImages(); err != nil {
		remoteErr = fmt.Errorf("listing images: %w", err)
	} else {
		for i := range raw {
			consider(&raw[i], false)
		}
	}
	if len(best) == 0 && remoteErr != nil {
		return nil, remoteErr // nothing to show and the catalog failed → surface why
	}

	out := make([]Image, 0, len(best))
	for _, e := range best {
		out = append(out, e.img)
	}
	sort.Slice(out, func(i, j int) bool {
		// Local images first (the user explicitly imported them), then cloud, then by label.
		if out[i].Local != out[j].Local {
			return out[i].Local
		}
		if out[i].Cloud != out[j].Cloud {
			return out[i].Cloud
		}
		return out[i].Alias < out[j].Alias
	})
	return out, nil
}

// productKey identifies an image product independent of its daily-build serial, so the
// serials of one product collapse together. It falls back to the fingerprint when the
// simplestreams properties are absent, so a metadata-less image is never merged with
// an unrelated one.
func productKey(im *api.Image) string {
	p := im.Properties
	os, rel := p["os"], p["release"]
	if os == "" || rel == "" {
		return "fp:" + im.Fingerprint
	}
	return strings.ToLower(os + "/" + rel + "/" + p["variant"] + "/" + im.Architecture)
}

// imageLabel returns the best display label for an image: its primary alias if it
// has one, otherwise a name built from its properties (most simplestreams VM images
// are aliased on a separate object and arrive here without an alias of their own).
func imageLabel(im *api.Image) string {
	if a := primaryAlias(im); a != "" {
		return a
	}
	p := im.Properties
	parts := make([]string, 0, 3)
	for _, k := range []string{"os", "release", "variant"} {
		if v := p[k]; v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) > 0 {
		// Lowercase so a property-built label (e.g. "Almalinux/10/cloud") matches the
		// alias-style convention ("almalinux/10/cloud") used elsewhere.
		return strings.ToLower(strings.Join(parts, "/"))
	}
	if len(im.Fingerprint) >= 12 {
		return im.Fingerprint[:12]
	}
	return im.Fingerprint
}

func primaryAlias(im *api.Image) string {
	if len(im.Aliases) == 0 {
		return ""
	}
	names := make([]string, 0, len(im.Aliases))
	for _, a := range im.Aliases {
		names = append(names, a.Name)
	}
	sort.Slice(names, func(i, j int) bool {
		// Prefer the more specific "/cloud" alias if present, then shortest.
		ci, cj := strings.Contains(names[i], "/cloud"), strings.Contains(names[j], "/cloud")
		if ci != cj {
			return ci
		}
		return len(names[i]) < len(names[j])
	})
	return names[0]
}
