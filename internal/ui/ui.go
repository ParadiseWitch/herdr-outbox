// Package ui implements the herdr-outbox Bubble Tea terminal interface.
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/config"
	"herdr-outbox/internal/dataprovider"
	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
	"herdr-outbox/internal/store"
)

type mode int

const (
	modeBrowse mode = iota
	modeTargetPicker
	modeTriggerPicker
	modeConfirmDelete
	modeHelp
	modeLog
	modeRename
	modeEdit
	modeWorkspaceFilter
	modeSearch
)

type focusArea int

const (
	focusList focusArea = iota
	focusDetail
)

type filterMode int

const (
	filterAll filterMode = iota
	filterUnsent
	filterSent
)

func (f filterMode) Label() string {
	switch f {
	case filterUnsent:
		return "未处理"
	case filterSent:
		return "已发送"
	default:
		return "全部"
	}
}

func (f filterMode) Name() string {
	switch f {
	case filterUnsent:
		return "unsent"
	case filterSent:
		return "sent"
	default:
		return "all"
	}
}

func filterFromName(s string) filterMode {
	switch s {
	case "unsent":
		return filterUnsent
	case "sent":
		return filterSent
	default:
		return filterAll
	}
}

type messageFilter struct {
	Status filterMode
	Panes  map[string]bool // qualified pane id -> selected
	Query  string          // search query
}

func (f messageFilter) Active() bool {
	return f.Status != filterAll || len(f.Panes) > 0 || f.Query != ""
}

// filterLabel describes the active scope, then the status tab and the search.
// Scope leads because it is the question the list answers most often: whose
// messages am I looking at. The scope and the status tab are now independent
// dimensions -- the tab is lifecycle (未处理/已发送), the scope is location -- so
// both are named when both are narrowed.
func (m *Model) filterLabel() string {
	parts := []string{m.scopeLabel()}
	if m.filter.Status != filterAll {
		parts = append(parts, m.filter.Status.Label())
	}
	if m.filter.Query != "" {
		parts = append(parts, "搜索:"+m.filter.Query)
	}
	return strings.Join(parts, " · ")
}

// scopeLabel names what the list is limited to. Following the current pane is
// spelled out because the list then changes underneath the reader, and an
// unlabelled change reads as a bug. The pane goes through targetLabel so the
// header names it exactly the way the list does.
func (m *Model) scopeLabel() string {
	switch {
	case len(m.filter.Panes) > 0:
		return fmt.Sprintf("%d 个面板", len(m.filter.Panes))
	case !m.scopeFollow:
		return "全部会话"
	}
	if here, ok := m.currentPaneRef(); ok {
		return "当前面板 " + m.targetLabel(here.Target())
	}
	return "全部会话"
}

type (
	stepDoneMsg struct {
		events []scheduler.Event
		err    error
	}
	snapshotDoneMsg struct {
		snap *herdr.Snapshot
		err  error
	}
	editorDoneMsg struct {
		id  string
		err error
		// saved marks an in-TUI commit, which shares the reload path with the
		// external editor but gets a 已保存 notice instead of 已重新加载.
		saved bool
	}
	remoteEditDoneMsg struct {
		id      string
		tmpPath string
	}
	noticeMsg struct{ text string }

	// builtinEditReadyMsg carries the fetched body into the in-TUI editor.
	builtinEditReadyMsg struct {
		id      string
		content string
	}

	sseEventMsg struct{ event api.Event }

	sseClosedMsg struct{}
)

type tickMsg time.Time

type Options struct {
	Store    *store.Store
	Engine   *scheduler.Engine
	Provider dataprovider.DataProvider
	Poll     time.Duration
	NoTimer  bool
	// Editor selects the in-TUI text area or an external $EDITOR. Empty means builtin.
	Editor config.EditorMode
	// StatePath persists UI session state (last active tab) across runs. Empty disables persistence.
	StatePath string
}

