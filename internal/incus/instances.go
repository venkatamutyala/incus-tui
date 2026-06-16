package incus

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

// VM is a flattened, UI-friendly view of an Incus virtual machine. It deliberately
// contains only plain values so it can be copied across the goroutine boundary into
// the Bubble Tea update loop without sharing state.
type VM struct {
	Name       string
	Status     string         // "Running", "Stopped", "Frozen", ...
	StatusCode api.StatusCode // typed status for color/logic decisions
	Type       string         // always "virtual-machine" here
	IPv4       string         // primary global IPv4, or "" if none yet
	Image      string         // human description of the source image
	CreatedAt  time.Time
	CPULimit   string // limits.cpu (cores), or "" if unset
	MemLimit   string // limits.memory, or "" if unset

	// Live metrics (only meaningful once the guest agent is up).
	CPUUsageNS  int64
	MemoryUsage int64
	MemoryTotal int64
	AgentReady  bool

	Snapshots []string
}

// Running reports whether the VM is currently running.
func (v VM) Running() bool { return v.StatusCode == api.Running }

// Age returns how long ago the VM was created.
func (v VM) Age() time.Duration {
	if v.CreatedAt.IsZero() {
		return 0
	}
	return time.Since(v.CreatedAt)
}

// ListVMs returns all virtual machines with their live state in a single round-trip.
func (c *Client) ListVMs() ([]VM, error) {
	full, err := c.server.GetInstancesFull(api.InstanceTypeVM)
	if err != nil {
		return nil, fmt.Errorf("listing VMs: %w", err)
	}
	vms := make([]VM, 0, len(full))
	for i := range full {
		vms = append(vms, toVM(&full[i]))
	}
	sort.Slice(vms, func(i, j int) bool { return vms[i].Name < vms[j].Name })
	return vms, nil
}

// GetVM returns a single VM (used for the detail pane / after an action).
func (c *Client) GetVM(name string) (VM, error) {
	inst, _, err := c.server.GetInstanceFull(name)
	if err != nil {
		return VM{}, fmt.Errorf("getting VM %q: %w", name, err)
	}
	return toVM(inst), nil
}

func toVM(f *api.InstanceFull) VM {
	vm := VM{
		Name:       f.Name,
		Status:     f.Status,
		StatusCode: f.StatusCode,
		Type:       f.Type,
		CreatedAt:  f.CreatedAt,
		CPULimit:   f.Config["limits.cpu"],
		MemLimit:   f.Config["limits.memory"],
		Image:      imageDescription(f.Config),
	}
	if f.State != nil {
		vm.IPv4 = primaryIPv4(f.State)
		vm.CPUUsageNS = f.State.CPU.Usage
		vm.MemoryUsage = f.State.Memory.Usage
		vm.MemoryTotal = f.State.Memory.Total
		// For a VM, Processes is reported by the in-guest agent and stays -1 until
		// the agent connects, so a RUNNING VM is not necessarily exec-able yet.
		vm.AgentReady = vm.Running() && f.State.Processes > 0
	}
	for _, s := range f.Snapshots {
		vm.Snapshots = append(vm.Snapshots, s.Name)
	}
	sort.Strings(vm.Snapshots)
	return vm
}

// imageDescription builds a readable source-image label from instance config.
func imageDescription(cfg map[string]string) string {
	if d := cfg["image.description"]; d != "" {
		return d
	}
	os, rel := cfg["image.os"], cfg["image.release"]
	switch {
	case os != "" && rel != "":
		return strings.TrimSpace(os + " " + rel)
	case os != "":
		return os
	default:
		return cfg["image.architecture"]
	}
}

// primaryIPv4 returns the first global IPv4 address across non-loopback NICs.
func primaryIPv4(st *api.InstanceState) string {
	// Stable iteration: sort device names so the chosen IP doesn't flap.
	devs := make([]string, 0, len(st.Network))
	for dev := range st.Network {
		devs = append(devs, dev)
	}
	sort.Strings(devs)
	for _, dev := range devs {
		if dev == "lo" {
			continue
		}
		for _, a := range st.Network[dev].Addresses {
			if a.Family == "inet" && a.Scope == "global" {
				return a.Address
			}
		}
	}
	return ""
}

