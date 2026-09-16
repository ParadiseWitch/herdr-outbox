// Package ui implements the herdr-outbox Bubble Tea terminal interface.
package ui

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/api"
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
)

type focusArea int

const (
	focusList focusArea = iota
	focusDetail
)

type filterMode int

const (
	filterAll filterMode = iota
	filterOpen
	filterSent
)

func (f filterMode) Label() string {
	switch f {
	case filterOpen:
		return "待处理"
	case filterSent:
		return "已发送"
	default:
		return "全部"
	}
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
	}
	remoteEditDoneMsg struct {
		id      string
		tmpPath string
	}
	noticeMsg struct{ text string }
)

type tickMsg time.Time

type Options struct {
	Store   *store.Store
	Engine  *scheduler.Engine
	Provider dataprovider.DataProvider
	Poll    time.Duration
	NoTimer bool
}

type Model struct {
	provider dataprovider.DataProvider
	sched    dataprovider.Scheduler
	poll     time.Duration

	width, height int
	ready         bool

	msgs       []*model.Message
	cursor     int
	selectedID string
	filter     filterMode
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

	renameInput string
	renameID    string

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
		provider: prov,
		poll:     opts.Poll,
		NoTimer:  opts.NoTimer,
	}
	if s, ok := prov.(dataprovider.Scheduler); ok {
		m.sched = s
	}
	if m.poll <= 0 {
		m.poll = time.Second
	}
	return m
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.loadMsg(), m.snapshotCmd(), m.tick()}
	if m.sched != nil {
		cmds = append(cmds, m.reconcileCmd())
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

	case panePreviewMsg:
		// Only update if this is still the selected pane.
		if m.mode == modeTargetPicker && m.picker.step == 1 && m.picker.cursor < len(m.picker.items) {
			item := m.picker.items[m.picker.cursor]
			if item.pane.ID == msg.paneID {
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
	for _, msg := range m.msgs {
		switch m.filter {
		case filterOpen:
			if msg.Status == model.StatusSent {
				continue
			}
		case filterSent:
			if msg.Status != model.StatusSent {
				continue
			}
		}
		out = append(out, msg)
	}
	return out
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
	if msg == nil || m.snapshot == nil {
		return herdr.Pane{}, false
	}
	return m.snapshot.Resolve(msg.Target)
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