type Model struct {
	provider dataprovider.DataProvider
	sched    dataprovider.Scheduler
	poll     time.Duration
	eventCh  <-chan api.Event

	width, height int
	ready         bool

	msgs       []*model.Message
	cursor     int
	selectedID string
	filter     messageFilter
	focus      focusArea

	snapshot *herdr.Snapshot
	snapErr  error

	log      []scheduler.Event
	notice   string
	noticeAt time.Time

	mode       mode
	picker     picker
	deleteHold *model.Message
	detailTop  int

	// currentPane is the pane id this process was launched with (HERDR_PANE_ID).
	// A popup has none: herdr clears the variable for popup commands, which is why
	// "where am I" resolves through currentPaneRef rather than this field.
	currentPane string
	// scopeFollow keeps the list limited to the pane the user is in, re-resolved
	// on every snapshot. Clearing the filter turns following off and shows every
	// session's messages.
	scopeFollow bool
	statePath   string

	renameInput string
	renameID    string

	searchInput string

	editorMode config.EditorMode
	textarea   textarea.Model
	editID     string
	// editOriginal is the body as loaded, used to detect unsaved changes.
	editOriginal       string
	editConfirmDiscard bool

	quitting   bool
	stepActive bool
	deferTick  bool
	NoTimer    bool
}

func New(opts Options) *Model {
	prov := opts.Provider
	if prov == nil && opts.Store != nil && opts.Engine != nil {
		prov = &LocalProvider{Store: opts.Store, Engine: opts.Engine}
	}
	m := &Model{
		provider:    prov,
		poll:        opts.Poll,
		NoTimer:     opts.NoTimer,
		editorMode:  config.Config{Editor: opts.Editor}.ResolveEditor(),
		textarea:    newTextarea(),
		currentPane: strings.TrimSpace(os.Getenv("HERDR_PANE_ID")),
		statePath:   opts.StatePath,
	}
	// Inside herdr the list starts limited to the pane the user is in. HERDR_ENV is
	// what to test, not HERDR_PANE_ID: herdr sets it for popup commands as well,
	// which is exactly the case where the pane id is withheld.
	m.scopeFollow = os.Getenv("HERDR_ENV") == "1"
	m.filter.Status = m.loadLastTab()
	if s, ok := prov.(dataprovider.Scheduler); ok {
		m.sched = s
	}
	if m.poll <= 0 {
		m.poll = time.Second
	}
	return m
}

// uiState is the persisted session state: which tab was active at exit.
type uiState struct {
	LastTab string `json:"lastTab"`
}

func (m *Model) loadLastTab() filterMode {
	if m.statePath == "" {
		return m.defaultTab()
	}
	data, err := os.ReadFile(m.statePath)
	if err != nil {
		return m.defaultTab()
	}
	var st uiState
	if json.Unmarshal(data, &st) != nil {
		return m.defaultTab()
	}
	// A saved tab is restored as-is. A state file written before the 当前 tab was
	// removed still names "current"; filterFromName maps that unknown value to
	// 全部, which is the honest reading of a tab that no longer exists.
	return filterFromName(st.LastTab)
}

// defaultTab is what a first launch shows: 未处理, the messages still needing
// attention. The location dimension is separate and lives in the scope, so the
// default tab says nothing about which pane is shown.
func (m *Model) defaultTab() filterMode {
	return filterUnsent
}

// clearScope drops every narrowing the user has applied to *location*, returning
// the list to all messages of all sessions. It leaves the status tab alone: the
// tab is a different dimension (lifecycle), and clearing the location scope must
// not silently change which lifecycle states are shown.
func (m *Model) clearScope() {
	m.filter.Panes = nil
	m.filter.Query = ""
	m.scopeFollow = false
	m.picker.selectedPanes = nil
	m.restoreCursor()
	m.setNotice("已清除筛选: 显示全部会话消息")
}

func (m *Model) persistTab() {
	if m.statePath == "" {
		return
	}
	data, err := json.Marshal(uiState{LastTab: m.filter.Status.Name()})
	if err != nil {
		return
	}
	_ = os.WriteFile(m.statePath, data, 0o644)
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadMsg(), m.snapshotCmd(), m.tick()}
	if m.sched != nil {
		cmds = append(cmds, m.reconcileCmd())
	}
	if ch, err := m.provider.Events(context.Background()); err == nil && ch != nil {
		m.eventCh = ch
		cmds = append(cmds, m.readEventCmd())
	}
	return tea.Batch(cmds...)
}

