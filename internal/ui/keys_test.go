package ui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
)

// The clear/clone keys swapped: `c` now clears the filter (was clone) and `y`
// clones (was unbound in browse mode). Pinning both directions catches a binding
// that silently reverts or lands on the wrong handler.
func TestClearKeyIsCAndCloneKeyIsY(t *testing.T) {
	m, st, fake := newTestModel(t, 120, 40)
	fake.AddPane(herdr.Pane{ID: "w2:p9", WorkspaceID: "w2", AgentStatus: herdr.StatusIdle})
	fake.Panes[0].Focused = true // w1:p1, the pane the scope follows
	snap, err := fake.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.snapshot = snap
	m.scopeFollow = true

	for _, pane := range []string{"w1:p1", "w2:p9"} {
		if _, err := st.Create(&model.Message{
			Content: "x", Target: model.Target{Workspace: "w1", Pane: pane},
			Trigger: model.Trigger{Kind: model.TriggerManual}, Status: model.StatusPending,
		}); err != nil {
			t.Fatal(err)
		}
	}
	m.reload(t)
	if got := len(m.visible()); got != 1 {
		t.Fatalf("following scope shows %d, want the focused pane only", got)
	}

	// c clears the scope and, unlike its old binding, does not clone.
	pressRune(t, m, 'c')
	if m.scopeFollow {
		t.Fatal("c did not clear the scope")
	}
	if got := len(m.visible()); got != 2 {
		t.Fatalf("after c, list shows %d, want both panes", got)
	}
	if len(m.msgs) != 2 {
		t.Fatalf("c cloned: %d messages, want the original 2", len(m.msgs))
	}

	// y clones the selected message into a draft. Update is called directly
	// because clone returns a reload command, which `press` would misread as an
	// error; the clone itself is already persisted by the time it returns.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")}); cmd == nil {
		t.Fatal("y returned no reload command; the clone did not run")
	}
	m.reload(t)
	var drafts int
	for _, msg := range m.msgs {
		if msg.Status == model.StatusDraft {
			drafts++
		}
	}
	if drafts != 1 {
		t.Fatalf("y produced %d drafts, want one clone", drafts)
	}
}
