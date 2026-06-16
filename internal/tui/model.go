package tui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/lxc/incus/v7/shared/api"
	xincus "github.com/venkatamutyala/incus-tui/internal/incus"
)

type mode int

const (
	modeList mode = iota
	modeDetail
	modeForm
	modeLaunchEdit
	modeLogs
	modeBusy
)

type model struct {
	client *xincus.Client
	styles styles
	keys   keyMap

	help        help.Model
	table       table.Model
	detail      viewport.Model
	logs        viewport.Model
	spinner     spinner.Model
	editor      textarea.Model
	filterInput textinput.Model

	vms      []xincus.VM
	filtered []xincus.VM

	width, height int
	mode          mode
	selectedName  string
	filtering     bool
	busyText      string
	cancel        context.CancelFunc // cancels the in-flight busy op (esc)

	form          *huh.Form
	vars          *formVars
	formKind      formKind
	pendingLaunch xincus.CreateSpec
	// cached launch data so editor "esc back to form" doesn't re-fetch.
	launchImages    []xincus.Image
	launchTemplates []xincus.Template

	logsShowCloudInit bool

	toast    string
	toastErr bool
	toastSeq int
	streamUp bool

	events     chan xincus.Event
	eventsDone chan struct{}

	ready      bool
	loadingVMs bool // a periodic ListVMs is in flight (avoids pile-up on a slow daemon)
	fatalErr   error
	quitting   bool
}

// New constructs the root model wired to a connected Incus client.
func New(c *xincus.Client) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	ti := textinput.New()
	ti.Placeholder = "filter by name…"

	st := newStyles()
	sp.Style = st.spinnerSty

	tbl := table.New()
	// The bubbles table binds extra paging keys (b, space, u, d, ctrl+u, ctrl+d)
	// that would otherwise leak through our action fallthrough; restrict them.
	tbl.KeyMap.PageUp.SetKeys("pgup")
	tbl.KeyMap.PageDown.SetKeys("pgdown")
	tbl.KeyMap.HalfPageUp.SetEnabled(false)
	tbl.KeyMap.HalfPageDown.SetEnabled(false)

	return model{
		client:      c,
		styles:      st,
		keys:        defaultKeys(),
		help:        help.New(),
		table:       tbl,
		detail:      viewport.New(),
		logs:        viewport.New(),
		spinner:     sp,
		editor:      textarea.New(),
		filterInput: ti,
		mode:        modeList,
		streamUp:    true,
		events:      make(chan xincus.Event, 32),
		eventsDone:  make(chan struct{}),
	}
}