func (m *Model) tick() tea.Cmd {
	if m.NoTimer {
		return nil
	}
	return tea.Tick(m.poll, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Model) loadMsg() tea.Cmd {
	return func() tea.Msg {
		msgs, err := m.provider.ListMessages(context.Background())
		if err != nil {
			return noticeMsg{text: "加载失败: " + err.Error()}
		}
		return loadedMsg{msgs: msgs}
	}
}

type loadedMsg struct{ msgs []*model.Message }

func (m *Model) reconcileCmd() tea.Cmd {
	if m.sched == nil {
		return nil
	}
	return func() tea.Msg {
		evs := m.sched.Reconcile(context.Background())
		return stepDoneMsg{events: evs}
	}
}

func (m *Model) readEventCmd() tea.Cmd {
	if m.eventCh == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-m.eventCh
		if !ok {
			return sseClosedMsg{}
		}
		return sseEventMsg{event: ev}
	}
}

func (m *Model) reconnectCmd() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(3 * time.Second)
		ch, err := m.provider.Events(context.Background())
		if err != nil || ch == nil {
			return sseClosedMsg{}
		}
		return sseReconnectMsg{ch: ch}
	}
}

type sseReconnectMsg struct{ ch <-chan api.Event }

func (m *Model) stepCmd() tea.Cmd {
	if m.sched == nil {
		return nil
	}
	return func() tea.Msg {
		evs, err := m.sched.Step(context.Background())
		return stepDoneMsg{events: evs, err: err}
	}
}

func (m *Model) snapshotCmd() tea.Cmd {
	return func() tea.Msg {
		snap, err := m.provider.Snapshot(context.Background())
		return snapshotDoneMsg{snap: snap, err: err}
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.logResize()
		if m.mode == modeEdit {
			m.sizeTextarea()
		}
		return m, nil

	case tickMsg:
		cmds := []tea.Cmd{m.snapshotCmd()}
		if m.sched != nil {
			if m.stepActive {
				m.deferTick = true
			} else {
				m.stepActive = true
				cmds = append(cmds, m.stepCmd())
			}
		}
		return m, tea.Batch(cmds...)

	case stepDoneMsg:
		m.stepActive = false
		cmds := []tea.Cmd{}
		if len(msg.events) > 0 {
			m.appendEvents(msg.events)
			cmds = append(cmds, m.loadMsg())
		}
		if msg.err != nil {
			m.snapErr = msg.err
		} else if m.sched != nil {
			if snap, err := m.sched.CachedSnapshot(); err == nil && snap != nil {
				m.snapshot = snap
			}
		}
		if m.deferTick {
			m.deferTick = false
			if m.sched != nil {
				m.stepActive = true
				cmds = append(cmds, m.stepCmd())
			}
		} else {
			cmds = append(cmds, m.tick())
		}
		return m, tea.Batch(cmds...)

	case snapshotDoneMsg:
		if msg.err != nil {
			m.snapErr = msg.err
		} else {
			m.snapshot = msg.snap
			m.snapErr = nil
		}
		return m, nil

	case sseEventMsg:
		se := scheduler.Event{
			Kind:      scheduler.EventKind(msg.event.Kind),
			MessageID: msg.event.MessageID,
			Title:     msg.event.Title,
			Pane:      msg.event.Pane,
			Detail:    msg.event.Detail,
			At:        msg.event.At,
		}
		m.appendEvents([]scheduler.Event{se})
		return m, tea.Batch(m.loadMsg(), m.readEventCmd())

	case sseClosedMsg:
		return m, m.reconnectCmd()

	case sseReconnectMsg:
		m.eventCh = msg.ch
		return m, tea.Batch(m.loadMsg(), m.readEventCmd())

	case loadedMsg:
		m.msgs = msg.msgs
		m.restoreCursor()
		return m, nil

	case noticeMsg:
		m.setNotice(msg.text)
		return m, nil

	case sendDoneMsg:
		if len(msg.events) > 0 {
			m.appendEvents(msg.events)
		}
		if msg.err != nil {
			m.setNotice("发送失败: " + msg.err.Error())
		}
		return m, m.loadMsg()

	case editorDoneMsg:
		return m.afterEdit(msg)

	case remoteEditReadyMsg:
		name, args := resolveEditor()
		cmd := execCommand(name, append(args, msg.tmpPath)...)
		if cmd == nil {
			os.Remove(msg.tmpPath)
			m.setNotice("未找到编辑器；请设置 $EDITOR（VISUAL 优先于 EDITOR）")
			return m, nil
		}
		id, tmpPath := msg.id, msg.tmpPath
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			if err != nil {
				os.Remove(tmpPath)
				return editorDoneMsg{id: id, err: err}
			}
			return remoteEditDoneMsg{id: id, tmpPath: tmpPath}
		})

	case remoteEditDoneMsg:
		return m, m.uploadAfterEdit(msg.id, msg.tmpPath)

	case builtinEditReadyMsg:
		m.mode = modeEdit
		m.editID = msg.id
		m.editOriginal = msg.content
		m.editConfirmDiscard = false
		m.textarea.SetValue(msg.content)
		m.textarea.Focus()
		m.sizeTextarea()
		return m, nil

	case panePreviewMsg:
		// Only update if this is still the selected pane.
		if (m.mode == modeTargetPicker || m.mode == modeWorkspaceFilter) && m.picker.cursor < len(m.picker.treeItems) {
			item := m.picker.treeItems[m.picker.cursor]
			if !item.isParent && item.pane.ID == msg.paneID {
				m.picker.panePreview = msg.content
				m.picker.panePreviewPane = msg.paneID
			}
		}
		return m, nil

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m *Model) setNotice(text string) {
	m.notice = text
	m.noticeAt = time.Now()
}

