package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/lxc/incus/v7/shared/api"
	xincus "github.com/venkatamutyala/incus-tui/internal/incus"
)

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "-"}, {-5, "-"}, {512, "512B"}, {1024, "1.0KiB"},
		{1536, "1.5KiB"}, {1048576, "1.0MiB"}, {1073741824, "1.0GiB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "-"}, {-time.Second, "-"}, {30 * time.Second, "30s"},
		{5 * time.Minute, "5m"}, {3 * time.Hour, "3h"}, {50 * time.Hour, "2d"},
	}
	for _, c := range cases {
		if got := formatAge(c.d); got != c.want {
			t.Errorf("formatAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestVisibleColsDropsAndStaysAligned(t *testing.T) {
	if cols := visibleCols(200); len(cols) != 7 {
		t.Errorf("visibleCols(200) = %d columns, want all 7", len(cols))
	}
	cols := visibleCols(40)
	if len(cols) >= 7 {
		t.Fatalf("visibleCols(40) = %d columns, want fewer", len(cols))
	}
	if cols[0].title != "NAME" || cols[1].title != "STATUS" {
		t.Errorf("first columns = %q,%q, want NAME,STATUS", cols[0].title, cols[1].title)
	}
	// Each kept column's cell function must still match its title (no index drift).
	vm := xincus.VM{Name: "x", Status: "Running", IPv4: "1.2.3.4"}
	for _, c := range cols {
		switch c.title {
		case "NAME":
			if c.cell(vm) != "x" {
				t.Errorf("NAME cell = %q, want x", c.cell(vm))
			}
		case "IPV4":
			if c.cell(vm) != "1.2.3.4" {
				t.Errorf("IPV4 cell = %q, want 1.2.3.4", c.cell(vm))
			}
		}
	}
}

func TestWithUnit(t *testing.T) {
	cases := []struct{ in, unit, want string }{
		{"2048", "MiB", "2048MiB"}, // bare int → unit appended
		{"12", "GiB", "12GiB"},
		{"1.5", "GiB", "1.5"},        // decimal is NOT a bare int → unchanged (validateSize rejects it inline)
		{"2GiB", "MiB", "2GiB"},      // already has a unit → unchanged
		{"512MiB", "GiB", "512MiB"},  // already has a unit → unchanged
		{" 1024 ", "MiB", "1024MiB"}, // trimmed
		{"", "MiB", ""},              // empty stays empty (omitted limit)
		{"abc", "MiB", "abc"},        // non-numeric passes through (validator rejects upstream)
	}
	for _, c := range cases {
		if got := withUnit(c.in, c.unit); got != c.want {
			t.Errorf("withUnit(%q,%q) = %q, want %q", c.in, c.unit, got, c.want)
		}
	}
}

func TestClampLines(t *testing.T) {
	if got := clampLines("a\nb\nc\nd", 2); got != "a\nb" {
		t.Errorf("clampLines 4->2 = %q, want \"a\\nb\"", got)
	}
	if got := clampLines("a\nb", 5); got != "a\nb" {
		t.Errorf("clampLines fewer-than-n = %q, want unchanged", got)
	}
	if got := clampLines("a\nb\nc", 0); got != "a" { // n<1 floored to 1
		t.Errorf("clampLines n=0 = %q, want \"a\"", got)
	}
}

func TestFormWidth(t *testing.T) {
	// content + box border/padding (4) must never exceed the terminal width.
	for _, w := range []int{24, 30, 50, 80, 200} {
		if fw := formWidth(w); fw+4 > w {
			t.Errorf("formWidth(%d)=%d → box %d overflows terminal %d", w, fw, fw+4, w)
		}
	}
	if formWidth(10) < 20 { // floor keeps the form usable on tiny terminals
		t.Errorf("formWidth floor not applied: %d", formWidth(10))
	}
}

func TestUniqueVMName(t *testing.T) {
	v := uniqueVMName([]string{"web", "db"})
	if err := v("web"); err == nil {
		t.Error("expected duplicate name to be rejected")
	}
	if err := v("cache"); err != nil {
		t.Errorf("unique valid name rejected: %v", err)
	}
	if err := v("Web"); err == nil { // uppercase fails the base validator
		t.Error("expected invalid charset to be rejected")
	}
}

func TestMemCell(t *testing.T) {
	cases := []struct {
		v    xincus.VM
		want string
	}{
		{xincus.VM{AgentReady: false}, "-"},
		{xincus.VM{AgentReady: true, MemoryUsage: 0}, "-"},
		{xincus.VM{AgentReady: true, MemoryUsage: 512, MemoryTotal: 2048}, "25%"},
		{xincus.VM{AgentReady: true, MemoryUsage: 1024, MemoryTotal: 0}, "1.0KiB"}, // no total → absolute
	}
	for _, c := range cases {
		if got := memCell(c.v); got != c.want {
			t.Errorf("memCell(%+v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestValidateSize(t *testing.T) {
	ok := []string{"", "2048", "12", "2GiB", "512MiB", "4TiB"}
	// SI units are rejected: fields advertise IEC (MiB/GiB), and "10GB" ≠ "10GiB".
	bad := []string{"1.5", "1.5GiB", "2 GiB", "2gib", "abc", "12GiBx", "100GB", "10MB", "1kB"}
	for _, s := range ok {
		if err := validateSize(s); err != nil {
			t.Errorf("validateSize(%q) rejected, want accepted: %v", s, err)
		}
	}
	for _, s := range bad {
		if err := validateSize(s); err == nil {
			t.Errorf("validateSize(%q) accepted, want rejected (Incus would reject it)", s)
		}
	}
}

func TestNormalizeByteSize(t *testing.T) {
	cases := map[string]string{
		"2147483648": "2GiB",        // 2 GiB in bytes → readable, and unit-bearing so withUnit won't rescale
		"536870912":  "512MiB",      // 512 MiB in bytes
		"1500000000": "1500000000B", // not cleanly divisible → exact bytes, still unit-bearing
		"2GiB":       "2GiB",        // already has a unit → unchanged
		"":           "",            // empty stays empty
	}
	for in, want := range cases {
		if got := normalizeByteSize(in); got != want {
			t.Errorf("normalizeByteSize(%q) = %q, want %q", in, got, want)
		}
		// And the key property: re-applying withUnit to the normalized value is a no-op.
		if n := normalizeByteSize(in); n != "" && withUnit(n, "MiB") != n {
			t.Errorf("withUnit(normalizeByteSize(%q)) rescaled %q → %q", in, n, withUnit(n, "MiB"))
		}
	}
}

// Pins the launch form → CreateSpec contract: bare numbers get the field's unit, the rest
// map straight through. A regression here would silently mis-size every launched VM.
func TestLaunchSpecMapping(t *testing.T) {
	m := testModel()
	m.formKind = formLaunch
	m.vars = &formVars{name: "vm1", imageFP: "fp123", cpu: "2", mem: "2048", disk: "12", cloud: "#cloud-config\n"}
	m2, _ := m.completeForm()
	spec := m2.(model).pendingLaunch
	if spec.Name != "vm1" || spec.ImageFingerprint != "fp123" || spec.CPU != "2" {
		t.Errorf("name/image/cpu mismatch: %+v", spec)
	}
	if spec.Memory != "2048MiB" {
		t.Errorf("Memory = %q, want 2048MiB", spec.Memory)
	}
	if spec.DiskSize != "12GiB" {
		t.Errorf("DiskSize = %q, want 12GiB", spec.DiskSize)
	}
}

// Pins the edit form's seed → submit contract for the disk field: the current disk
// size seeds the field unit-bearing (so an untouched value isn't rescaled), and a
// bare number typed in gets the GiB unit on the way to SetResources. A regression here
// would silently mis-size a resized disk.
func TestEditFormDiskSeedAndMapping(t *testing.T) {
	vm := xincus.VM{StatusCode: api.Stopped, CPULimit: "2", MemLimit: "2147483648", DiskSize: "10GiB"}
	_, v := newEditForm(vm)
	if v.cpu != "2" || v.mem != "2GiB" || v.disk != "10GiB" {
		t.Fatalf("edit form seed = cpu %q / mem %q / disk %q, want 2 / 2GiB / 10GiB", v.cpu, v.mem, v.disk)
	}
	// Untouched current size is unit-bearing → withUnit leaves it (no rescale).
	if got := withUnit(v.disk, "GiB"); got != "10GiB" {
		t.Errorf("withUnit(seeded disk) = %q, want 10GiB", got)
	}
	// The seed is captured so an untouched submit can be detected.
	if v.diskSeed != "10GiB" {
		t.Errorf("diskSeed = %q, want 10GiB", v.diskSeed)
	}
}

// Pins the untouched-submit guard: editing only cpu/ram (disk field left at its seeded
// value, or blanked) must NOT resize the disk — otherwise a profile-inherited size gets
// pinned onto the instance as an override. Only a genuine change resizes.
func TestDiskResizeArg(t *testing.T) {
	cases := []struct{ seed, field, want string }{
		{"10GiB", "10GiB", ""},      // untouched → no resize
		{"10GiB", " 10GiB ", ""},    // untouched (whitespace) → no resize
		{"10GiB", "", ""},           // cleared → no resize
		{"10GiB", "20", "20GiB"},    // bare number changed → GiB
		{"10GiB", "20GiB", "20GiB"}, // unit-bearing change → passthrough
		{"", "10", "10GiB"},         // no prior explicit size, user sets one
		{"", "", ""},                // nothing seeded, nothing entered
	}
	for _, c := range cases {
		if got := diskResizeArg(c.seed, c.field); got != c.want {
			t.Errorf("diskResizeArg(%q, %q) = %q, want %q", c.seed, c.field, got, c.want)
		}
	}
}

// The edit form offers an editable disk field only when the VM is stopped. A running VM
// gets no disk field (and no seed), so a cpu/ram edit can never silently drop into — or be
// refused because of — a disk resize.
func TestEditFormDiskFieldGatedByState(t *testing.T) {
	// Stopped: the disk field is present and seeded from the current size.
	_, vs := newEditForm(xincus.VM{Name: "web", StatusCode: api.Stopped, CPULimit: "2", MemLimit: "2147483648", DiskSize: "10GiB"})
	if vs.disk != "10GiB" || vs.diskSeed != "10GiB" {
		t.Errorf("stopped VM: disk=%q diskSeed=%q, want 10GiB/10GiB (editable seeded field)", vs.disk, vs.diskSeed)
	}
	// Running: no editable disk field, so no seed — the disk dimension stays untouched.
	_, vr := newEditForm(xincus.VM{Name: "web", StatusCode: api.Running, CPULimit: "2", MemLimit: "2147483648", DiskSize: "10GiB"})
	if vr.disk != "" || vr.diskSeed != "" {
		t.Errorf("running VM: disk=%q diskSeed=%q, want empty (no disk field offered)", vr.disk, vr.diskSeed)
	}
}

// A disk grow is gated on the confirm step: a changed disk with a declined confirm cancels
// the whole edit; a confirmed change proceeds; a cpu/ram-only edit (unchanged disk) never
// needs the confirm and proceeds regardless.
func TestEditConfirmGatesDiskGrow(t *testing.T) {
	mk := func(seed, disk string, confirm bool) model {
		m := *testModel()
		m.selectedName = "web"
		m.formKind = formEdit
		m.vars = &formVars{cpu: "2", diskSeed: seed, disk: disk, confirm: confirm}
		return m
	}
	// Grew the disk but declined the confirm → whole edit cancelled (back to list, not busy).
	if got, _ := mk("10GiB", "20GiB", false).completeForm(); got.(model).mode != modeList {
		t.Errorf("declined disk grow: mode = %v, want modeList (cancelled)", got.(model).mode)
	}
	// Grew the disk and confirmed → proceeds into busy.
	if got, _ := mk("10GiB", "20GiB", true).completeForm(); got.(model).mode != modeBusy {
		t.Errorf("confirmed disk grow: mode = %v, want modeBusy", got.(model).mode)
	}
	// Disk unchanged (cpu/ram-only edit) → proceeds regardless of the confirm flag.
	if got, _ := mk("10GiB", "10GiB", false).completeForm(); got.(model).mode != modeBusy {
		t.Errorf("cpu/ram-only edit: mode = %v, want modeBusy (no confirm needed)", got.(model).mode)
	}
}

// The inline grow-only validator rejects a shrink below the seeded size in-field, accepts a
// grow or an unchanged value, and defers to format validation.
func TestGrowOnlyValidator(t *testing.T) {
	v := growOnlyValidator("10GiB")
	if err := v("8GiB"); err == nil {
		t.Error("shrink 8GiB below 10GiB should be rejected in-field")
	}
	if err := v("8000MiB"); err == nil { // 8000MiB < 10GiB, cross-unit
		t.Error("cross-unit shrink 8000MiB below 10GiB should be rejected")
	}
	for _, ok := range []string{"", "10GiB", "20", "20GiB", "10240MiB"} { // blank/equal/grow
		if err := v(ok); err != nil {
			t.Errorf("growOnlyValidator(10GiB)(%q) = %v, want nil", ok, err)
		}
	}
	if err := v("1.5"); err == nil { // still format-validated
		t.Error("decimal should be rejected by the format check")
	}
}

// sizedModel builds a real model at a given terminal size (via WindowSizeMsg → layout),
// marked ready with one VM, for frame-rendering assertions.
func sizedModel(w, h int, vm xincus.VM) model {
	mm, _ := New(nil).Update(tea.WindowSizeMsg{Width: w, Height: h})
	m := mm.(model)
	m.ready = true
	m.vms = []xincus.VM{vm}
	m.applyFilter()
	m.selectedName = vm.Name
	return m
}

// assertFrame pins the core rendering invariant: the frame is EXACTLY m.height rows and no
// line exceeds m.width. A frame shorter than m.height leaves stale rows on screen (the
// "garbled text" bug, worst in tmux); a wider line is clipped by the AltScreen renderer.
func assertFrame(t *testing.T, m model, label string) {
	t.Helper()
	f := m.frameView()
	if got := lipgloss.Height(f); got != m.height {
		t.Errorf("%s @ %dx%d: frame height = %d, want %d", label, m.width, m.height, got, m.height)
	}
	for i, line := range strings.Split(f, "\n") {
		if lw := lipgloss.Width(line); lw > m.width {
			t.Errorf("%s @ %dx%d: line %d width = %d > %d", label, m.width, m.height, i, lw, m.width)
		}
	}
}

// The composed frame must always be exactly m.height × m.width across every mode and size —
// including a form whose content is far taller than the screen (simulating a big inline
// validation error), which must be clamped, not allowed to strand rows.
func TestFrameAlwaysExactlyFillsScreen(t *testing.T) {
	vm := xincus.VM{Name: "web-01", StatusCode: api.Stopped, Status: "Stopped", CPULimit: "2", MemLimit: "2147483648", DiskSize: "10GiB", IPv4: "10.0.0.5"}
	for _, s := range [][2]int{{80, 24}, {100, 40}, {120, 30}, {60, 20}, {40, 14}} {
		w, h := s[0], s[1]
		base := func() model { return sizedModel(w, h, vm) }

		list := base()
		assertFrame(t, list, "list")

		full := base()
		full.help.ShowAll = true
		full.layout()
		assertFrame(t, full, "list+fullhelp")

		busy := base()
		busy.mode = modeBusy
		busy.busyText = "resize web-01"
		assertFrame(t, busy, "busy")

		detail := base()
		detail.mode = modeDetail
		detail.layout()
		detail.refreshDetail()
		assertFrame(t, detail, "detail")

		logs := base()
		logs.mode = modeLogs
		logs.layout()
		logs.logs.SetContent("line one\nline two\nline three")
		assertFrame(t, logs, "logs")

		editor := base()
		editor.mode = modeLaunchEdit
		editor.editor.SetValue("#cloud-config\nruncmd:\n  - echo hi")
		assertFrame(t, editor, "launchEdit")

		edit := base()
		edit.mode = modeForm
		ef, _ := newEditForm(vm)
		edit.form = ef.WithWidth(formWidth(w)).WithHeight(formHeight(h))
		_ = edit.form.Init()
		assertFrame(t, edit, "editForm")

		launch := base()
		launch.mode = modeForm
		launch.form = newLaunchForm(nil, nil, &formVars{}, nil).WithWidth(formWidth(w)).WithHeight(formHeight(h))
		_ = launch.form.Init()
		assertFrame(t, launch, "launchForm")

		// The garbled-text regression: a form far taller than the screen must still be pinned.
		overtall := base()
		overtall.mode = modeForm
		of, _ := newEditForm(vm)
		overtall.form = of.WithWidth(formWidth(w)).WithHeight(h * 4)
		_ = overtall.form.Init()
		assertFrame(t, overtall, "overtallForm")
	}
}

func testModel() *model {
	m := &model{width: 100}
	m.table = table.New()
	m.imgTable = table.New()
	m.filterInput = textinput.New()
	m.editor = textarea.New()
	return m
}

func vmsNamed(names ...string) []xincus.VM {
	vms := make([]xincus.VM, len(names))
	for i, n := range names {
		vms[i] = xincus.VM{Name: n}
	}
	return vms
}

func TestApplyFilterByName(t *testing.T) {
	m := testModel()
	m.vms = vmsNamed("web", "db", "webcache")
	m.filterInput.SetValue("web")
	m.applyFilter()
	if len(m.filtered) != 2 {
		t.Fatalf("filtered = %d, want 2 (web, webcache)", len(m.filtered))
	}
}

// Regression for the resize crash: bubbles' table.SetColumns re-renders the existing
// (wider) rows against the new, shorter column slice and panics with index-out-of-range
// when the terminal shrinks. syncTable must clear rows before shrinking the column set.
func TestSyncTableResizeShrinkNoPanic(t *testing.T) {
	m := testModel()
	m.vms = vmsNamed("web", "db", "cache")

	m.width = 200 // wide: all 7 columns, rows built with 7 cells
	m.applyFilter()
	if got := len(m.table.Columns()); got != 7 {
		t.Fatalf("precondition: wide table has %d columns, want 7", got)
	}

	// Shrink hard to far fewer columns. Before the fix this panicked.
	m.width = 40
	m.applyFilter()
	_ = m.table.View() // rendering is where the row/column mismatch blew up

	want := len(visibleCols(40))
	if got := len(m.table.Columns()); got != want {
		t.Errorf("after shrink, table has %d columns, want %d", got, want)
	}
	for i, r := range m.table.Rows() {
		if len(r) != want {
			t.Errorf("row %d has %d cells, want %d (column drift)", i, len(r), want)
		}
	}
}

// Regression for the bug where, after the selected VM disappeared, the cursor kept a
// stale index that pointed at a different VM (so the next action hit the wrong one).
func TestApplyFilterSelectionFallback(t *testing.T) {
	m := testModel()
	m.vms = vmsNamed("a", "b", "c")
	m.applyFilter()
	m.table.SetCursor(2) // select "c"
	if got := m.filtered[m.table.Cursor()].Name; got != "c" {
		t.Fatalf("cursor on %q, want c", got)
	}

	m.vms = vmsNamed("a", "b") // "c" removed out of band
	m.applyFilter()
	cur := m.table.Cursor()
	if cur < 0 || cur >= len(m.filtered) {
		t.Fatalf("cursor %d out of range after VM removal (would target a wrong VM)", cur)
	}
}

// --- codespace import + images view -----------------------------------------

// The launch wizard's default disk must be 50 GiB (the roomy default that also clears the
// ~20 GiB codespace image floor). Driving the launchDataMsg handler pins the actual default,
// which TestLaunchSpecMapping (it sets disk explicitly) does not cover.
func TestLaunchDefaultDiskIs50(t *testing.T) {
	m := *testModel()
	m.mode = modeBusy // the handler ignores the msg unless a launch is in flight
	out, _ := m.Update(launchDataMsg{})
	got := out.(model)
	if got.vars == nil || got.vars.disk != "50" {
		t.Fatalf("launch default disk = %v, want 50", got.vars)
	}
}

// The import picker defaults to (and preselects) "latest".
func TestImportFormDefaultsToLatest(t *testing.T) {
	v := &formVars{}
	rels := []xincus.CodespaceRelease{{Tag: "v2", HasImage: true}}
	_ = newImportForm(rels, nil, v)
	if v.tag != "latest" {
		t.Errorf("import form default tag = %q, want latest", v.tag)
	}
}

// importableReleases keeps only releases that actually carry an image.
func TestImportableReleases(t *testing.T) {
	in := []xincus.CodespaceRelease{
		{Tag: "v2", HasImage: true}, {Tag: "notes", HasImage: false}, {Tag: "v1", HasImage: true},
	}
	out := importableReleases(in)
	if len(out) != 2 || out[0].Tag != "v2" || out[1].Tag != "v1" {
		t.Fatalf("importableReleases = %+v, want v2,v1", out)
	}
}

// importedTags maps per-tag codespace aliases back to their tag (for the ✓ marker), ignoring
// unrelated aliases and the rolling latest.
func TestImportedTags(t *testing.T) {
	m := testModel()
	m.images = []xincus.LocalImage{
		{Aliases: []string{"glueops-codespace-v2"}},
		{Aliases: []string{"some-other-image"}},
		{Aliases: []string{"glueops-codespace-latest"}},
	}
	got := m.importedTags()
	if !got["v2"] || !got["latest"] || got["some-other-image"] {
		t.Fatalf("importedTags = %+v, want v2+latest only", got)
	}
}

// importStatusLine renders phase/step + (download) percent/bytes + a live elapsed timer, and
// stays a single line (frameView clamps width; it must never inject a newline).
func TestImportStatusLine(t *testing.T) {
	m := *testModel()
	m.busyText, m.busyStart = "import glueops-codespace-latest", time.Now().Add(-3*time.Second)

	m.busyProg = xincus.ImportProgress{Phase: "download", Step: 2, Done: 1 << 30, Total: 2 << 30}
	dl := m.importStatusLine()
	if strings.Contains(dl, "\n") {
		t.Fatalf("status line must be single-line, got %q", dl)
	}
	for _, want := range []string{"step 2/4", "download", "50%"} {
		if !strings.Contains(dl, want) {
			t.Errorf("download status %q missing %q", dl, want)
		}
	}

	// Opaque phase: no percent, still shows the phase + elapsed.
	m.busyProg = xincus.ImportProgress{Phase: "import", Step: 4}
	im := m.importStatusLine()
	if !strings.Contains(im, "step 4/4") || strings.Contains(im, "%") {
		t.Errorf("import-phase status %q should show step 4/4 and no percent", im)
	}
}

// syncImages must not panic when the column set is rebuilt on a width change (same class of
// bug as the VM table's SetColumns-on-shrink crash).
func TestSyncImagesResizeNoPanic(t *testing.T) {
	m := testModel()
	m.images = []xincus.LocalImage{
		{Fingerprint: "abcdef0123456789", Aliases: []string{"glueops-codespace-latest"}, Type: "virtual-machine", Size: 1 << 30},
	}
	m.width = 200
	m.syncImages()
	m.width = 30 // shrink — a naive SetColumns would panic here
	m.syncImages()
}

// A local image with no alias falls back to a short fingerprint label (never an empty cell).
func TestImageLabelFallback(t *testing.T) {
	if got := imageLabel(xincus.LocalImage{Fingerprint: "abcdef0123456789"}); got != "abcdef012345" {
		t.Errorf("imageLabel(no alias) = %q, want abcdef012345", got)
	}
	if got := imageLabel(xincus.LocalImage{Aliases: []string{"glueops-codespace-v2"}}); got != "glueops-codespace-v2" {
		t.Errorf("imageLabel(alias) = %q, want the alias", got)
	}
}
