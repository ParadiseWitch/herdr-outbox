package ui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
)

// A popup has no pane identity of its own: herdr clears HERDR_PANE_ID for popup
// commands, and a popup is not a pane of the layout, so the snapshot's focused
// pane still names the pane the user was in before the popup opened. A new
// message has to default to it.
func TestNewMessageDefaultsToFocusedPaneInPopup(t *testing.T) {
	m, st, fake := newTestModel(t, 100, 30)
	// newTestModel pins HERDR_PANE_ID off, which is the popup case.
	if m.currentPane != "" {
		t.Fatalf("currentPane = %q, want empty for the popup case", m.currentPane)
	}
	fake.AddPane(herdr.Pane{ID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", Agent: "claude", AgentStatus: herdr.StatusIdle})
	fake.Panes[1].Focused = true
	snap, err := fake.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.snapshot = snap

	if got := m.defaultTarget(); got.Pane != "w1:p2" || got.Workspace != "w1" || got.PaneLabel == "" {
		t.Fatalf("defaultTarget = %+v, want the focused pane w1:p2", got)
	}

	_, cmd := m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	pump(t, m, cmd, 8)

	msgs, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	var created *model.Message
	for _, msg := range msgs {
		if msg.Status == model.StatusDraft {
			created = msg
		}
	}
	if created == nil {
		t.Fatal("n did not create a draft")
	}
	if created.Target.Pane != "w1:p2" || created.Target.Workspace != "w1" {
		t.Fatalf("new draft target = %+v, want the focused pane", created.Target)
	}
}

// Launched inside a pane, the default is that pane even when a different pane is
// focused: the user is here, which beats guessing from the last click.
func TestNewMessageDefaultsToOwnPaneInsidePane(t *testing.T) {
	m, _, fake := newTestModel(t, 100, 30)
	fake.AddPane(herdr.Pane{ID: "w1:p2", WorkspaceID: "w1", TabID: "w1:t1", Agent: "claude", AgentStatus: herdr.StatusIdle})
	fake.Panes[1].Focused = true
	snap, err := fake.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.snapshot = snap
	m.currentPane = "w1:p1"

	if got := m.defaultTarget(); got.Pane != "w1:p1" {
		t.Fatalf("defaultTarget = %+v, want this TUI's own pane", got)
	}
}

// A stale HERDR_PANE_ID for a pane herdr no longer reports falls back to the
// focused pane rather than targeting a vanished pane.
func TestNewMessageFallsBackWhenOwnPaneIsGone(t *testing.T) {
	m, _, fake := newTestModel(t, 100, 30)
	fake.RemovePane("w1:p1")
	fake.AddPane(herdr.Pane{ID: "w1:p9", WorkspaceID: "w1", TabID: "w1:t1", Agent: "claude", AgentStatus: herdr.StatusIdle, Focused: true})
	snap, err := fake.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.snapshot = snap
	m.currentPane = "w1:p1"

	if got := m.defaultTarget(); got.Pane != "w1:p9" {
		t.Fatalf("defaultTarget = %+v, want the focused pane after the own pane closed", got)
	}
}

// With herdr unreachable there is no focused pane to name, so a popup leaves the
// target empty instead of guessing. Inside a pane the launch id still names it.
func TestNewMessageDefaultTargetWithoutSnapshot(t *testing.T) {
	m, _, _ := newTestModel(t, 100, 30)
	m.snapshot = nil
	if got := m.defaultTarget(); !got.Empty() {
		t.Fatalf("defaultTarget with no snapshot = %+v, want empty", got)
	}
	m.currentPane = "w1:p1"
	if got := m.defaultTarget(); got.Pane != "w1:p1" {
		t.Fatalf("defaultTarget inside a pane with no snapshot = %+v", got)
	}
}
