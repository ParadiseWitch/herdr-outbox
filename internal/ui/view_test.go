package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
	"herdr-outbox/internal/store"
)

func newTestModel(t *testing.T, width, height int) (*Model, *store.Store, *herdr.Fake) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	led, err := scheduler.OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake := herdr.NewFake()
	fake.Workspaces = []herdr.Workspace{{ID: "w1", Label: "研究", Focused: true}}
	fake.AddPane(herdr.Pane{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", Agent: "qodercli", AgentStatus: herdr.StatusWorking})
	eng := scheduler.NewEngine(st, fake, led)
	prov := &LocalProvider{Store: st, Engine: eng}
	m := New(Options{Provider: prov, NoTimer: true})
	m.width, m.height = width, height
	m.ready = true
	snap, err := fake.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.snapshot = snap
	return m, st, fake
}

func (m *Model) reload(t *testing.T) {
	t.Helper()
	msgs, err := m.provider.ListMessages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.msgs = msgs
	if len(msgs) > 0 {
		m.selectedID = msgs[m.cursor].ID
	}
}

// The frame must never be taller than the terminal or wider than its right edge:
// one overflowing row makes the terminal wrap and the whole layout shifts.
func TestFrameFitsTheTerminal(t *testing.T) {
	m, st, _ := newTestModel(t, 100, 30)
	if _, err := st.Create(&model.Message{
		Content: "# 重构 API\n\n这是一段很长的中文正文，用来验证宽字符不会被截断到错误的位置。\n第二行\n",
		Target:  model.Target{Workspace: "w1", Pane: "w1:p1", PaneLabel: "qodercli-1"},
		Trigger: model.Trigger{Kind: model.TriggerManual},
		Status:  model.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)

	for _, tc := range []struct {
		name    string
		prepare func()
	}{
		{"browse", func() {}},
		{"help", func() { m.mode = modeHelp }},
		{"log", func() { m.mode = modeLog }},
		{"confirm", func() { m.mode = modeConfirmDelete; m.deleteHold = m.selected() }},
		{"picker", func() {
			items := []pickerItem{{key: "w1:p1", label: "qodercli-1", hint: "working │ x"}}
			m.mode = modeTargetPicker
			m.picker = picker{title: "target pane", step: 1, itemsAll: items, items: items}
		}},
	} {
		tc.prepare()
		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > m.height {
			t.Fatalf("%s: frame is %d lines, terminal has %d\n%s", tc.name, len(lines), m.height, view)
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w > m.width {
				t.Fatalf("%s: line %d is %d columns wide in a %d column pane: %q", tc.name, i+1, w, m.width, ansi.Strip(l))
			}
		}
		m.mode = modeBrowse
		m.deleteHold = nil
		m.picker = picker{}
	}
}

func TestPanelsAreDividedOnEveryRow(t *testing.T) {
	m, st, _ := newTestModel(t, 100, 30)
	if _, err := st.Create(&model.Message{
		Content: "hello target pane",
		Target:  model.Target{Workspace: "w1", Pane: "w1:p1", PaneLabel: "qodercli-1"},
		Trigger: model.Trigger{Kind: model.TriggerAfterCompletion},
		Status:  model.StatusWaiting,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	lines := strings.Split(m.View(), "\n")
	if len(lines) < 8 {
		t.Fatalf("frame too short to judge: %d lines", len(lines))
	}
	divCol := m.leftWidth()
	bodyStart, bodyEnd := 2, len(lines)-2
	found := 0
	for _, l := range lines[bodyStart:bodyEnd] {
		plain := ansi.Strip(l)
		// Measure with the same function the layout pads with. Walking rune by rune
		// with runewidth disagrees about box drawing and symbols like ◎, and reports
		// a divider that is perfectly aligned as one column off.
		if i := strings.IndexRune(plain, '│'); i >= 0 && ansi.StringWidth(plain[:i]) == divCol {
			found++
		}
	}
	if found < bodyEnd-bodyStart-2 {
		t.Fatalf("divider column present on %d of %d body rows", found, bodyEnd-bodyStart)
	}
}

func TestStatusLineShowsLivePane(t *testing.T) {
	m, st, fake := newTestModel(t, 116, 30)
	if _, err := st.Create(&model.Message{
		Content: "ping",
		Target:  model.Target{Workspace: "w1", WorkspaceLabel: "研究", Pane: "w1:p1", PaneLabel: "qodercli-1", Agent: "qodercli"},
		Trigger: model.Trigger{Kind: model.TriggerManual},
		Status:  model.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	fake.SetStatus("w1:p1", herdr.StatusIdle)
	fake.Panes[0].Focused = true
	snap, err := m.provider.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.snapshot = snap

	status := ansi.Strip(m.statusView(m.width))
	for _, want := range []string{"工作区:", "面板:", "代理:"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status line %q lacks %q", status, want)
		}
	}
}

func TestDetailListsTargetTriggerAndStatus(t *testing.T) {
	m, st, _ := newTestModel(t, 116, 30)
	sendAt := time.Now().Add(2 * time.Hour)
	if _, err := st.Create(&model.Message{
		Content: "# ship it\n\nbody line\n",
		Target:  model.Target{Workspace: "w1", WorkspaceLabel: "研究", Pane: "w1:p1", PaneLabel: "qodercli-1", Agent: "qodercli"},
		Trigger: model.Trigger{Kind: model.TriggerScheduled, SendAt: &sendAt},
		Status:  model.StatusScheduled,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	view := ansi.Strip(m.View())
	for _, want := range []string{"目标: 研究 / qodercli-1", "触发: 定时", "状态: 定时", "body line"} {
		if !strings.Contains(view, want) {
			t.Fatalf("detail is missing %q in:\n%s", want, view)
		}
	}
	if strings.Contains(view, "研究 / 研究") {
		t.Fatalf("target repeated the workspace:\n%s", view)
	}
}

// A waiting message can be waiting on something that will never happen. The list
// has to say so rather than show a bare herdr status word.
func TestWaitingRowShowsWhy(t *testing.T) {
	m, st, _ := newTestModel(t, 116, 30)
	if _, err := st.Create(&model.Message{
		Content:   "to a shell",
		Target:    model.Target{Workspace: "w1", WorkspaceLabel: "研究", Pane: "w1:p2"},
		Trigger:   model.Trigger{Kind: model.TriggerAfterCompletion},
		Status:    model.StatusWaiting,
		LastError: "该面板没有可等待的代理",
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "等待中: 该面板没有可等待的代理") {
		t.Fatalf("waiting row hides the reason:\n%s", view)
	}
}
