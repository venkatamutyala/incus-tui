package tui

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
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
	modeImages
)

type model struct {
	client *xincus.Client
	styles styles
	keys   keyMap

	help        help.Model
	table       table.Model
	imgTable    table.Model // the modeImages local-image list
	detail      viewport.Model
	logs        viewport.Model
	spinner     spinner.Model
	editor      textarea.Model
	filterInput textinput.Model

	vms      []xincus.VM
	filtered []xincus.VM

	images              []xincus.LocalImage       // modeImages rows
	releases            []xincus.CodespaceRelease // cached for the import picker
	selectedFingerprint string                    // the image the delete-image confirm targets

	width, height int
	mode          mode
	selectedName  string
	filtering     bool
	busyText      string
	cancel        context.CancelFunc // cancels the in-flight busy op (esc)

	// Progress-reporting busy op (the codespace import): importing gates the richer status line;
	// busyProg is the latest ImportProgress; busyStart drives the live elapsed timer.
	importing bool
	busyProg  xincus.ImportProgress
	busyStart time.Time

	form          *huh.Form
	vars          *formVars
	formKind      formKind
	pendingLaunch xincus.CreateSpec
	// cached launch data so editor "esc back to form" doesn't re-fetch.
	launchImages    []xincus.Image
	launchTemplates []xincus.Template

	logsShowCloudInit bool
	logsAuto          bool // auto-refresh the logs view on each tick

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

	// restrictPaging trims the bubbles table's extra paging keys (b, space, u, d, ctrl+u,
	// ctrl+d) so our single-key actions don't leak into table navigation.
	restrictPaging := func(t *table.Model) {
		t.KeyMap.PageUp.SetKeys("pgup")
		t.KeyMap.PageDown.SetKeys("pgdown")
		t.KeyMap.HalfPageUp.SetEnabled(false)
		t.KeyMap.HalfPageDown.SetEnabled(false)
	}
	tbl := table.New()
	restrictPaging(&tbl)
	imgTbl := table.New()
	restrictPaging(&imgTbl)

	m := model{
		client:      c,
		styles:      st,
		keys:        defaultKeys(),
		help:        help.New(),
		table:       tbl,
		imgTable:    imgTbl,
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
	// Wrap long lines (wide image labels, joined snapshot lists, long console-log lines)
	// instead of clipping them off the right edge.
	m.detail.SoftWrap = true
	m.logs.SoftWrap = true
	return m
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
		cmds := []tea.Cmd{tickCmd(), cmd}
		if m.mode == modeLogs && m.logsAuto {
			if m.logsShowCloudInit {
				cmds = append(cmds, fetchCloudInit(m.client, m.selectedName))
			} else {
				cmds = append(cmds, fetchConsoleLog(m.client, m.selectedName))
			}
		}
		return m, tea.Batch(cmds...)

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
		m.importing = false
		if m.mode == modeBusy {
			m.mode = modeList
		}
		// Image ops belong to the images view: land back there and refresh it (not the VM list).
		if msg.action == "delete-image" {
			m.mode = modeImages
			m.layout()
			var cmd tea.Cmd
			if msg.err != nil {
				cmd = m.setToast("delete image "+msg.name+": "+msg.err.Error(), true)
			} else {
				cmd = m.setToast("deleted image "+msg.name, false)
			}
			return m, tea.Batch(loadImages(m.client), cmd)
		}
		var cmd tea.Cmd
		switch {
		case msg.err == nil && msg.action == "launch":
			// Point at the next step — the VM is created but still booting / running cloud-init.
			cmd = m.setToast("launched "+msg.name+" — l: boot logs, s: shell in once it's up", false)
		case msg.err == nil && msg.action == "resize":
			// limits.cpu hotplugs; new memory and a grown disk apply on the next start —
			// cloud images auto-grow the filesystem onto a bigger disk.
			cmd = m.setToast("resize "+msg.name+" — start it to apply; cloud images auto-grow the filesystem", false)
		case msg.err == nil && msg.action == "import":
			// The image now shows in the launch wizard; refresh the images view too.
			cmd = m.setToast("imported "+msg.name+" — press n to launch it", false)
			return m, tea.Batch(loadVMs(m.client), loadImages(m.client), cmd)
		case msg.err == nil:
			cmd = m.setToast(msg.action+" "+msg.name, false)
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
		vars := &formVars{cpu: "2", mem: "2048", disk: "50"}
		m.vars, m.formKind = vars, formLaunch
		m.form = newLaunchForm(msg.images, msg.templates, vars, vmNames(m.vms)).
			WithWidth(formWidth(m.width)).WithHeight(formHeight(m.height))
		m.mode = modeForm
		return m, m.form.Init()

	case codespaceReleasesMsg:
		if m.mode != modeBusy { // user pressed esc while the release list was loading
			return m, nil
		}
		if msg.err != nil {
			m.mode = modeList
			return m, m.setToast("releases: "+msg.err.Error(), true)
		}
		// Fail loud & early: the codespace image is x86_64-only, so an arm64 host can't run it —
		// say so here instead of after a multi-GB download.
		if runtime.GOARCH != "amd64" {
			m.mode = modeList
			return m, m.setToast("the GlueOps codespace image is x86_64-only; this host is "+runtime.GOARCH, true)
		}
		rels := importableReleases(msg.releases)
		if len(rels) == 0 {
			m.mode = modeList
			return m, m.setToast("no importable codespace releases found on github.com/glueops/codespaces", true)
		}
		m.releases = rels
		vars := &formVars{}
		m.vars, m.formKind = vars, formImport
		m.form = newImportForm(rels, m.importedTags(), vars).
			WithWidth(formWidth(m.width)).WithHeight(formHeight(m.height))
		m.mode = modeForm
		return m, m.form.Init()

	case imagesMsg:
		if msg.err != nil {
			return m, m.setToast("images: "+msg.err.Error(), true)
		}
		m.images = msg.images
		m.syncImages()
		return m, nil

	case busyProgressMsg:
		// Interim progress only refreshes the status line; opDoneMsg is still terminal. Re-arm
		// the listener so it keeps draining until the producer closes the channel.
		m.busyProg = msg.p
		return m, listenProgress(msg.ch)

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
	case modeImages:
		var cmd tea.Cmd
		m.imgTable, cmd = m.imgTable.Update(msg)
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
	case modeImages:
		return m.handleImagesKey(k)
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
			m.layout() // size the detail viewport for this mode's 1-line help bar
			m.refreshDetail()
			m.detail.GotoTop() // start a freshly-opened VM at the top, not a stale offset
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
		m.layout() // re-reserve the help-bar rows for the list view
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
		m.layout() // re-reserve the help-bar rows for the list view
		return m, nil
	case key.Matches(k, m.keys.Refresh):
		if m.logsShowCloudInit {
			return m, fetchCloudInit(m.client, m.selectedName)
		}
		return m, fetchConsoleLog(m.client, m.selectedName)
	case k.String() == "a":
		m.logsAuto = !m.logsAuto
		return m, nil
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
	case key.Matches(k, m.keys.Import):
		mm, cmd := m.startImport()
		return mm, cmd, true
	case key.Matches(k, m.keys.Images):
		mm, cmd := m.openImages()
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
		form, vars = newDeleteForm(v)
	default:
		return m, nil
	}
	m.formKind, m.vars, m.selectedName = kind, vars, v.Name
	m.form = form.WithWidth(formWidth(m.width)).WithHeight(formHeight(m.height))
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
	m.form = form.WithWidth(formWidth(m.width)).WithHeight(formHeight(m.height))
	m.mode = modeForm
	return m, m.form.Init()
}

func (m model) completeForm() (tea.Model, tea.Cmd) {
	kind, vars, name := m.formKind, m.vars, m.selectedName
	m.form, m.formKind = nil, formNone

	switch kind {
	case formEdit:
		// The disk field is only present when the VM is stopped (newEditForm gates it), so
		// a running-VM edit carries no disk change and its cpu/ram still apply. diskResizeArg
		// is "" unless the user actually changed it.
		disk := diskResizeArg(vars.diskSeed, vars.disk)
		// When the disk is being grown, the form showed a confirm; a declined confirm cancels
		// the whole edit (the disk is the consequential change). A cpu/ram-only edit (disk=="")
		// never sees the confirm, so don't gate it.
		if disk != "" && !vars.confirm {
			m.mode = modeList
			return m, m.setToast("edit cancelled", false)
		}
		edit := xincus.ResourceEdit{CPU: vars.cpu, Mem: withUnit(vars.mem, "MiB"), Disk: disk}
		return m.busy("resize", name, func(ctx context.Context) error {
			return m.client.SetResources(ctx, name, edit)
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
			Memory:           withUnit(vars.mem, "MiB"),
			DiskSize:         withUnit(vars.disk, "GiB"),
		}
		content := vars.cloud
		if strings.TrimSpace(content) == "" {
			content = blankCloudInitScaffold // teach a first-timer the shape; harmless if launched as-is
		}
		m.editor.SetValue(content)
		m.editor.SetWidth(min(m.width, max(20, m.width-4)))
		m.editor.SetHeight(max(6, m.height-3))
		m.mode = modeLaunchEdit
		return m, m.editor.Focus()
	case formImport:
		if !vars.confirm {
			m.mode = modeList
			return m, m.setToast("import cancelled", false)
		}
		tag := vars.tag
		return m.busyProgress("import", "glueops-codespace-"+tag, func(ctx context.Context, onProgress func(xincus.ImportProgress)) error {
			_, err := m.client.ImportCodespaceImage(ctx, tag, onProgress)
			return err
		})
	case formDeleteImage:
		if !vars.confirm {
			m.mode = modeImages
			return m, m.setToast("delete cancelled", false)
		}
		fp := m.selectedFingerprint
		return m.busy("delete-image", name, func(ctx context.Context) error {
			return m.client.DeleteImage(ctx, fp)
		})
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
			m.form = newLaunchForm(m.launchImages, m.launchTemplates, m.vars, vmNames(m.vms)).
				WithWidth(formWidth(m.width)).WithHeight(formHeight(m.height))
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
				// Blast-radius guard: refuse if the shared pool can't hold the root disk, rather
				// than letting the daemon hit ENOSPC and disturb existing VMs on a dir pool.
				if err := m.client.CheckLaunchSpace(ctx, spec.DiskSize); err != nil {
					return err
				}
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

// startImport opens the GlueOps codespace importer: fetch the release list (busy), then the
// codespaceReleasesMsg handler validates the host arch and opens the release picker.
func (m model) startImport() (tea.Model, tea.Cmd) {
	m.mode, m.busyText, m.importing = modeBusy, "loading releases…", false
	return m, loadReleases(m.client)
}

// openImages enters the local-image management view and (re)loads its rows.
func (m model) openImages() (tea.Model, tea.Cmd) {
	m.mode = modeImages
	m.layout()
	m.imgTable.Focus()
	return m, loadImages(m.client)
}

// handleImagesKey drives the modeImages list: navigate, d to delete the selected image, I to
// import, R to refresh, esc back to the VM list.
func (m model) handleImagesKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(k, m.keys.Back), key.Matches(k, m.keys.Quit):
		m.mode = modeList
		m.layout()
		return m, nil
	case key.Matches(k, m.keys.Import):
		return m.startImport()
	case key.Matches(k, m.keys.Delete):
		return m.startDeleteImage()
	case key.Matches(k, m.keys.Refresh):
		return m, loadImages(m.client)
	}
	var cmd tea.Cmd
	m.imgTable, cmd = m.imgTable.Update(k)
	return m, cmd
}

// startDeleteImage opens a confirm for the image under the cursor.
func (m model) startDeleteImage() (tea.Model, tea.Cmd) {
	img, ok := m.currentImage()
	if !ok {
		return m, nil
	}
	form, vars := newDeleteImageForm(img)
	m.formKind, m.vars = formDeleteImage, vars
	m.selectedFingerprint, m.selectedName = img.Fingerprint, imageLabel(img)
	m.form = form.WithWidth(formWidth(m.width)).WithHeight(formHeight(m.height))
	m.mode = modeForm
	return m, m.form.Init()
}

// busyProgress runs a long, progress-reporting op (the codespace import). Like busy() it is
// esc-cancelable and backstopped, but it also streams ImportProgress into the status line. A cap-1
// channel carries progress; the op goroutine is its SOLE owner and closes it on every exit path
// (defer close), so the listener never sends on — nor closes — a live channel. Cancellation is
// ctx-cancel only. Coalescing + throttling live in the onProgress adapter here, keeping the service
// callback dumb and all concurrency in this one testable place.
func (m model) busyProgress(action, name string, fn func(context.Context, func(xincus.ImportProgress)) error) (tea.Model, tea.Cmd) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	m.cancel = cancel
	m.mode, m.busyText = modeBusy, action+" "+name
	m.importing, m.busyProg, m.busyStart = true, xincus.ImportProgress{}, time.Now()

	progCh := make(chan xincus.ImportProgress, 1)
	var lastEmit time.Time
	lastPhase := ""
	onProgress := func(p xincus.ImportProgress) {
		// Throttle the high-frequency byte updates so we don't re-render the whole frame on every
		// 1 MB read; but let phase changes through immediately.
		now := time.Now()
		phaseChanged := p.Phase != lastPhase
		if !phaseChanged && now.Sub(lastEmit) < 200*time.Millisecond {
			return
		}
		lastEmit, lastPhase = now, p.Phase
		if phaseChanged {
			// A phase (resolve→download→assemble→import) emits exactly once, so drop-coalescing
			// could lose it and freeze the status line on the previous phase. Block until the
			// listener drains (fast — cap-1) or the op is cancelled. Runs on the producer goroutine.
			select {
			case progCh <- p:
			case <-ctx.Done():
			}
			return
		}
		select {
		case progCh <- p: // cap-1
		default: // coalesce — drop if the listener hasn't drained the previous byte update yet
		}
	}
	run := func() tea.Msg {
		defer cancel()
		defer close(progCh) // sole owner, closes exactly once on success/error/cancel/panic-return
		return opDoneMsg{action: action, name: name, err: fn(ctx, onProgress)}
	}
	return m, tea.Batch(run, listenProgress(progCh))
}

// currentImage returns the local image under the images-table cursor.
func (m model) currentImage() (xincus.LocalImage, bool) {
	i := m.imgTable.Cursor()
	if i < 0 || i >= len(m.images) {
		return xincus.LocalImage{}, false
	}
	return m.images[i], true
}

// importedTags maps a release tag → true when its per-tag alias (glueops-codespace-<tag>) is
// already in the local store, so the picker can mark it "✓ imported". Best-effort: it reflects the
// last images load (the import-time alias-gate is the authoritative idempotency check).
func (m model) importedTags() map[string]bool {
	const prefix = "glueops-codespace-"
	out := make(map[string]bool)
	for _, img := range m.images {
		for _, a := range img.Aliases {
			if strings.HasPrefix(a, prefix) {
				out[strings.TrimPrefix(a, prefix)] = true
			}
		}
	}
	return out
}

// importableReleases keeps only releases that actually carry a codespace image (HasImage), so the
// picker never offers a tag whose import would immediately fail.
func importableReleases(rels []xincus.CodespaceRelease) []xincus.CodespaceRelease {
	out := make([]xincus.CodespaceRelease, 0, len(rels))
	for _, r := range rels {
		if r.HasImage {
			out = append(out, r)
		}
	}
	return out
}

// syncImages rebuilds the images-table columns and rows from m.images, sizing the flexible ALIAS
// column to the current width so the row never overflows (the AltScreen clips, it doesn't wrap).
func (m *model) syncImages() {
	fixed := 16 + 10 + 12 + 14 + 2*5 // TYPE+SIZE+CREATED+FINGERPRINT + ~2 pad/col
	aliasW := max(16, m.width-fixed)
	cols := []table.Column{
		{Title: "ALIAS", Width: aliasW},
		{Title: "TYPE", Width: 16},
		{Title: "SIZE", Width: 10},
		{Title: "CREATED", Width: 12},
		{Title: "FINGERPRINT", Width: 14},
	}
	m.imgTable.SetRows(nil) // clear before shrinking columns (bubbles panics otherwise)
	m.imgTable.SetColumns(cols)
	rows := make([]table.Row, len(m.images))
	for i, img := range m.images {
		created := ""
		if !img.CreatedAt.IsZero() {
			created = img.CreatedAt.Format("2006-01-02")
		}
		rows[i] = table.Row{
			imageLabel(img), orDash(img.Type), formatBytes(img.Size), created,
			img.Fingerprint[:min(12, len(img.Fingerprint))],
		}
	}
	m.imgTable.SetRows(rows)
}

func (m model) openLogs() (tea.Model, tea.Cmd) {
	v, ok := m.activeVM()
	if !ok {
		return m, nil
	}
	m.selectedName = v.Name
	m.logsShowCloudInit = false
	m.logsAuto = true // tail live by default; 'a' turns it off to read scrollback
	m.mode = modeLogs
	m.layout() // size the logs viewport for this mode's 1-line help bar
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
		// Cancel any in-flight op (e.g. a codespace import) so its ctx-tied read loops unwind and
		// their deferred temp-dir cleanup runs — quitting via ctrl+c otherwise orphans a partial
		// multi-GB download (the next import also sweeps stale dirs as a backstop).
		if m.cancel != nil {
			m.cancel()
		}
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
	// Collapse whitespace (incl. newlines) to a single line — a multi-line toast (e.g. a
	// YAML or daemon error) would otherwise grow the status row and clip the help bar off
	// the bottom of the fixed-height frame.
	m.toast, m.toastErr = strings.Join(strings.Fields(text), " "), isErr
	d := 5 * time.Second
	if isErr {
		d = 10 * time.Second // errors linger so a glance-away doesn't miss a failure
	}
	return clearToastCmd(m.toastSeq, d)
}

// vmNames returns the current VM names (used to reject a duplicate in the launch form).
func vmNames(vms []xincus.VM) []string {
	names := make([]string, len(vms))
	for i, v := range vms {
		names[i] = v.Name
	}
	return names
}

// helpRows is the number of lines the bottom help bar occupies. The multi-line cheat
// sheet is only rendered in the list/busy views, so reserve it only there; everywhere
// else (and on short terminals where it wouldn't fit) it stays a single line. layout()
// reserves exactly this and bottomBar() never exceeds it, keeping the frame at m.height.
func (m model) helpRows() int {
	if m.help.ShowAll && (m.mode == modeList || m.mode == modeBusy) {
		return min(6, max(1, m.height-5))
	}
	return 1
}

// formWidth is a huh-form content width that still fits the terminal once wrapped in
// styles.box (border+padding = 4 cols), so narrow windows don't overflow.
func formWidth(termW int) int {
	return max(20, termW-4)
}

// formHeight budgets a huh form's height so its bordered box, huh's footer line, and a 1–2
// line inline validation error all fit inside the body region (bodyH = termH-2-helpRows,
// with helpRows==1 in form mode) without pushing the status/help bars off-screen. The box
// adds 2 rows and huh a +1 footer, so termH-8 leaves ~2 rows of headroom for an error.
// frameView() also hard-pins the body, so this only needs to be close, not exact.
func formHeight(termH int) int {
	return max(8, termH-8)
}

func (m *model) setLogsContent(content string, err error, empty string) {
	// Stay pinned to the tail across a refresh, but don't yank a reader who scrolled up.
	atBottom := m.logs.AtBottom()
	switch {
	case err != nil && strings.TrimSpace(content) != "":
		m.logs.SetContent(content + "\n\n[error: " + err.Error() + "]")
	case err != nil:
		m.logs.SetContent("error: " + err.Error())
	case strings.TrimSpace(content) == "":
		m.logs.SetContent(empty)
	default:
		m.logs.SetContent(content)
		if atBottom {
			m.logs.GotoBottom()
		}
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
	bodyH := max(1, m.height-2-m.helpRows()) // header line + status line

	m.table.SetWidth(m.width)
	m.table.SetHeight(bodyH)
	m.table.Focus()
	m.syncTable()

	m.imgTable.SetWidth(m.width)
	m.imgTable.SetHeight(bodyH)
	m.syncImages()

	m.detail.SetWidth(m.width)
	m.detail.SetHeight(bodyH)
	m.logs.SetWidth(m.width)
	m.logs.SetHeight(bodyH)

	m.filterInput.SetWidth(max(10, m.width-12))
	m.editor.SetWidth(min(m.width, max(20, m.width-4)))
	m.editor.SetHeight(max(6, m.height-3))

	if m.form != nil {
		m.form = m.form.WithWidth(formWidth(m.width)).WithHeight(formHeight(m.height))
	}
}
