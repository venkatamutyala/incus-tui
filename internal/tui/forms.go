package tui

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/huh/v2"

	xincus "github.com/venkatamutyala/incus-tui/internal/incus"
)

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
}

// applyEscKeymap makes esc abort a form (back to the list). esc is consumed by a field
// first (e.g. clearing a Select's filter), so it only aborts at the top level.
func applyEscKeymap(f *huh.Form) *huh.Form {
	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))
	return f.WithKeyMap(km)
}

func newLaunchForm(images []xincus.Image, templates []xincus.Template, v *formVars) *huh.Form {
	imgOpts := make([]huh.Option[string], 0, len(images))
	for _, im := range images {
		label := im.Alias
		if label == "" {
			label = im.Fingerprint[:min(12, len(im.Fingerprint))]
		}
		if im.Arch != "" {
			label = fmt.Sprintf("%-30s %s", label, im.Arch)
		}
		imgOpts = append(imgOpts, huh.NewOption(label, im.Fingerprint))
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
				Value(&v.name).Validate(validateVMName),
			huh.NewSelect[string]().Key("image").Title("Image (type to filter)").
				Options(imgOpts...).Value(&v.imageFP).Filtering(true).Height(10),
		),
		huh.NewGroup(
			huh.NewInput().Key("cpu").Title("vCPUs (limits.cpu)").Value(&v.cpu).Validate(validateCPU),
			huh.NewInput().Key("mem").Title("Memory (limits.memory)").Value(&v.mem).Validate(validateSize),
			huh.NewInput().Key("disk").Title("Root disk size").Value(&v.disk).Validate(validateSize),
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
	v := &formVars{cpu: vm.CPULimit, mem: vm.MemLimit}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Key("cpu").Title("vCPUs (limits.cpu)").
			Placeholder("e.g. 2").Value(&v.cpu).Validate(validateCPU),
		huh.NewInput().Key("mem").Title("Memory (limits.memory)").
			Placeholder("e.g. 2GiB").Value(&v.mem).Validate(validateSize),
	))
	return applyEscKeymap(form), v
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
			huh.NewConfirm().Key("ok").Title("Apply this snapshot action?").
				Affirmative("Yes").Negative("No").Value(&v.confirm),
		).WithHideFunc(func() bool { return v.action == "create" }),
	)
	return applyEscKeymap(form), v
}

func newDeleteForm(name string) (*huh.Form, *formVars) {
	v := &formVars{}
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Key("ok").
			Title(fmt.Sprintf("Delete VM %q? This is irreversible.", name)).
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

var sizeRe = regexp.MustCompile(`^\d+(\.\d+)?\s*(B|kB|KB|KiB|MB|MiB|GB|GiB|TB|TiB)?$`)

func validateSize(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	if !sizeRe.MatchString(strings.TrimSpace(s)) {
		return fmt.Errorf("use a size like 2GiB or 512MiB")
	}
	return nil
}