func (m model) Init() tea.Cmd {
	go m.client.WatchEvents(m.events, m.eventsDone)
	return tea.Batch(loadVMs(m.client), tickCmd(), waitForEvent(m.events), m.spinner.Tick)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case vmsMsg:
		m.loadingVMs = false
		if msg.err != nil {
			if !m.ready {
				m.fatalErr = msg.err
				return m, nil
			}
			return m, m.setToast(msg.err.Error(), true)
		}
		m.ready, m.fatalErr = true, nil
		m.vms = msg.vms
		m.applyFilter()
		if m.mode == modeDetail {
			m.refreshDetail()
		}
		return m, nil

	case tickMsg:
		// Sequence periodicLoad (pointer receiver, mutates m) before returning m so
		// the loadingVMs flag is captured in the returned model, not lost to copy order.
		cmd := m.periodicLoad()
		return m, tea.Batch(tickCmd(), cmd)

	case eventMsg:
		cmds := []tea.Cmd{waitForEvent(m.events)}
		switch msg.ev.Kind {
		case xincus.EventListenerDown:
			m.streamUp = false
		case xincus.EventListenerUp:
			m.streamUp = true
		case xincus.EventLifecycle:
			cmds = append(cmds, m.periodicLoad())
		}
		return m, tea.Batch(cmds...)

	case opDoneMsg:
		m.cancel = nil
		if m.mode == modeBusy {
			m.mode = modeList
		}
		var cmd tea.Cmd
		switch {
		case msg.err == nil && msg.action == "resize":
			// limits.cpu hotplugs, but a running VM needs a reboot to pick up new memory.
			cmd = m.setToast(msg.action+" "+msg.name+" ✓ (restart VM to apply memory)", false)
		case msg.err == nil:
			cmd = m.setToast(msg.action+" "+msg.name+" ✓", false)
		case errors.Is(msg.err, context.Canceled):
			cmd = m.setToast(msg.action+" "+msg.name+": aborted", true)
		default:
			cmd = m.setToast(msg.action+" "+msg.name+": "+msg.err.Error(), true)
		}
		return m, tea.Batch(loadVMs(m.client), cmd)

	case launchDataMsg:
		if m.mode != modeBusy { // user aborted while loading
			return m, nil
		}
		if msg.err != nil {
			m.mode = modeList
			return m, m.setToast("images: "+msg.err.Error(), true)
		}
		m.launchImages, m.launchTemplates = msg.images, msg.templates
		vars := &formVars{cpu: "2", mem: "2GiB", disk: "12GiB"}
		m.vars, m.formKind = vars, formLaunch
		m.form = newLaunchForm(msg.images, msg.templates, vars).
			WithWidth(max(40, m.width-4)).WithHeight(max(12, m.height-5))
		m.mode = modeForm
		return m, m.form.Init()

	case consoleLogMsg:
		if m.mode == modeLogs && !m.logsShowCloudInit && msg.name == m.selectedName {
			m.setLogsContent(msg.content, msg.err, "(console log is empty)")
		}
		return m, nil

	case cloudInitMsg:
		if m.mode == modeLogs && m.logsShowCloudInit && msg.name == m.selectedName {
			m.setLogsContent(msg.content, msg.err, "(no cloud-init output)")
		}
		return m, nil

	case toastMsg:
		return m, m.setToast(msg.text, msg.isErr)

	case clearToastMsg:
		if msg.seq == m.toastSeq {
			m.toast = ""
		}
		return m, nil

	case execDoneMsg:
		m.mode = modeList
		var cmd tea.Cmd
		if msg.err != nil {
			cmd = m.setToast("shell: "+msg.err.Error(), true)
		}
		return m, tea.Batch(loadVMs(m.client), cmd)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	return m.routeInactive(msg)
}

// routeInactive forwards non-key messages (cursor blink, mouse, ...) to whatever
// component currently owns the screen.
func (m model) routeInactive(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeForm:
		return m.updateForm(msg)
	case modeLaunchEdit:
		return m.updateEditor(msg)
	case modeDetail:
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	case modeLogs:
		var cmd tea.Cmd
		m.logs, cmd = m.logs.Update(msg)
		return m, cmd
	case modeBusy:
		return m, nil
	default:
		if m.filtering {
			var cmd tea.Cmd
			m.filterInput, cmd = m.filterInput.Update(msg)
			m.applyFilter()
			return m, cmd
		}
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)
		return m, cmd
	}
}

func (m model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m.quit()
	}
	switch m.mode {
	case modeForm:
		return m.updateForm(k)
	case modeLaunchEdit:
		return m.updateEditor(k)
	case modeLogs:
		return m.handleLogsKey(k)
	case modeBusy:
		if key.Matches(k, m.keys.Back) {
			if m.cancel != nil {
				m.cancel()
				m.busyText = "cancelling…"
			} else {
				m.mode = modeList // e.g. esc during "loading images…"
			}
		}
		return m, nil
	case modeDetail:
		return m.handleDetailKey(k)
	default:
		if m.filtering {
			return m.handleFilterKey(k)
		}
		return m.handleListKey(k)
	}
}

func (m model) handleListKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.keys.Quit):
		return m.quit()
	case key.Matches(k, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.layout()
		return m, nil
	case key.Matches(k, m.keys.Filter):
		m.filtering = true
		return m, m.filterInput.Focus()
	case key.Matches(k, m.keys.Refresh):
		return m, loadVMs(m.client)
	case key.Matches(k, m.keys.Enter):
		if v, ok := m.current(); ok {
			m.selectedName = v.Name
			m.mode = modeDetail
			m.refreshDetail()
		}
		return m, nil
	}
	if mm, cmd, handled := m.handleAction(k); handled {
		return mm, cmd
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(k)
	return m, cmd
}

