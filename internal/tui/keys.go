package tui

import "charm.land/bubbles/v2/key"

// keyMap defines all keybindings, following k9s/lazydocker conventions.
type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	Top     key.Binding
	Bottom  key.Binding
	Enter   key.Binding
	Back    key.Binding
	Filter  key.Binding
	Help    key.Binding
	Quit    key.Binding
	Refresh key.Binding

	// Actions on the selected VM.
	Launch     key.Binding
	Shell      key.Binding
	Logs       key.Binding
	Start      key.Binding
	Stop       key.Binding
	Restart    key.Binding
	Freeze     key.Binding
	Snapshot   key.Binding
	EditLimits key.Binding
	CopyIP     key.Binding
	Delete     key.Binding
	Resize     key.Binding // grow a storage pool (host-scoped)
}

// ShortHelp is the always-visible help bar. Discoverability keys come first so the
// help library's width truncation can never drop "quit"/"help"/"filter".
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Quit, k.Filter, k.Enter, k.Launch, k.Shell, k.Logs, k.Delete}
}

// FullHelp is the expanded '?' cheat sheet. Essentials (enter/back/filter/help/quit)
// lead column 1 so the help library's width truncation drops actions, never the keys
// needed to leave or get unstuck.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Enter, k.Back, k.Filter, k.Help, k.Quit, k.Refresh},
		{k.Up, k.Down, k.Top, k.Bottom},
		{k.Launch, k.Shell, k.Logs, k.Start, k.Stop, k.Restart},
		{k.Freeze, k.Snapshot, k.EditLimits, k.CopyIP, k.Delete, k.Resize},
	}
}

func defaultKeys() keyMap {
	return keyMap{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Top:     key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:  key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("↵", "details")),
		Back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Refresh: key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "refresh")),

		Launch:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new VM")),
		Shell:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "shell")),
		Logs:       key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "logs")),
		Start:      key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "start")),
		Stop:       key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "stop")),
		Restart:    key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart")),
		Freeze:     key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "pause/resume")),
		Snapshot:   key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "snapshot")),
		EditLimits: key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit cpu/ram")),
		CopyIP:     key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy IP")),
		Delete:     key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		Resize:     key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "resize pool")),
	}
}
