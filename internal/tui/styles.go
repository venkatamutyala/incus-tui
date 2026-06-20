package tui

import (
	"fmt"
	"image/color"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/lxc/incus/v7/shared/api"
	xincus "github.com/venkatamutyala/incus-tui/internal/incus"
)

type styles struct {
	title      lipgloss.Style
	statusBar  lipgloss.Style
	help       lipgloss.Style
	toastOK    lipgloss.Style
	toastErr   lipgloss.Style
	stale      lipgloss.Style
	dim        lipgloss.Style
	detailKey  lipgloss.Style
	box        lipgloss.Style
	spinnerSty lipgloss.Style
}

func newStyles() styles {
	return styles{
		title:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("63")).Padding(0, 1),
		statusBar:  lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Padding(0, 1),
		help:       lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		toastOK:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")),
		toastErr:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203")),
		stale:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("214")).Padding(0, 1),
		dim:        lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		detailKey:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")),
		box:        lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("63")).Padding(0, 1),
		spinnerSty: lipgloss.NewStyle().Foreground(lipgloss.Color("63")),
	}
}

// statusGlyph pairs a shape with statusColor so VM state is legible without relying on
// color alone (colorblind users / no-color terminals).
func statusGlyph(code api.StatusCode) string {
	switch code {
	case api.Running:
		return "●"
	case api.Frozen:
		return "◐"
	case api.Stopped:
		return "○"
	case api.Error:
		return "✗"
	default:
		return "·"
	}
}

// statusColor maps an Incus status to a foreground color.
func statusColor(code api.StatusCode) color.Color {
	switch code {
	case api.Running:
		return lipgloss.Color("42") // green
	case api.Frozen:
		return lipgloss.Color("214") // amber
	case api.Stopped:
		return lipgloss.Color("244") // gray
	case api.Error:
		return lipgloss.Color("203") // red
	default:
		return lipgloss.Color("245")
	}
}

// --- formatting helpers ------------------------------------------------------

func formatAge(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func formatBytes(b int64) string {
	if b <= 0 {
		return "-"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// memCell shows memory pressure as a percent of the guest's total — a denominator the
// raw byte figure lacked. The absolute usage stays in the detail pane.
func memCell(v xincus.VM) string {
	if !v.AgentReady || v.MemoryUsage <= 0 {
		return "-"
	}
	if v.MemoryTotal > 0 {
		return fmt.Sprintf("%d%%", int(v.MemoryUsage*100/v.MemoryTotal))
	}
	return formatBytes(v.MemoryUsage)
}
