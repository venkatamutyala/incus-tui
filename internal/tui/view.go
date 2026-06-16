package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	xincus "github.com/venkatamutyala/incus-tui/internal/incus"
)

func (m model) View() tea.View {
	var body string
	switch {
	case m.fatalErr != nil && !m.ready:
		body = m.fatalScreen()
	case !m.ready:
		body = "\n  " + m.spinner.View() + " connecting to Incus…"
	default:
		switch m.mode {
		case modeForm:
			body = m.styles.box.Render(m.form.View())
		case modeLaunchEdit:
			body = m.editor.View()
		case modeDetail:
			body = m.detail.View()
		case modeLogs:
			body = m.logs.View()
		default: // modeList, modeBusy
			body = m.listBody()
		}
	}

	frame := lipgloss.JoinVertical(lipgloss.Left,
		m.headerLine(),
		body,
		m.statusLine(),
		m.bottomBar(),
	)

	v := tea.NewView(frame)
	v.AltScreen = true
	return v
}

func (m model) listBody() string {
	if m.ready && len(m.filtered) == 0 {
		msg := "No VMs yet — press n to launch one."
		if strings.TrimSpace(m.filterInput.Value()) != "" {
			msg = "No VMs match the filter."
		}
		return "\n  " + m.styles.dim.Render(msg)
	}
	return m.table.View()
}

func (m model) headerLine() string {
	title := m.styles.title.Render("incus-tui")
	ctx := "local · VMs"
	switch m.mode {
	case modeDetail:
		ctx = "VM · " + m.selectedName
	case modeLogs:
		ctx = "logs · " + m.selectedName
	case modeForm:
		ctx = "launch / action"
	case modeLaunchEdit:
		ctx = "cloud-init editor"
	}
	return title + " " + m.styles.dim.Render(ctx)
}

func (m model) statusLine() string {
	var left string
	switch {
	case m.mode == modeBusy:
		left = m.spinner.View() + " " + m.busyText + m.styles.dim.Render("  (esc cancels)")
	case m.filtering:
		left = "/" + m.filterInput.View()
	default:
		left = fmt.Sprintf("%d VM(s)", len(m.filtered))
		if len(m.filtered) != len(m.vms) {
			left += fmt.Sprintf(" of %d", len(m.vms))
		}
	}

	var right string
	if !m.streamUp {
		right = m.styles.stale.Render("⟳ reconnecting")
	}

	var mid string
	if m.toast != "" {
		if m.toastErr {
			mid = m.styles.toastErr.Render(m.toast)
		} else {
			mid = m.styles.toastOK.Render(m.toast)
		}
	}

	return m.styles.statusBar.Render(strings.TrimRight(left+"  "+mid+"  "+right, " "))
}

// bottomBar is the mode-aware help line (full keymap on the list, contextual hints
// elsewhere) so users always see actions relevant to the current screen.
func (m model) bottomBar() string {
	switch m.mode {
	case modeForm:
		return m.styles.help.Render("tab/enter next · esc cancel")
	case modeLaunchEdit:
		return m.styles.help.Render("ctrl+s launch · esc back to options")
	case modeDetail:
		return m.styles.help.Render("e edit · p snapshot · l logs · y copy IP · s shell · d delete · esc back")
	case modeLogs:
		view := "console"
		if m.logsShowCloudInit {
			view = "cloud-init"
		}
		return m.styles.help.Render("c toggle [" + view + "] · r refresh · ↑/↓ scroll · esc back")
	default:
		return m.help.View(m.keys)
	}
}

func (m model) fatalScreen() string {
	return m.styles.box.Render(
		m.styles.toastErr.Render("Cannot reach the Incus daemon.") + "\n\n" +
			m.fatalErr.Error() + "\n\n" +
			m.styles.dim.Render("Is the daemon running? Try: scripts/start-incusd.sh") + "\n" +
			m.styles.dim.Render("Press q to quit."),
	)
}

// --- responsive table columns -----------------------------------------------

// colSpec describes one table column and how to render its cell, so the column set
// and the row cells are always built from the same definition (no index drift).
type colSpec struct {
	title string
	min   int
	width int
	flex  bool
	cell  func(xincus.VM) string
}

func allCols() []colSpec {
	return []colSpec{
		{title: "NAME", min: 12, flex: true, cell: func(v xincus.VM) string { return v.Name }},
		{title: "STATUS", min: 9, cell: func(v xincus.VM) string { return v.Status }},
		{title: "IPV4", min: 15, cell: func(v xincus.VM) string { return orDash(v.IPv4) }},
		{title: "IMAGE", min: 12, flex: true, cell: func(v xincus.VM) string { return orDash(v.Image) }},
		{title: "AGE", min: 5, cell: func(v xincus.VM) string { return formatAge(v.Age()) }},
		{title: "CPU", min: 4, cell: func(v xincus.VM) string { return orDash(v.CPULimit) }},
		{title: "MEM", min: 9, cell: func(v xincus.VM) string { return memCell(v) }},
	}
}

// colDropOrder lists columns least-to-most important to drop when the terminal is
// too narrow. NAME and STATUS are never dropped.
var colDropOrder = []string{"CPU", "MEM", "AGE", "IMAGE", "IPV4"}

// visibleCols returns the columns that fit in width, with flex columns absorbing the
// leftover space.
func visibleCols(width int) []colSpec {
	cols := allCols()
	if width <= 0 {
		width = 100
	}
	minSum := func(cs []colSpec) int {
		s := 0
		for _, c := range cs {
			s += c.min
		}
		return s + 2*len(cs) // ~2 chars of cell padding per column
	}
	for _, title := range colDropOrder {
		if minSum(cols) <= width {
			break
		}
		cols = dropCol(cols, title)
	}
	leftover := width - minSum(cols)
	if leftover < 0 {
		leftover = 0
	}
	flexN := 0
	for _, c := range cols {
		if c.flex {
			flexN++
		}
	}
	for i := range cols {
		cols[i].width = cols[i].min
		if cols[i].flex && flexN > 0 {
			cols[i].width += leftover / flexN
		}
	}
	return cols
}

func dropCol(cols []colSpec, title string) []colSpec {
	out := make([]colSpec, 0, len(cols))
	for _, c := range cols {
		if c.title != title {
			out = append(out, c)
		}
	}
	return out
}

// --- detail rendering --------------------------------------------------------

func renderDetail(s styles, v xincus.VM) string {
	var b strings.Builder
	row := func(k, val string) {
		b.WriteString(s.detailKey.Render(fmt.Sprintf("%-12s", k)))
		b.WriteString("  " + val + "\n")
	}
	status := lipgloss.NewStyle().Foreground(statusColor(v.StatusCode)).Render(v.Status)
	row("Name", v.Name)
	row("Status", status)
	row("Type", v.Type)
	row("IPv4", orDash(v.IPv4))
	row("Image", orDash(v.Image))
	if !v.CreatedAt.IsZero() {
		row("Created", v.CreatedAt.Format(time.RFC1123))
	}
	row("Age", formatAge(v.Age()))
	row("CPU limit", orDash(v.CPULimit))
	row("Mem limit", orDash(v.MemLimit))
	row("Agent", boolStr(v.AgentReady, "ready", "not ready"))
	if v.AgentReady {
		row("CPU time", fmt.Sprintf("%.1fs", float64(v.CPUUsageNS)/1e9))
		row("Mem used", formatBytes(v.MemoryUsage))
	}
	if len(v.Snapshots) > 0 {
		row("Snapshots", strings.Join(v.Snapshots, ", "))
	}
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func boolStr(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
