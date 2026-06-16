package incus

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lxc/incus/v7/shared/api"
)

// Image is a flattened, UI-friendly view of a VM-capable remote image.
type Image struct {
	Fingerprint string
	Alias       string // a display label: real alias if any, else os/release/variant
	Cloud       bool   // cloud variant (ships the guest agent → exec/cloud-init work)
	Description string
	Arch        string
	SizeBytes   int64
}

// ListVMImages returns VM-capable images from the public image server that match the
// host architecture, preferring cloud variants and sorted for stable browsing.
func (c *Client) ListVMImages() ([]Image, error) {
	is, err := c.imageServer()
	if err != nil {
		return nil, err
	}
	raw, err := is.GetImages()
	if err != nil {
		return nil, fmt.Errorf("listing images: %w", err)
	}
	host := c.hostArch() // "" if undetermined → don't filter

	out := make([]Image, 0, len(raw))
	for i := range raw {
		im := &raw[i]
		if im.Type != "virtual-machine" {
			continue
		}
		if host != "" && normalizeArch(im.Architecture) != host {
			continue // an image of another arch can't boot here
		}
		out = append(out, Image{
			Fingerprint: im.Fingerprint,
			Alias:       imageLabel(im),
			Cloud:       im.Properties["variant"] == "cloud",
			Description: im.Properties["description"],
			Arch:        im.Architecture,
			SizeBytes:   im.Size,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		// Cloud images first (they ship the agent), then by label.
		if out[i].Cloud != out[j].Cloud {
			return out[i].Cloud
		}
		return out[i].Alias < out[j].Alias
	})
	return out, nil
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
		return strings.Join(parts, "/")
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
