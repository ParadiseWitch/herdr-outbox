package ui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
)

// threeLevelModel is a snapshot with two workspaces, two tabs under the first and
// one labelled tab under the second: the shape the tree has to render.
func threeLevelModel(t *testing.T) *Model {
	t.Helper()
	m, st, fake := newTestModel(t, 120, 40)
	fake.Workspaces = []herdr.Workspace{
		{ID: "w1", Label: "研究"},
		{ID: "w2", Label: "app"},
	}
	fake.Tabs = []herdr.Tab{
		{ID: "w1:t1", WorkspaceID: "w1", Label: "代码"},
		{ID: "w1:t2", WorkspaceID: "w1"},
		{ID: "w2:t1", WorkspaceID: "w2", Label: "前端"},
	}
	fake.Panes = []herdr.Pane{
		{ID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", Label: "p1", Agent: "qodercli", AgentStatus: herdr.StatusWorking},
		{ID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t2", Label: "p2", AgentStatus: herdr.StatusUnknown},
		{ID: "w2:p1", WorkspaceID: "w2", TabID: "w2:t1", Label: "p1", Agent: "claude", AgentStatus: herdr.StatusIdle},
	}
	snap, err := fake.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.snapshot = snap
	if _, err := st.Create(&model.Message{
		Content: "tree",
		Target:  model.Target{Workspace: "w1", Pane: "w1:p1"},
		Trigger: model.Trigger{Kind: model.TriggerManual},
		Status:  model.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	return m
}

// treeRowsOf renders the flattened tree as "depth:label" so a structure
// assertion reads like the panel itself.
func treeRowsOf(items []treeItem) string {
	rows := make([]string, 0, len(items))
	for _, it := range items {
		rows = append(rows, string(rune('0'+it.depth))+":"+it.label)
	}
	return strings.Join(rows, " ")
}

const targetTreeRows = "0:herdr 1:研究 2:代码 3:p1 2:标签 t2 1:app"

func press(t *testing.T, m *Model, msg tea.KeyMsg) {
	t.Helper()
	if _, err := m.Update(msg); err != nil {
		t.Fatal(err)
	}
}

func pressRune(t *testing.T, m *Model, r rune) {
	t.Helper()
	press(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
}

func pressEnter(t *testing.T, m *Model) {
	t.Helper()
	press(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

func pressSpace(t *testing.T, m *Model) {
	t.Helper()
	press(t, m, tea.KeyMsg{Type: tea.KeySpace})
}

func putCursorOn(t *testing.T, m *Model, key string) {
	t.Helper()
	for i, item := range m.picker.treeItems {
		if item.key == key {
			m.picker.cursor = i
			return
		}
	}
	t.Fatalf("row %q missing from %q", key, treeRowsOf(m.picker.treeItems))
}

func TestTargetTreeShowsWorkspaceTabPane(t *testing.T) {
	m := threeLevelModel(t)
	m.openTargetPicker(context.Background(), m.selected())

	// The message's own session, workspace and tab start open, so its pane is on
	// screen; the sibling tab is listed but collapsed, other workspaces stay shut.
	if got := treeRowsOf(m.picker.treeItems); got != targetTreeRows {
		t.Fatalf("tree = %q", got)
	}
	leaf := m.picker.treeItems[m.picker.cursor]
	if leaf.key != "w1:p1" || leaf.depth != 3 || leaf.target.Pane != "w1:p1" ||
		leaf.tab != "w1:t1" || leaf.workspace != "w1" {
		t.Fatalf("cursor row = %+v", leaf)
	}
}

func TestTreeGroupToggleOpensTabsThenPanes(t *testing.T) {
	m := threeLevelModel(t)
	m.openTargetPicker(context.Background(), m.selected())

	// Enter on a group row opens or closes it instead of selecting it: a tab is
	// never a send target.
	putCursorOn(t, m, "w1:t1")
	pressEnter(t, m)
	if m.mode != modeTargetPicker {
		t.Fatalf("group enter left the picker: mode=%v", m.mode)
	}
	if got := treeRowsOf(m.picker.treeItems); got != "0:herdr 1:研究 2:代码 2:标签 t2 1:app" {
		t.Fatalf("collapsed tab = %q", got)
	}
	// Closing the workspace drops every row under it, leaving its own row.
	putCursorOn(t, m, "w1")
	pressEnter(t, m)
	if got := treeRowsOf(m.picker.treeItems); got != "0:herdr 1:研究 1:app" {
		t.Fatalf("collapsed workspace = %q", got)
	}
	// Reopening the workspace brings the tab rows back, and the tab the user
	// closed by hand stays closed.
	putCursorOn(t, m, "w1")
	pressEnter(t, m)
	if got := treeRowsOf(m.picker.treeItems); got != "0:herdr 1:研究 2:代码 2:标签 t2 1:app" {
		t.Fatalf("reopened workspace = %q", got)
	}
	// Reopening the tab shows its panes again.
	putCursorOn(t, m, "w1:t1")
	pressEnter(t, m)
	if got := treeRowsOf(m.picker.treeItems); got != targetTreeRows {
		t.Fatalf("reopened tab = %q", got)
	}
}

// The session row is the outermost group: closing it hides the whole topology of
// that session, which is how a stopped session gets out of the way.
func TestTreeSessionRowCollapsesWholeSession(t *testing.T) {
	m := threeLevelModel(t)
	m.openTargetPicker(context.Background(), m.selected())
	putCursorOn(t, m, "/")
	pressEnter(t, m)
	if got := treeRowsOf(m.picker.treeItems); got != "0:herdr" {
		t.Fatalf("collapsed session = %q", got)
	}
	pressEnter(t, m)
	if got := treeRowsOf(m.picker.treeItems); got != targetTreeRows {
		t.Fatalf("reopened session = %q", got)
	}
}

// h/l move through the tree by opening and closing groups, exactly like enter.
func TestTreeArrowsToggleGroups(t *testing.T) {
	m := threeLevelModel(t)
	m.openTargetPicker(context.Background(), m.selected())
	putCursorOn(t, m, "w1")
	pressRune(t, m, 'l')
	if got := treeRowsOf(m.picker.treeItems); got != "0:herdr 1:研究 1:app" {
		t.Fatalf("l did not collapse the workspace: %q", got)
	}
	pressRune(t, m, 'l')
	if got := treeRowsOf(m.picker.treeItems); got != targetTreeRows {
		t.Fatalf("l did not reopen the workspace: %q", got)
	}
	// A pane row has nothing to toggle, so the tree is left alone.
	putCursorOn(t, m, "w1:p1")
	pressRune(t, m, 'h')
	if got := treeRowsOf(m.picker.treeItems); got != targetTreeRows {
		t.Fatalf("h on a pane row changed the tree: %q", got)
	}
}

func TestFilterTreeOpensEveryLevel(t *testing.T) {
	m := threeLevelModel(t)
	m.openWorkspaceFilter()
	// w1:t2 has no label from `tab list`, so the row falls back to the id suffix.
	if got := treeRowsOf(m.picker.treeItems); got !=
		"0:herdr 1:研究 2:代码 3:p1 2:标签 t2 3:p2 1:app 2:前端 3:p1" {
		t.Fatalf("filter tree = %q", got)
	}
	// The quiet column on a pane row is the status plus the stable ordinal. The
	// row label already shows the pane title, so the hint must not repeat it.
	var hints []string
	for _, item := range m.picker.treeItems {
		if item.depth == 3 {
			hints = append(hints, item.hint)
		}
	}
	if strings.Join(hints, " ") != "working │ qodercli-1 shell │ w1:p2 idle │ claude-1" {
		t.Fatalf("pane hints = %v", hints)
	}
}

func TestFilterSpaceSelectsWholeTabOrWorkspace(t *testing.T) {
	m := threeLevelModel(t)
	m.openWorkspaceFilter()

	// A tab row selects only the panes under it.
	putCursorOn(t, m, "w1:t1")
	pressSpace(t, m)
	if len(m.picker.selectedPanes) != 1 || !m.picker.selectedPanes["w1:p1"] {
		t.Fatalf("tab selection = %v", m.picker.selectedPanes)
	}

	// A workspace row fills every pane under it, then clears them all.
	putCursorOn(t, m, "w1")
	pressSpace(t, m)
	if len(m.picker.selectedPanes) != 2 {
		t.Fatalf("workspace selection = %v", m.picker.selectedPanes)
	}
	pressSpace(t, m)
	if len(m.picker.selectedPanes) != 0 {
		t.Fatalf("workspace deselect = %v", m.picker.selectedPanes)
	}
	pressSpace(t, m)

	pressEnter(t, m)
	if m.mode != modeBrowse {
		t.Fatalf("mode after enter = %v", m.mode)
	}
	if len(m.filter.Panes) != 2 || !m.filter.Panes["w1:p1"] || !m.filter.Panes["w1:p2"] {
		t.Fatalf("applied filter = %v", m.filter.Panes)
	}
}

func TestTreePreviewLeadsWithPaneName(t *testing.T) {
	m := threeLevelModel(t)
	m.openWorkspaceFilter()

	// A pane row previews its name first, then tab, then id: the name is what a
	// user scans for, the address is confirmation.
	for i, item := range m.picker.treeItems {
		if item.depth == 3 && item.workspace == "w2" {
			m.picker.cursor = i
		}
	}
	lines := strings.Split(ansi.Strip(m.treePreviewPanel(60, 24, true)), "\n")
	name, tab, id := -1, -1, -1
	for i, l := range lines {
		s := strings.TrimSpace(l)
		switch {
		case name < 0 && s == "p1":
			name = i
		case tab < 0 && s == "标签: 前端":
			tab = i
		case id < 0 && s == "w2:p1":
			id = i
		}
	}
	if name < 0 || tab < 0 || id < 0 || !(name < tab && tab < id) {
		t.Fatalf("preview order name=%d tab=%d id=%d:\n%s", name, tab, id, strings.Join(lines, "\n"))
	}

	// A group row previews its contents instead of refusing to show anything.
	putCursorOn(t, m, "w1")
	group := ansi.Strip(m.treePreviewPanel(60, 24, true))
	if !strings.Contains(group, "研究") || !strings.Contains(group, "个面板") {
		t.Fatalf("group preview = %q", group)
	}
}
