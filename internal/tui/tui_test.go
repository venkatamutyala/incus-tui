package tui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"

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
	ok := []string{"", "2048", "12", "2GiB", "512MiB", "100GB"}
	bad := []string{"1.5", "1.5GiB", "2 GiB", "2gib", "abc", "12GiBx"}
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

func TestNormalizeMem(t *testing.T) {
	cases := map[string]string{
		"2147483648": "2GiB",        // 2 GiB in bytes → readable, and unit-bearing so withUnit won't rescale
		"536870912":  "512MiB",      // 512 MiB in bytes
		"1500000000": "1500000000B", // not cleanly divisible → exact bytes, still unit-bearing
		"2GiB":       "2GiB",        // already has a unit → unchanged
		"":           "",            // empty stays empty
	}
	for in, want := range cases {
		if got := normalizeMem(in); got != want {
			t.Errorf("normalizeMem(%q) = %q, want %q", in, got, want)
		}
		// And the key property: re-applying withUnit to the normalized value is a no-op.
		if n := normalizeMem(in); n != "" && withUnit(n, "MiB") != n {
			t.Errorf("withUnit(normalizeMem(%q)) rescaled %q → %q", in, n, withUnit(n, "MiB"))
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

func testModel() *model {
	m := &model{width: 100}
	m.table = table.New()
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