func (m model) handleDetailKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.keys.Back), key.Matches(k, m.keys.Quit):
		m.mode = modeList
		return m, nil
	case key.Matches(k, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.layout()
		return m, nil
	case key.Matches(k, m.keys.Bottom):
		m.detail.GotoBottom()
		return m, nil
	case key.Matches(k, m.keys.Top):
		m.detail.GotoTop()
		return m, nil
	}
	if mm, cmd, handled := m.handleAction(k); handled {
		return mm, cmd
	}
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(k)
	return m, cmd
}

func (m model) handleFilterKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.filtering = false
		m.filterInput.Blur()
		m.filterInput.SetValue("")
		m.applyFilter()
		return m, nil
	case "enter":
		m.filtering = false
		m.filterInput.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(k)
	m.applyFilter()
	return m, cmd
}

func (m model) handleLogsKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.keys.Back), key.Matches(k, m.keys.Quit):
		m.mode = modeList
		return m, nil
	case key.Matches(k, m.keys.Refresh):
		if m.logsShowCloudInit {
			return m, fetchCloudInit(m.client, m.selectedName)
		}
		return m, fetchConsoleLog(m.client, m.selectedName)
	case k.String() == "c":
		m.logsShowCloudInit = !m.logsShowCloudInit
		if m.logsShowCloudInit {
			if v, ok := m.vmByName(m.selectedName); !ok || !v.AgentReady {
				m.logsShowCloudInit = false
				return m, toastAfter("cloud-init status needs the guest agent…", true)
			}
			m.logs.SetContent("loading cloud-init status…")
			return m, fetchCloudInit(m.client, m.selectedName)
		}
		m.logs.SetContent("loading console log…")
		return m, fetchConsoleLog(m.client, m.selectedName)
	}
	var cmd tea.Cmd
	m.logs, cmd = m.logs.Update(k)
	return m, cmd
}

// handleAction routes the VM action keys shared by the list and detail views.
func (m model) handleAction(k tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(k, m.keys.Launch):
		mm, cmd := m.startLaunch()
		return mm, cmd, true
	case key.Matches(k, m.keys.Shell):
		mm, cmd := m.startShell()
		return mm, cmd, true
	case key.Matches(k, m.keys.Logs):
		mm, cmd := m.openLogs()
		return mm, cmd, true
	case key.Matches(k, m.keys.Start):
		if v, ok := m.activeVM(); ok && v.Running() {
			return m, toastAfter(v.Name+" is already running", true), true
		}
		mm, cmd := m.actionOp("start", (*xincus.Client).Start)
		return mm, cmd, true
	case key.Matches(k, m.keys.Stop):
		mm, cmd := m.actionOp("stop", (*xincus.Client).Stop)
		return mm, cmd, true
	case key.Matches(k, m.keys.Restart):
		mm, cmd := m.actionOp("restart", (*xincus.Client).Restart)
		return mm, cmd, true
	case key.Matches(k, m.keys.Freeze):
		mm, cmd := m.freezeToggle()
		return mm, cmd, true
	case key.Matches(k, m.keys.Snapshot):
		mm, cmd := m.openSnapshotManager()
		return mm, cmd, true
	case key.Matches(k, m.keys.EditLimits):
		mm, cmd := m.openForm(formEdit)
		return mm, cmd, true
	case key.Matches(k, m.keys.CopyIP):
		mm, cmd := m.copyIP()
		return mm, cmd, true
	case key.Matches(k, m.keys.Delete):
		mm, cmd := m.openForm(formDelete)
		return mm, cmd, true
	}
	return m, nil, false
}

// --- form handling -----------------------------------------------------------

func (m model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	fm, cmd := m.form.Update(msg)
	if f, ok := fm.(*huh.Form); ok {
		m.form = f
	}
	switch m.form.State {
	case huh.StateCompleted:
		return m.completeForm()
	case huh.StateAborted:
		return m.cancelForm(), nil
	}
	return m, cmd
}

