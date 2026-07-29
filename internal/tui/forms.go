package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/huh/v2"

	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/units"
	xincus "github.com/venkatamutyala/incus-tui/internal/incus"
)

// blankCloudInitScaffold pre-fills the editor when the user picks "(blank)", teaching the
// shape of a cloud-config. It's all comments, so launching it as-is is a harmless no-op.
const blankCloudInitScaffold = `#cloud-config
# Edit this, or press ctrl+s to launch as-is (an all-comment config does nothing).
# Uncomment what you need:
# package_update: true
# packages:
#   - htop
#   - curl
# runcmd:
#   - echo "hello from cloud-init" > /etc/motd
`

type formKind int

const (
	formNone formKind = iota
	formLaunch
	formEdit
	formSnapManage
	formDelete
)

// formVars holds form-bound values on the heap so huh's pointer bindings stay valid
// across the Bubble Tea model's value copies (the model is passed by value each Update).
type formVars struct {
	name    string
	cpu     string
	mem     string
	disk    string
	imageFP string
	cloud   string
	action  string // snapshot manager: "create" | "restore:<snap>" | "delete:<snap>"
	confirm bool

	diskSeed string // edit form: the disk field's seeded current size, to detect a real change
}

// applyEscKeymap makes esc abort a form (back to the list). esc is consumed by a field
// first (e.g. clearing a Select's filter), so it only aborts at the top level.
func applyEscKeymap(f *huh.Form) *huh.Form {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))
	return f.WithKeyMap(km)
}

func newLaunchForm(images []xincus.Image, templates []xincus.Template, v *formVars, names []string) *huh.Form {
	// Images are normally pre-filtered to the host architecture, so repeating the arch
	// on every row is just clutter down the right edge. Show it once in the title and
	// keep it per-row only if the list is somehow mixed.
	arch, mixed := "", false
	for i, im := range images {
		if i == 0 {
			arch = im.Arch
		} else if im.Arch != arch {
			mixed = true
		}
	}
	imgOpts := make([]huh.Option[string], 0, len(images))
	for _, im := range images {
		label := im.Alias
		if label == "" {
			label = im.Fingerprint[:min(12, len(im.Fingerprint))]
		}
		if mixed && im.Arch != "" {
			label = fmt.Sprintf("%-30s %s", label, im.Arch)
		}
		// Don't tag non-cloud rows per-line — it repeats down the whole list. The field
		// description below explains that cloud images are the ones with the guest agent.
		imgOpts = append(imgOpts, huh.NewOption(label, im.Fingerprint))
	}
	imgTitle := "Image (type to filter)"
	if !mixed && arch != "" {
		imgTitle = "Image · " + arch + " (type to filter)"
	}

	known := map[string]bool{"": true}
	tmplOpts := []huh.Option[string]{huh.NewOption("(blank)", "")}
	for _, t := range templates {
		tmplOpts = append(tmplOpts, huh.NewOption(t.Name, t.Content))
		known[t.Content] = true
	}
	// Preserve edits made in the cloud-init editor across an "esc back to form".
	if v.cloud != "" && !known[v.cloud] {
		tmplOpts = append([]huh.Option[string]{huh.NewOption("(current edits)", v.cloud)}, tmplOpts...)
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Key("name").Title("VM name").
				Value(&v.name).Validate(uniqueVMName(names)),
			huh.NewSelect[string]().Key("image").Title(imgTitle).
				Description("cloud images ship the guest agent (needed for shell, IP, cloud-init)").
				Options(imgOpts...).Value(&v.imageFP).Filtering(true).Height(10),
		),
		huh.NewGroup(
			huh.NewInput().Key("cpu").Title("vCPUs").Placeholder("2").Value(&v.cpu).Validate(validateCPU),
			huh.NewInput().Key("mem").Title("Memory (MiB)").Placeholder("2048").Value(&v.mem).Validate(validateSize),
			huh.NewInput().Key("disk").Title("Disk (GiB)").Placeholder("12").Value(&v.disk).Validate(validateSize),
		),
		huh.NewGroup(
			huh.NewSelect[string]().Key("tmpl").
				Title("cloud-init template (you can edit it next)").
				Options(tmplOpts...).Value(&v.cloud),
		),
	)
	return applyEscKeymap(form)
}

