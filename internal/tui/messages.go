package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	xincus "github.com/venkatamutyala/incus-tui/internal/incus"
)

const refreshInterval = 3 * time.Second

// --- messages ---------------------------------------------------------------

type vmsMsg struct {
	vms []xincus.VM
	err error
}

type opDoneMsg struct {
	action string
	name   string
	err    error
}

type eventMsg struct{ ev xincus.Event }

type launchDataMsg struct {
	images    []xincus.Image
	templates []xincus.Template
	err       error
}

type consoleLogMsg struct {
	name    string
	content string
	err     error
}

type cloudInitMsg struct {
	name    string
	content string
	err     error
}

type toastMsg struct {
	text  string
	isErr bool
}

type clearToastMsg struct{ seq int }

type tickMsg time.Time

type execDoneMsg struct{ err error }

// --- commands ---------------------------------------------------------------

func loadVMs(c *xincus.Client) tea.Cmd {
	return func() tea.Msg {
		vms, err := c.ListVMs()
		return vmsMsg{vms: vms, err: err}
	}
}

// runOp runs a cancelable, blocking service-layer operation off the UI thread and
// reports the result as an opDoneMsg. It owns the op's context: cancel is always
// called when the op returns, releasing the backstop timer set by busy().
func runOp(ctx context.Context, cancel context.CancelFunc, action, name string, fn func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		defer cancel()
		return opDoneMsg{action: action, name: name, err: fn(ctx)}
	}
}

// waitForEvent reads exactly one event from the supervisor channel; the Update loop
// re-issues it to turn the channel into a stream of messages.
func waitForEvent(ch <-chan xincus.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return eventMsg{ev: ev}
	}
}

func loadLaunchData(c *xincus.Client) tea.Cmd {
	return func() tea.Msg {
		images, err := c.ListVMImages()
		templates, _ := xincus.ListTemplates()
		return launchDataMsg{images: images, templates: templates, err: err}
	}
}

func fetchConsoleLog(c *xincus.Client, name string) tea.Cmd {
	return func() tea.Msg {
		out, err := c.ConsoleLog(name)
		return consoleLogMsg{name: name, content: out, err: err}
	}
}

func fetchCloudInit(c *xincus.Client, name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := c.CloudInitStatus(ctx, name)
		return cloudInitMsg{name: name, content: out, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func toastAfter(text string, isErr bool) tea.Cmd {
	return func() tea.Msg { return toastMsg{text: text, isErr: isErr} }
}

func clearToastCmd(seq int) tea.Cmd {
	return tea.Tick(4*time.Second, func(time.Time) tea.Msg { return clearToastMsg{seq: seq} })
}
