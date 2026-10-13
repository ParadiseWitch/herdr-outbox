package ui

import (
	"context"
	"os"
	"path/filepath"
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
	// Tests run in whatever pane launched them, so a real HERDR_PANE_ID would
	// flip the default 当前 tab on and empty every list. HERDR_ENV would arm the
	// following scope, which restricts the list the same way. Pin both off; the
	// tests that care about either set them explicitly.
	t.Setenv("HERDR_PANE_ID", "")
	t.Setenv("HERDR_ENV", "")
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

func TestCurrentTabShowsOnlyThisPane(t *testing.T) {
	m, st, _ := newTestModel(t, 100, 30)
	m.currentPane = "w1:p1"
	for _, tc := range []struct {
		pane string
		want bool
	}{
		{"w1:p1", true},
		{"w2:p9", false},
		{"", false},
	} {
		if _, err := st.Create(&model.Message{
			Content: "x", Target: model.Target{Pane: tc.pane},
			Trigger: model.Trigger{Kind: model.TriggerManual}, Status: model.StatusPending,
		}); err != nil {
			t.Fatal(err)
		}
	}
	m.reload(t)
	m.filter.Status = filterCurrent
	m.restoreCursor()
	if got := len(m.visible()); got != 1 {
		t.Fatalf("当前 tab shows %d messages, want 1", got)
	}
}

func TestLastTabSurvivesARestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	m, _, _ := newTestModel(t, 100, 30)
	m.statePath = statePath
	m.filter.Status = filterSent
	m.persistTab()

	again, _, _ := newTestModel(t, 100, 30)
	again.statePath = statePath
	again.filter.Status = again.loadLastTab()
	if again.filter.Status != filterSent {
		t.Fatalf("restored tab = %v, want sent", again.filter.Status)
	}
}

// Closing on 当前 used to reopen on 全部: a missing HERDR_PANE_ID silently
// rewrote the saved tab. A saved tab must always win.
func TestSavedCurrentTabSurvivesOutsideHerdr(t *testing.T) {
	t.Setenv("HERDR_PANE_ID", "")
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte(`{"lastTab":"current"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _, _ := newTestModel(t, 100, 30)
	m.statePath = statePath
	m.currentPane = ""
	if got := m.loadLastTab(); got != filterCurrent {
		t.Fatalf("restored tab = %v, want current", got)
	}
	// First launch without a saved state still falls back to 全部.
	if got := m.defaultTab(); got != filterAll {
		t.Fatalf("default tab outside herdr = %v, want all", got)
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
		{"target tree", func() { m.openTargetPicker(context.Background(), m.selected()) }},
		{"filter tree", func() { m.openWorkspaceFilter() }},
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

// A pane herdr has renamed or retitled must be named by that live name, not by
// the label cached when the message was written.
func TestTargetNamePrefersLivePaneTitle(t *testing.T) {
	m, st, fake := newTestModel(t, 116, 34)
	fake.Panes[0].TerminalTitleClean = "重构支付模块"
	fake.Panes[0].Label = "支付"
	snap, err := fake.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.snapshot = snap
	if _, err := st.Create(&model.Message{
		Content: "hello",
		Target:  model.Target{Workspace: "w1", WorkspaceLabel: "研究", Pane: "w1:p1", PaneLabel: "qodercli-1", Agent: "qodercli"},
		Trigger: model.Trigger{Kind: model.TriggerManual},
		Status:  model.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)

	view := ansi.Strip(m.View())
	if !strings.Contains(view, "目标: 研究 / 重构支付模块") {
		t.Fatalf("detail does not use the live pane title:\n%s", view)
	}
	// The list row's second line names the target the same way.
	if !strings.Contains(view, "→ 研究 / 重构支付模块") {
		t.Fatalf("list row does not use the live pane title:\n%s", view)
	}

	// A pane that no longer exists keeps the stored label rather than going blank.
	m.snapshot = nil
	if got := m.targetDisplay(m.selected()); got != "研究 / qodercli-1" {
		t.Fatalf("offline target label = %q", got)
	}
}

// The destination decides a message, so it heads the preview panel: the target
// line must come before the body it describes.
func TestDetailPanelHeadsWithTarget(t *testing.T) {
	m, st, _ := newTestModel(t, 116, 34)
	if _, err := st.Create(&model.Message{
		Content: "# 标题\n\n第一行正文\n第二行正文\n",
		Target:  model.Target{Workspace: "w1", WorkspaceLabel: "研究", Pane: "w1:p1", PaneLabel: "qodercli-1", Agent: "qodercli"},
		Trigger: model.Trigger{Kind: model.TriggerManual},
		Status:  model.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	target, body := -1, -1
	for i, l := range lines {
		if target < 0 && strings.Contains(l, "目标: 研究 / qodercli-1") {
			target = i
		}
		if body < 0 && strings.Contains(l, "第一行正文") {
			body = i
		}
	}
	if target < 0 || body < 0 {
		t.Fatalf("detail missing target (%d) or body (%d):\n%s", target, body, strings.Join(lines, "\n"))
	}
	if target > body {
		t.Fatalf("target row %d is below body row %d", target, body)
	}
	// 触发/状态 stay in the footer block, below the body.
	trigger := -1
	for i, l := range lines {
		if trigger < 0 && strings.Contains(l, "触发: 手动") {
			trigger = i
		}
	}
	if trigger < 0 || trigger < body {
		t.Fatalf("触发 row should stay under the body, got line %d (body %d)", trigger, body)
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