func newEditForm(vm xincus.VM) (*huh.Form, *formVars) {
	// normalizeByteSize renders the seed unit-bearing so an untouched value isn't rescaled
	// by withUnit on submit.
	v := &formVars{cpu: vm.CPULimit, mem: normalizeByteSize(vm.MemLimit)}
	fields := []huh.Field{
		huh.NewInput().Key("cpu").Title("vCPUs").
			Placeholder("e.g. 2").Value(&v.cpu).Validate(validateCPU),
		huh.NewInput().Key("mem").Title("Memory (MiB or 2GiB)").
			Placeholder("e.g. 2048").Value(&v.mem).Validate(validateSize),
	}
	// A disk resize needs the VM stopped, so only offer the editable field then; a running
	// VM gets an informational note instead. This is what keeps a running-VM edit from
	// silently dropping the user's cpu/ram changes just because they also touched the disk.
	if vm.StatusCode == api.Stopped {
		v.disk = normalizeByteSize(vm.DiskSize)
		v.diskSeed = v.disk // remember the seed so an untouched submit doesn't pin it as an override
		fields = append(fields, huh.NewInput().Key("disk").Title("Disk (GiB)").
			Description("Grow only — can't shrink. Applies on the next start.").
			Placeholder("e.g. 20GiB").Value(&v.disk).Validate(growOnlyValidator(v.diskSeed)))
	} else {
		fields = append(fields, huh.NewNote().Title("Disk").
			Description("Stop the VM to resize its root disk (grow only)."))
	}
	return applyEscKeymap(huh.NewForm(huh.NewGroup(fields...))), v
}

// growOnlyValidator returns a size-field validator that also rejects a value below the
// seeded current size, so a shrink is caught inline instead of after a daemon round-trip.
// The service layer re-checks authoritatively (SetResources → growOnly).
func growOnlyValidator(current string) func(string) error {
	return func(s string) error {
		if err := validateSize(s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" || current == "" {
			return nil // blank leaves it unchanged; an unknown current size can't be compared
		}
		cur, err1 := units.ParseByteSizeString(current)
		req, err2 := units.ParseByteSizeString(withUnit(s, "GiB"))
		if err1 == nil && err2 == nil && req < cur {
			return fmt.Errorf("grow only: %s is smaller than the current %s", withUnit(s, "GiB"), current)
		}
		return nil
	}
}

func newSnapManageForm(vm xincus.VM) (*huh.Form, *formVars) {
	v := &formVars{action: "create"}
	opts := []huh.Option[string]{huh.NewOption("Create a new snapshot", "create")}
	for _, s := range vm.Snapshots {
		opts = append(opts, huh.NewOption("Restore → "+s, "restore:"+s))
		opts = append(opts, huh.NewOption("Delete → "+s, "delete:"+s))
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Key("action").
				Title("Snapshots — "+vm.Name).Options(opts...).Value(&v.action),
		),
		huh.NewGroup(
			huh.NewInput().Key("name").Title("New snapshot name").
				Value(&v.name).Validate(huh.ValidateNotEmpty()),
		).WithHideFunc(func() bool { return v.action != "create" }),
		huh.NewGroup(
			huh.NewConfirm().Key("ok").
				TitleFunc(func() string {
					switch {
					case strings.HasPrefix(v.action, "restore:"):
						return fmt.Sprintf("Restore %q to snapshot %q? Discards all changes since the snapshot. Cannot be undone.",
							vm.Name, strings.TrimPrefix(v.action, "restore:"))
					case strings.HasPrefix(v.action, "delete:"):
						return fmt.Sprintf("Delete snapshot %q of %q? Cannot be undone.",
							strings.TrimPrefix(v.action, "delete:"), vm.Name)
					}
					return "Apply this snapshot action?"
				}, &v.action).
				Affirmative("Yes").Negative("No").Value(&v.confirm),
		).WithHideFunc(func() bool { return v.action == "create" }),
	)
	return applyEscKeymap(form), v
}

func newDeleteForm(vm xincus.VM) (*huh.Form, *formVars) {
	v := &formVars{}
	title := fmt.Sprintf("Delete VM %q? This is irreversible.", vm.Name)
	// Client.Delete force-stops (hard power-off, no graceful shutdown) anything that isn't
	// already Stopped/Error — disclose that instead of pretending it's a clean delete.
	if vm.StatusCode != api.Stopped && vm.StatusCode != api.Error {
		title = fmt.Sprintf("Delete VM %q? It is %s and will be force-stopped (no graceful shutdown), then deleted. Irreversible.",
			vm.Name, strings.ToLower(vm.Status))
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Key("ok").
			Title(title).
			Affirmative("Delete").Negative("Cancel").Value(&v.confirm),
	))
	return applyEscKeymap(form), v
}

