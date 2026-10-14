package ui

import (
	"context"
	"testing"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
	"herdr-outbox/internal/store"
)

// twoSessions is a merged snapshot of two herdr servers that use the *same* ids:
// each counted out a workspace `w1`, a tab `w1:t1` and a focused pane `w1:p1`.
// Nothing here can pass by reading the first match, because there is never one
// match to find.
//
// "default" is the primary, the session this TUI runs in; "note" is a foreign one.
func twoSessions(t *testing.T) *herdr.Snapshot {
	t.Helper()

	build := func(session, wsLabel, paneLabel, agent string) *herdr.Snapshot {
		ws := []herdr.Workspace{{ID: "w1", Label: wsLabel}}
		panes := []herdr.Pane{{
			ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1",
			Label: paneLabel, Agent: agent, AgentStatus: herdr.StatusWorking, Focused: true,
		}}
		return herdr.BuildSession(session, ws, nil, panes, nil)
	}

	a := build("default", "主区", "本机面板", "qodercli")
	b := build("note", "笔记区", "笔记面板", "claude")

	return &herdr.Snapshot{
		Primary:    "default",
		Workspaces: append(append([]herdr.Workspace{}, a.Workspaces...), b.Workspaces...),
		Tabs:       append(append([]herdr.Tab{}, a.Tabs...), b.Tabs...),
		Panes:      append(append([]herdr.Pane{}, a.Panes...), b.Panes...),
		Sessions: []herdr.Session{
			{Name: "default", Running: true, Default: true},
			{Name: "note", Running: true},
		},
	}
}