func (m model) cancelForm() model {
	m.mode = modeList
	m.form = nil
	m.formKind = formNone
	return m
}

func (m model) openForm(kind formKind) (tea.Model, tea.Cmd) {
	v, ok := m.activeVM()
	if !ok {
		return m, nil
	}
	var form *huh.Form
	var vars *formVars
	switch kind {
	case formEdit:
		form, vars = newEditForm(v)
	case formDelete:
		form, vars = newDeleteForm(v.Name)
	default:
		return m, nil
	}
	m.formKind, m.vars, m.selectedName = kind, vars, v.Name
	m.form = form.WithWidth(max(40, m.width-4)).WithHeight(max(8, m.height-5))
	m.mode = modeForm
	return m, m.form.Init()
}

func (m model) openSnapshotManager() (tea.Model, tea.Cmd) {
	v, ok := m.activeVM()
	if !ok {
		return m, nil
	}
	form, vars := newSnapManageForm(v)
	m.formKind, m.vars, m.selectedName = formSnapManage, vars, v.Name
	m.form = form.WithWidth(max(40, m.width-4)).WithHeight(max(8, m.height-5))
	m.mode = modeForm
	return m, m.form.Init()
}

func (m model) completeForm() (tea.Model, tea.Cmd) {
	kind, vars, name := m.formKind, m.vars, m.selectedName
	m.form, m.formKind = nil, formNone

	switch kind {
	case formEdit:
		cpu, mem := vars.cpu, vars.mem
		return m.busy("resize", name, func(ctx context.Context) error {
			return m.client.SetLimits(ctx, name, cpu, mem)
		})
	case formDelete:
		if !vars.confirm {
			m.mode = modeList
			return m, m.setToast("delete cancelled", false)
		}
		return m.busy("delete", name, func(ctx context.Context) error {
			return m.client.Delete(ctx, name)
		})
	case formSnapManage:
		return m.completeSnapManage(name, vars)
	case formLaunch:
		m.pendingLaunch = xincus.CreateSpec{
			Name:             vars.name,
			ImageFingerprint: vars.imageFP,
			CPU:              vars.cpu,
			Memory:           vars.mem,
			DiskSize:         vars.disk,
		}
		m.editor.SetValue(vars.cloud)
		m.editor.SetWidth(max(20, m.width-4))
		m.editor.SetHeight(max(6, m.height-8))
		m.mode = modeLaunchEdit
		return m, m.editor.Focus()
	}
	m.mode = modeList
	return m, nil
}

func (m model) completeSnapManage(name string, vars *formVars) (tea.Model, tea.Cmd) {
	switch {
	case vars.action == "create":
		snap := vars.name
		return m.busy("snapshot", name, func(ctx context.Context) error {
			return m.client.Snapshot(ctx, name, snap)
		})
	case strings.HasPrefix(vars.action, "restore:"):
		if !vars.confirm {
			m.mode = modeList
			return m, m.setToast("restore cancelled", false)
		}
		snap := strings.TrimPrefix(vars.action, "restore:")
		return m.busy("restore", name, func(ctx context.Context) error {
			return m.client.RestoreSnapshot(ctx, name, snap)
		})
	case strings.HasPrefix(vars.action, "delete:"):
		if !vars.confirm {
			m.mode = modeList
			return m, m.setToast("snapshot delete cancelled", false)
		}
		snap := strings.TrimPrefix(vars.action, "delete:")
		return m.busy("del-snapshot", name, func(ctx context.Context) error {
			return m.client.DeleteSnapshot(ctx, name, snap)
		})
	}
	m.mode = modeList
	return m, nil
}

func (m model) updateEditor(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "esc":
			// Back to the (pre-filled) launch form instead of discarding the wizard.
			m.vars.cloud = m.editor.Value()
			m.formKind = formLaunch
			m.form = newLaunchForm(m.launchImages, m.launchTemplates, m.vars).
				WithWidth(max(40, m.width-4)).WithHeight(max(12, m.height-5))
			m.mode = modeForm
			return m, m.form.Init()
		case "ctrl+s":
			content := m.editor.Value()
			if err := xincus.ValidateCloudInit(content); err != nil {
				return m, toastAfter(err.Error(), true)
			}
			spec := m.pendingLaunch
			spec.CloudInitUser = content
			return m.busy("launch", spec.Name, func(ctx context.Context) error {
				return m.client.CreateVM(ctx, spec)
			})
		}
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