// --- validators --------------------------------------------------------------

func validateVMName(s string) error {
	if s == "" {
		return fmt.Errorf("name is required")
	}
	if len(s) > 63 {
		return fmt.Errorf("name must be 63 characters or fewer")
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok {
			return fmt.Errorf("use lowercase letters, digits and hyphens only")
		}
	}
	if strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return fmt.Errorf("name cannot start or end with a hyphen")
	}
	return nil
}

// uniqueVMName wraps validateVMName and additionally rejects a name already in use, so
// the collision is caught inline in the wizard instead of failing the whole launch.
func uniqueVMName(existing []string) func(string) error {
	set := make(map[string]bool, len(existing))
	for _, n := range existing {
		set[n] = true
	}
	return func(s string) error {
		if err := validateVMName(s); err != nil {
			return err
		}
		if set[s] {
			return fmt.Errorf("a VM named %q already exists", s)
		}
		return nil
	}
}

func validateCPU(s string) error {
	if s == "" {
		return nil
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return fmt.Errorf("vCPUs must be a whole number")
		}
	}
	return nil
}

// Whole numbers only, no embedded space, and IEC units only. Every size field advertises
// IEC (MiB/GiB), and a bare number is read in the field's IEC unit — so we deliberately
// reject SI units (kB/MB/GB/TB) too: "10GB" (=10^9 B ≈ 9.31GiB) would be smaller than a
// "10GiB" label implies and, for the grow-only disk field, would read as a confusing
// shrink. Incus also rejects decimals ("1.5GiB") and spaces ("2 GiB"), so the form must too.
var (
	sizeRe  = regexp.MustCompile(`^\d+(B|KiB|MiB|GiB|TiB)?$`)
	bareNum = regexp.MustCompile(`^\d+$`)
)

func validateSize(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if !sizeRe.MatchString(strings.TrimSpace(s)) {
		return fmt.Errorf("use a whole number, or a size like 2GiB / 512MiB (no spaces or decimals)")
	}
	return nil
}

// diskResizeArg returns the size to pass to SetResources for the edit form's disk
// field: "" (leave the root device untouched) when the field is blank or still equals
// the seeded current size, otherwise the unit-normalized new size. Because the field is
// seeded with the current size, this keeps an edit that only changed cpu/ram from
// rewriting — and thereby pinning as an instance override — a profile-inherited disk.
func diskResizeArg(seed, field string) string {
	field = strings.TrimSpace(field)
	if field == "" || field == strings.TrimSpace(seed) {
		return ""
	}
	// A bare number is read as GiB (root disks are GiB-scale); a unit-bearing value passes through.
	return withUnit(field, "GiB")
}

// withUnit appends defUnit to a bare whole-number size (e.g. "2048" → "2048MiB") so a
// plain number is read in the unit the field advertises — Incus treats a unit-less value
// as bytes. Values that already carry a unit (and the empty string) pass through.
func withUnit(s, defUnit string) string {
	s = strings.TrimSpace(s)
	if s != "" && bareNum.MatchString(s) {
		return s + defUnit
	}
	return s
}

// normalizeByteSize renders a unit-less byte count (e.g. a limits.memory or disk size set
// out-of-band, which is valid in Incus) as a unit-bearing IEC string, so an edit field never
// starts as a bare number that withUnit would later rescale by 1024² when re-submitted
// untouched. It is unit-agnostic (bytes → the largest clean IEC unit), used for mem and disk.
func normalizeByteSize(s string) string {
	s = strings.TrimSpace(s)
	if !bareNum.MatchString(s) {
		return s // already has a unit (or is empty/odd) — leave it
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return s
	}
	const mib = 1024 * 1024
	switch {
	case n%(1024*mib) == 0:
		return fmt.Sprintf("%dGiB", n/(1024*mib))
	case n%mib == 0:
		return fmt.Sprintf("%dMiB", n/mib)
	default:
		return fmt.Sprintf("%dB", n) // exact bytes, but unit-bearing so withUnit leaves it
	}
}