func put(t *testing.T, m *Model, content string, target model.Target) {
	t.Helper()
	prov := m.provider.(*LocalProvider)
	if _, err := prov.Store.Create(&model.Message{
		Content: content,
		Target:  target,
		Trigger: model.Trigger{Kind: model.TriggerManual},
		Status:  model.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
}

func at(session string) model.Target {
	return model.Target{Session: session, Workspace: "w1", Pane: "w1:p1"}
}

// sessionsOf names the sessions a set of messages belongs to, for failures that
// have to say *which* session leaked in.
func sessionsOf(msgs []*model.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		s := msg.Target.Session
		if s == "" {
			s = "(primary)"
		}
		out = append(out, s)
	}
	return out
}

// Both sessions have a focused pane with the same id, so following has to resolve
// it inside the session this TUI runs in and pick up only that one's messages.
func TestScopeFollowsTheFocusedPaneOfThisSession(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.snapshot = twoSessions(t)
	m.scopeFollow = true
	put(t, m, "主区消息", at("default"))
	put(t, m, "笔记消息", at("note"))
	m.reload(t)

	// Asserted by session, not by content: the store keeps the body verbatim and
	// appends a newline, so a string compare here would test the fixture.
	got := m.visible()
	if len(got) != 1 || m.snapshot.SessionOf(got[0].Target) != "default" {
		t.Fatalf("visible = %d from session %v, want this session's message only",
			len(got), sessionsOf(got))
	}
	// The scope headline carries the full path, session included.
	if label := m.scopeLabel(); label != "当前面板 default / 主区 / 标签 t1 / 本机面板" {
		t.Fatalf("scope label = %q", label)
	}
	// Scope (location) and the status tab (lifecycle) are independent dimensions,
	// so the header names both when both are narrowed.
	m.filter.Status = filterQueued
	if got := m.filterLabel(); got != "当前面板 default / 主区 / 标签 t1 / 本机面板 · 队列中" {
		t.Fatalf("filter label = %q, want scope and tab both named", got)
	}
}

// A popup has no HERDR_PANE_ID, so the launch id names nothing; the list still has
// to fill from the focused pane instead of showing an empty inbox.
func TestScopeIsNotEmptyInAPopup(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.snapshot = twoSessions(t)
	m.currentPane = ""
	m.scopeFollow = true
	put(t, m, "主区消息", at("default"))
	put(t, m, "笔记消息", at("note"))
	m.reload(t)

	if got := len(m.visible()); got != 1 {
		t.Fatalf("popup scope shows %d messages, want the focused pane's 1", got)
	}
}

// Following resolves to no restriction while herdr reports nothing focused: a
// snapshot that has not landed yet must not blank the list.
func TestScopeWithNoFocusedPaneShowsEverything(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	snap := twoSessions(t)
	for i := range snap.Panes {
		snap.Panes[i].Focused = false
	}
	m.snapshot = snap
	m.scopeFollow = true
	put(t, m, "主区消息", at("default"))
	put(t, m, "笔记消息", at("note"))
	m.reload(t)

	if got := len(m.visible()); got != 2 {
		t.Fatalf("visible = %d, want both with nothing to follow", got)
	}
	if m.scopePanes() != nil {
		t.Fatalf("scopePanes = %v, want no limit", m.scopePanes())
	}
}

func TestClearScopeRevealsEverySession(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.snapshot = twoSessions(t)
	m.scopeFollow = true
	m.filter.Status = filterQueued // both messages are pending-manual, so the tab is not what limits the list
	put(t, m, "主区消息", at("default"))
	put(t, m, "笔记消息", at("note"))
	m.reload(t)
	if got := len(m.visible()); got != 1 {
		t.Fatalf("following scope shows %d, want the focused pane only", got)
	}

	pressRune(t, m, 'c')

	if got := len(m.visible()); got != 2 {
		t.Fatalf("after clearing: %d messages, want both", got)
	}
	if m.scopeFollow {
		t.Fatal("clearing left the scope following")
	}
	// Clearing is a location action; the lifecycle tab is a different dimension
	// and survives untouched.
	if m.filter.Status != filterQueued {
		t.Fatalf("status tab after clearing = %v, want it left alone", m.filter.Status)
	}
	if label := m.scopeLabel(); label != "全部会话" {
		t.Fatalf("scope label = %q, want 全部会话", label)
	}
}

// A selection made in the tree pins the list. The pin has to win over following,
// and be keyed by session: here it selects the *other* session's same-numbered
// pane, so a bare key would show this session's message instead.
func TestPinnedSelectionBeatsFollowingAndKeysBySession(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.snapshot = twoSessions(t)
	m.scopeFollow = true
	m.filter.Status = filterQueued
	put(t, m, "主区消息", at("default"))
	put(t, m, "笔记消息", at("note"))
	m.reload(t)

	m.openWorkspaceFilter()
	putCursorOn(t, m, "note/w1:p1")
	pressSpace(t, m)
	pressEnter(t, m)

	if len(m.filter.Panes) != 1 || !m.filter.Panes["note/w1:p1"] {
		t.Fatalf("pinned set = %v, want exactly note/w1:p1", m.filter.Panes)
	}
	got := m.visible()
	if len(got) != 1 || m.snapshot.SessionOf(got[0].Target) != "note" {
		t.Fatalf("visible after pin = %d from session %v, want the pinned session only",
			len(got), sessionsOf(got))
	}
	// Pinning is a location action; the lifecycle tab is a different dimension and
	// is left exactly where the user had it.
	if m.filter.Status != filterQueued {
		t.Fatalf("status tab after pinning = %v, want it left alone", m.filter.Status)
	}
}

// Both sessions expose a workspace whose bare id is `w1`, so expand state has to
// be keyed by session or closing one would hide the other's subtree.
func TestTreeExpandStateIsPerSession(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.snapshot = twoSessions(t)
	m.scopeFollow = false
	m.mode = modeTargetPicker
	m.picker = picker{expanded: map[string]bool{}, treeOpen: true}
	m.picker.treeItems = m.flattenTree()

	putCursorOn(t, m, "note/w1")
	pressEnter(t, m)

	seen := map[string]bool{}
	for _, item := range m.picker.treeItems {
		seen[item.key] = true
	}
	if seen["note/w1:t1"] {
		t.Fatalf("collapsed note workspace still shows its tab: %v", m.picker.treeItems)
	}
	if !seen["default/w1"] || !seen["default/w1:t1"] || !seen["default/w1:p1"] {
		t.Fatalf("primary subtree collapsed too: %v", m.picker.treeItems)
	}
}

// A stopped session has no socket to write to, so its rows cannot become a send
// target; it stays listed because history aimed at it still has to be reachable.
func TestStoppedSessionCannotBecomeTarget(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	snap := twoSessions(t)
	for i := range snap.Sessions {
		if snap.Sessions[i].Name == "note" {
			snap.Sessions[i].Running = false
		}
	}
	m.snapshot = snap
	m.scopeFollow = false
	put(t, m, "主区消息", at("default"))
	m.reload(t)

	// The picker opens on the message's own session, so reaching a stopped
	// session's pane means expanding it the way a user would.
	m.openTargetPicker(context.Background(), m.selected())
	for _, group := range []string{"note/", "note/w1", "note/w1:t1"} {
		putCursorOn(t, m, group)
		pressEnter(t, m)
	}
	putCursorOn(t, m, "note/w1:p1")
	pressEnter(t, m)
	if m.mode != modeTargetPicker {
		t.Fatalf("a stopped session's pane was accepted: mode=%v", m.mode)
	}
	if m.notice == "" {
		t.Fatal("refusing the row said nothing")
	}

	// The same row is still a legitimate filter, so the tree lists it there too.
	m.openWorkspaceFilter()
	putCursorOn(t, m, "note/w1:p1")
	pressSpace(t, m)
	if !m.picker.selectedPanes["note/w1:p1"] {
		t.Fatal("a stopped session's pane cannot be used to filter")
	}
}

// Every full path names its session, so two servers that each own a workspace
// labelled 笔记区 and a pane titled 笔记面板 cannot be confused.
func TestTargetPathCarriesTheSession(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.snapshot = twoSessions(t)

	if got := m.targetLabel(at("note")); got != "note / 笔记区 / 标签 t1 / 笔记面板" {
		t.Fatalf("foreign target = %q", got)
	}
	// The primary is no longer special: the full path names it too, which is what
	// makes a same-named foreign pane unambiguous.
	if got := m.targetLabel(at("default")); got != "default / 主区 / 标签 t1 / 本机面板" {
		t.Fatalf("primary target = %q", got)
	}
	// The narrow form drops the session, spending its width on the differing levels.
	if got := m.targetDisplay(&model.Message{Target: at("note")}); got != "笔记区 / 标签 t1 / 笔记面板" {
		t.Fatalf("narrow target = %q, want no session", got)
	}
}

// An ambient session has no name, so the path calls it "herdr" -- the same word
// the tree uses for the top row -- rather than leaving the session level blank.
func TestAmbientSessionPathUsesHerdrName(t *testing.T) {
	m, _, fake := newTestModel(t, 120, 40)
	snap, err := fake.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.snapshot = snap
	if got := m.targetLabel(model.Target{Workspace: "w1", Pane: "w1:p1"}); got != "herdr / 研究 / 标签 t1 / qodercli-1" {
		t.Fatalf("ambient-session label = %q", got)
	}
}

// The launch environment decides the default scope: inside herdr the list starts
// narrowed to the current pane, outside it does not.
func TestLaunchEnvironmentArmsFollowing(t *testing.T) {
	newBare := func() *Model {
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
		return New(Options{
			Provider: &LocalProvider{Store: st, Engine: scheduler.NewEngine(st, herdr.NewFake(), led)},
			NoTimer:  true,
		})
	}
	t.Setenv("HERDR_ENV", "1")
	if !newBare().scopeFollow {
		t.Fatal("inside herdr the list did not start following the current pane")
	}
	t.Setenv("HERDR_ENV", "")
	if newBare().scopeFollow {
		t.Fatal("outside herdr the list started narrowed")
	}
}