// --- Lifecycle ---------------------------------------------------------------

func (c *Client) setState(ctx context.Context, name, action string, force bool) error {
	op, err := c.server.UpdateInstanceState(name, api.InstanceStatePut{
		Action:  action,
		Timeout: -1, // bounded graceful timeout (daemon default ~600s), never infinite
		Force:   force,
	}, "")
	if err != nil {
		return fmt.Errorf("%s %q: %w", action, name, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("%s %q: %w", action, name, err)
	}
	return nil
}

// Start, Stop, Restart, Freeze and Unfreeze drive the VM power state. Stop/Restart
// default to a GRACEFUL shutdown (Timeout:-1 bounds it); use ForceStop for a hard kill.
func (c *Client) Start(ctx context.Context, name string) error {
	return c.setState(ctx, name, "start", false)
}
func (c *Client) Stop(ctx context.Context, name string) error {
	return c.setState(ctx, name, "stop", false)
}
func (c *Client) ForceStop(ctx context.Context, name string) error {
	return c.setState(ctx, name, "stop", true)
}
func (c *Client) Restart(ctx context.Context, name string) error {
	return c.setState(ctx, name, "restart", false)
}
func (c *Client) Freeze(ctx context.Context, name string) error {
	return c.setState(ctx, name, "freeze", false)
}
func (c *Client) Unfreeze(ctx context.Context, name string) error {
	return c.setState(ctx, name, "unfreeze", false)
}

// Delete removes a VM, stopping it first if the daemon would reject the delete. The
// daemon rejects deletion of anything that isn't Stopped or Error (so Frozen counts
// as running too), hence the guard is "not stopped and not errored".
func (c *Client) Delete(ctx context.Context, name string) error {
	state, _, err := c.server.GetInstanceState(name)
	if err != nil {
		return fmt.Errorf("deleting %q: checking state: %w", name, err)
	}
	if state.StatusCode != api.Stopped && state.StatusCode != api.Error {
		if err := c.ForceStop(ctx, name); err != nil {
			return fmt.Errorf("deleting %q: stopping first: %w", name, err)
		}
	}
	op, err := c.server.DeleteInstance(name)
	if err != nil {
		return fmt.Errorf("deleting %q: %w", name, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("deleting %q: %w", name, err)
	}
	return nil
}

// SetLimits updates limits.cpu and/or limits.memory on an existing VM, preserving
// the rest of the config via a read-modify-write with the current ETag.
func (c *Client) SetLimits(ctx context.Context, name, cpu, mem string) error {
	inst, etag, err := c.server.GetInstance(name)
	if err != nil {
		return fmt.Errorf("reading %q config: %w", name, err)
	}
	put := inst.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	if cpu != "" {
		put.Config["limits.cpu"] = cpu
	}
	if mem != "" {
		put.Config["limits.memory"] = mem
	}
	op, err := c.server.UpdateInstance(name, put, etag)
	if err != nil {
		return fmt.Errorf("updating %q limits: %w", name, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("updating %q limits: %w", name, err)
	}
	return nil
}

// --- Snapshots ---------------------------------------------------------------

// Snapshot creates a (stateless) snapshot of a VM.
func (c *Client) Snapshot(ctx context.Context, name, snapshot string) error {
	op, err := c.server.CreateInstanceSnapshot(name, api.InstanceSnapshotsPost{Name: snapshot})
	if err != nil {
		return fmt.Errorf("snapshotting %q: %w", name, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("snapshotting %q: %w", name, err)
	}
	return nil
}

// RestoreSnapshot restores a VM to a named snapshot. Incus has no dedicated restore
// call; it is an instance update with the Restore field, so we read-modify-write.
func (c *Client) RestoreSnapshot(ctx context.Context, name, snapshot string) error {
	inst, etag, err := c.server.GetInstance(name)
	if err != nil {
		return fmt.Errorf("reading %q: %w", name, err)
	}
	put := inst.Writable()
	put.Restore = snapshot
	op, err := c.server.UpdateInstance(name, put, etag)
	if err != nil {
		return fmt.Errorf("restoring %q@%s: %w", name, snapshot, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("restoring %q@%s: %w", name, snapshot, err)
	}
	return nil
}

// DeleteSnapshot removes a snapshot from a VM.
func (c *Client) DeleteSnapshot(ctx context.Context, name, snapshot string) error {
	op, err := c.server.DeleteInstanceSnapshot(name, snapshot)
	if err != nil {
		return fmt.Errorf("deleting snapshot %q@%s: %w", name, snapshot, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("deleting snapshot %q@%s: %w", name, snapshot, err)
	}
	return nil
}

// --- Create ------------------------------------------------------------------

// CreateSpec describes a VM to launch.
type CreateSpec struct {
	Name             string
	ImageFingerprint string // preferred (robust); falls back to ImageAlias
	ImageAlias       string
	CPU              string // limits.cpu, optional
	Memory           string // limits.memory, optional
	DiskSize         string // root disk size (e.g. "12GiB"), optional
	CloudInitUser    string // cloud-init.user-data, optional
}

// CreateVM launches a new virtual machine from a remote image, applying cloud-init
// and limits. It returns once the create+start operation completes (the guest may
// still be booting / running cloud-init afterwards).
func (c *Client) CreateVM(ctx context.Context, spec CreateSpec) error {
	cfg := map[string]string{
		// Most cloud images declare image.requirements.secureboot=false and fail to
		// start under secure boot; disabling it keeps the common case working.
		"security.secureboot": "false",
	}
	if spec.CPU != "" {
		cfg["limits.cpu"] = spec.CPU
	}
	if spec.Memory != "" {
		cfg["limits.memory"] = spec.Memory
	}
	if spec.CloudInitUser != "" {
		cfg["cloud-init.user-data"] = spec.CloudInitUser
	}

	src := api.InstanceSource{
		Type:     "image",
		Server:   ImageServerURL,
		Protocol: ImageProtocol,
	}
	if spec.ImageFingerprint != "" {
		src.Fingerprint = spec.ImageFingerprint
	} else {
		src.Alias = spec.ImageAlias
	}

	post := api.InstancesPost{
		Name:   spec.Name,
		Type:   api.InstanceTypeVM,
		Source: src,
		Start:  true,
		InstancePut: api.InstancePut{
			Config: cfg,
		},
	}
	// A root-device override replaces the profile's root device entirely, so copy it
	// (preserving the real pool/path) and override only the size. This makes the tool
	// storage-agnostic instead of assuming a pool literally named "default". If a size
	// was requested but the root device can't be resolved, fail loudly rather than
	// silently launching at the default size.
	if spec.DiskSize != "" {
		root, err := c.rootDevice()
		if err != nil {
			return fmt.Errorf("creating VM %q: setting disk size: %w", spec.Name, err)
		}
		root["size"] = spec.DiskSize
		post.Devices = map[string]map[string]string{"root": root}
	}

	op, err := c.server.CreateInstance(post)
	if err != nil {
		return fmt.Errorf("creating VM %q: %w", spec.Name, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("creating VM %q: %w", spec.Name, err)
	}
	return nil
}

// rootDevice returns a copy of the default profile's root disk device (incl. its
// pool), so a caller can override just the size while preserving the configured pool.
func (c *Client) rootDevice() (map[string]string, error) {
	prof, _, err := c.server.GetProfile("default")
	if err != nil {
		return nil, fmt.Errorf("reading default profile: %w", err)
	}
	root, ok := prof.Devices["root"]
	if !ok {
		return nil, fmt.Errorf("default profile has no root disk device to size")
	}
	out := make(map[string]string, len(root)+1)
	for k, v := range root {
		out[k] = v
	}
	return out, nil
}

// hostArch returns the daemon's kernel architecture in canonical form (e.g. x86_64),
// or "" if it can't be determined.
func (c *Client) hostArch() string {
	srv, _, err := c.server.GetServer()
	if err != nil {
		return ""
	}
	return normalizeArch(srv.Environment.KernelArchitecture)
}

// normalizeArch maps the various spellings to a single canonical name.
func normalizeArch(a string) string {
	switch a {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return a
	}
}