// --- actions -----------------------------------------------------------------

func (m model) startLaunch() (tea.Model, tea.Cmd) {
	m.mode, m.busyText = modeBusy, "loading images…"
	return m, loadLaunchData(m.client)
}

func (m model) openLogs() (tea.Model, tea.Cmd) {
	v, ok := m.activeVM()
	if !ok {
		return m, nil
	}
	m.selectedName = v.Name
	m.logsShowCloudInit = false
	m.mode = modeLogs
	m.logs.SetContent("loading console log…")
	return m, fetchConsoleLog(m.client, v.Name)
}

func (m model) startShell() (tea.Model, tea.Cmd) {
	v, ok := m.activeVM()
	if !ok {
		return m, nil
	}
	if !v.Running() {
		return m, toastAfter(v.Name+" is not running", true)
	}
	if !v.AgentReady {
		return m, toastAfter("waiting for guest agent on "+v.Name+"…", true)
	}
	if _, err := exec.LookPath("incus"); err != nil {
		return m, toastAfter("shell-in needs the 'incus' CLI on PATH", true)
	}
	// Prefer bash, fall back to sh for minimal images (alpine, etc.).
	c := exec.Command("incus", "exec", v.Name, "--",
		"sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh")
	return m, tea.ExecProcess(c, func(err error) tea.Msg { return execDoneMsg{err: err} })
}

func (m model) freezeToggle() (tea.Model, tea.Cmd) {
	v, ok := m.activeVM()
	if !ok {
		return m, nil
	}
	switch {
	case v.StatusCode == api.Frozen:
		return m.busy("resume", v.Name, func(ctx context.Context) error { return m.client.Unfreeze(ctx, v.Name) })
	case v.Running():
		return m.busy("pause", v.Name, func(ctx context.Context) error { return m.client.Freeze(ctx, v.Name) })
	default:
		return m, toastAfter("can only pause a running VM", true)
	}
}

func (m model) copyIP() (tea.Model, tea.Cmd) {
	v, ok := m.activeVM()
	if !ok {
		return m, nil
	}
	if v.IPv4 == "" {
		return m, toastAfter("no IP yet (agent not ready?)", true)
	}
	return m, tea.Batch(tea.SetClipboard(v.IPv4), toastAfter("copied "+v.IPv4, false))
}

func (m model) actionOp(action string, fn func(*xincus.Client, context.Context, string) error) (tea.Model, tea.Cmd) {
	v, ok := m.activeVM()
	if !ok {
		return m, nil
	}
	return m.busy(action, v.Name, func(ctx context.Context) error { return fn(m.client, ctx, v.Name) })
}

func (m model) busy(action, name string, fn func(context.Context) error) (tea.Model, tea.Cmd) {
	// Backstop deadline so a hung op eventually fails even without esc; esc cancels sooner.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	m.cancel = cancel
	m.mode, m.busyText = modeBusy, action+" "+name
	return m, runOp(ctx, cancel, action, name, fn)
}

func (m model) quit() (tea.Model, tea.Cmd) {
	if !m.quitting {
		m.quitting = true
		close(m.eventsDone)
	}
	return m, tea.Quit
}

// periodicLoad issues a background VM refresh unless one is already in flight, so a
// slow daemon can't accumulate a backlog of overlapping ListVMs calls from the tick
// and event streams. The flag is cleared when the vmsMsg result lands.
func (m *model) periodicLoad() tea.Cmd {
	if m.loadingVMs {
		return nil
	}
	m.loadingVMs = true
	return loadVMs(m.client)
}

// --- helpers -----------------------------------------------------------------

// setToast sets a transient message and returns a Cmd that clears it after a delay,
// using a sequence id so a stale timer can't clear a newer toast.
func (m *model) setToast(text string, isErr bool) tea.Cmd {
	m.toastSeq++
	m.toast, m.toastErr = text, isErr
	return clearToastCmd(m.toastSeq)
}