func (m *Model) appendEvents(evs []scheduler.Event) {
	m.log = append(m.log, evs...)
	if len(m.log) > 200 {
		m.log = m.log[len(m.log)-200:]
	}
	last := evs[len(evs)-1]
	m.setNotice(last.String())
}

func (m *Model) logResize() {}

func (m *Model) visible() []*model.Message {
	out := make([]*model.Message, 0, len(m.msgs))
	panes := m.scopePanes()
	for _, msg := range m.msgs {
		switch m.filter.Status {
		case filterUnsent:
			if msg.Status == model.StatusSent {
				continue
			}
		case filterSent:
			if msg.Status != model.StatusSent {
				continue
			}
		}
		// Keys are qualified: two sessions can each own `w1:p1`, and a bare id
		// would let one session's filter hide another's messages.
		if panes != nil && !panes[m.snapshot.QualifiedPane(msg.Target)] {
			continue
		}
		if m.filter.Query != "" {
			q := strings.ToLower(m.filter.Query)
			// Search the live pane name as well, so a query that matches what the
			// terminal is titled finds the message aimed at it.
			haystack := strings.ToLower(msg.Title + " " + msg.Content + " " + msg.Target.Display() + " " + m.targetDisplay(msg))
			if !strings.Contains(haystack, q) {
				continue
			}
		}
		out = append(out, msg)
	}
	return out
}

// scopePanes is the pane set the list is limited to, or nil for no limit. A
// selection made in the topology tree wins; otherwise the list follows the pane
// the user is in, until the filter is cleared. Following resolves to no limit
// while herdr has nothing focused, so a missing snapshot never blanks the list.
func (m *Model) scopePanes() map[string]bool {
	if len(m.filter.Panes) > 0 {
		return m.filter.Panes
	}
	if !m.scopeFollow {
		return nil
	}
	if here, ok := m.currentPaneRef(); ok {
		return map[string]bool{here.QualifiedID(): true}
	}
	return nil
}

func (m *Model) selected() *model.Message {
	list := m.visible()
	if len(list) == 0 {
		return nil
	}
	if m.cursor >= len(list) {
		m.cursor = len(list) - 1
	}
	return list[m.cursor]
}

func (m *Model) byID(id string) *model.Message {
	for _, msg := range m.msgs {
		if msg.ID == id {
			return msg
		}
	}
	return nil
}

func (m *Model) restoreCursor() {
	list := m.visible()
	if m.selectedID != "" {
		for i, msg := range list {
			if msg.ID == m.selectedID {
				m.cursor = i
				return
			}
		}
	}
	if m.cursor >= len(list) {
		m.cursor = len(list) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if sel := m.selected(); sel != nil {
		m.selectedID = sel.ID
	}
}

func (m *Model) move(delta int) {
	list := m.visible()
	if len(list) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(list) {
		m.cursor = len(list) - 1
	}
	m.detailTop = 0
	m.selectedID = list[m.cursor].ID
}

func (m *Model) paneFor(msg *model.Message) (herdr.Pane, bool) {
	if msg == nil {
		return herdr.Pane{}, false
	}
	return m.paneForTarget(msg.Target)
}

// paneForTarget resolves a stored target against the live herdr snapshot.
func (m *Model) paneForTarget(t model.Target) (herdr.Pane, bool) {
	if m.snapshot == nil {
		return herdr.Pane{}, false
	}
	return m.snapshot.Resolve(t)
}

// currentPaneRef is the one definition of "the pane the user is in": the primary
// session's focused pane in the latest snapshot. The default scope and the 当前
// tab both read it, so the two can never disagree.
//
// A popup has no pane identity of its own — herdr clears HERDR_PANE_ID for popup
// commands — and a popup is not a pane of the layout, so opening one leaves the
// underlying focused pane untouched. That pane is what "the pane before the popup
// opened" means here, which is why following focus works from a popup at all.
func (m *Model) currentPaneRef() (herdr.Pane, bool) {
	if m.snapshot == nil {
		return herdr.Pane{}, false
	}
	if pane, ok := m.snapshot.Focused(); ok {
		return pane, true
	}
	// Nothing focused, or no snapshot yet: the pane this process was launched in
	// is the only remaining clue, and it belongs to the primary session.
	return m.snapshot.Pane(m.currentPane)
}

// defaultTarget is where a new message goes when the user has not chosen a pane.
// Unlike the scope, this prefers the pane this process is running in: the launch
// id is a fact about the command that opened the outbox, not a guess about where
// the user last clicked, and a message aimed somewhere else on first write is the
// more expensive mistake.
func (m *Model) defaultTarget() model.Target {
	if pane, ok := m.snapshot.Pane(m.currentPane); ok {
		return pane.Target()
	}
	if pane, ok := m.currentPaneRef(); ok {
		return pane.Target()
	}
	// No snapshot and no launch id: leave it untargeted rather than guess.
	return model.Target{Pane: m.currentPane}
}

func (m *Model) armIfNeeded(ctx context.Context, msg *model.Message) error {
	if msg.Trigger.Kind == model.TriggerManual {
		if msg.Status == model.StatusDraft {
			msg.Status = model.StatusPending
		}
		fav := msg.Favorite
		_, err := m.provider.UpdateMessage(ctx, msg.ID, api.UpdateRequest{Favorite: &fav})
		return err
	}
	_, err := m.provider.SetTrigger(ctx, msg.ID, msg.Trigger.Kind, msg.Trigger.SendAt)
	return err
}

// Quitting reports a user-requested exit, so the caller can print where the files
// live after the alternate screen is released.
func (m *Model) Quitting() bool { return m.quitting }

// StoreDir returns the data directory for display after exit.
func (m *Model) StoreDir() string {
	if lp, ok := m.provider.(*LocalProvider); ok {
		return lp.Store.Dir
	}
	return ""
}

func (m *Model) String() string { return fmt.Sprintf("outbox(%d)", len(m.msgs)) }

var _ tea.Model = (*Model)(nil)