func (m *model) setLogsContent(content string, err error, empty string) {
	switch {
	case err != nil && strings.TrimSpace(content) != "":
		m.logs.SetContent(content + "\n\n[error: " + err.Error() + "]")
	case err != nil:
		m.logs.SetContent("error: " + err.Error())
	case strings.TrimSpace(content) == "":
		m.logs.SetContent(empty)
	default:
		m.logs.SetContent(content)
		m.logs.GotoBottom()
	}
}

// current returns the VM under the table cursor.
func (m model) current() (xincus.VM, bool) {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.filtered) {
		return xincus.VM{}, false
	}
	return m.filtered[i], true
}

func (m model) vmByName(name string) (xincus.VM, bool) {
	for _, v := range m.vms {
		if v.Name == name {
			return v, true
		}
	}
	return xincus.VM{}, false
}

// activeVM returns the VM the next action should target: the detail/logs subject,
// otherwise the table selection.
func (m model) activeVM() (xincus.VM, bool) {
	if m.mode == modeDetail || m.mode == modeLogs {
		return m.vmByName(m.selectedName)
	}
	return m.current()
}

func (m *model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filterInput.Value()))
	if q == "" {
		m.filtered = m.vms
	} else {
		fl := make([]xincus.VM, 0, len(m.vms))
		for _, v := range m.vms {
			if strings.Contains(strings.ToLower(v.Name), q) {
				fl = append(fl, v)
			}
		}
		m.filtered = fl
	}
	m.syncTable()
}

// syncTable rebuilds the table columns and rows (responsive to width) and preserves
// the selection by name, defaulting to the top row when it disappeared.
func (m *model) syncTable() {
	cols := visibleCols(m.width)

	var sel string
	if i := m.table.Cursor(); i >= 0 && i < len(m.filtered) {
		sel = m.filtered[i].Name
	}

	tcols := make([]table.Column, len(cols))
	for i, c := range cols {
		tcols[i] = table.Column{Title: c.title, Width: c.width}
	}
	// Clear rows before shrinking the column set: bubbles' SetColumns re-renders the
	// existing (wider) rows against the new, shorter column slice and panics with an
	// index-out-of-range on resize. Clearing first avoids the mismatch.
	m.table.SetRows(nil)
	m.table.SetColumns(tcols)

	rows := make([]table.Row, len(m.filtered))
	for i, v := range m.filtered {
		cells := make(table.Row, len(cols))
		for j, c := range cols {
			cells[j] = c.cell(v)
		}
		rows[i] = cells
	}
	m.table.SetRows(rows)

	cur := 0
	for i, v := range m.filtered {
		if v.Name == sel {
			cur = i
			break
		}
	}
	m.table.SetCursor(cur)
}

func (m *model) refreshDetail() {
	if v, ok := m.vmByName(m.selectedName); ok {
		m.detail.SetContent(renderDetail(m.styles, v))
		return
	}
	m.detail.SetContent("VM " + m.selectedName + " no longer exists.")
}

// layout sizes every component to the current terminal dimensions.
func (m *model) layout() {
	if m.width == 0 || m.height == 0 {
		return
	}
	m.help.SetWidth(m.width)
	helpH := 1
	if m.help.ShowAll {
		helpH = min(6, max(1, m.height-5))
	}
	bodyH := max(3, m.height-2-helpH) // header line + status line
	if !m.help.ShowAll && bodyH > m.height-3 {
		bodyH = m.height - 3
	}

	m.table.SetWidth(m.width)
	m.table.SetHeight(bodyH)
	m.table.Focus()
	m.syncTable()

	m.detail.SetWidth(m.width)
	m.detail.SetHeight(bodyH)
	m.logs.SetWidth(m.width)
	m.logs.SetHeight(bodyH)

	m.filterInput.SetWidth(max(10, m.width-12))
	m.editor.SetWidth(max(20, m.width-4))
	m.editor.SetHeight(max(6, m.height-8))

	if m.form != nil {
		m.form = m.form.WithWidth(max(40, m.width-4)).WithHeight(max(8, m.height-5))
	}
}
