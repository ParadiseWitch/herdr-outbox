package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/model"
)

// pump drives the model the way the bubbletea runtime does: a returned command is
// executed, a batch is expanded into its members, and each message goes back
// through Update. The budget stops a test from chasing the tick loop forever.
func pump(t *testing.T, m *Model, cmd tea.Cmd, budget int) *Model {
	t.Helper()
	for cmd != nil && budget > 0 {
		budget--
		msg := cmd()
		batch, ok := msg.(tea.BatchMsg)
		if !ok {
			batch = tea.BatchMsg{func() tea.Msg { return msg }}
		}
		cmd = nil
		for _, sub := range batch {
			if sub == nil {
				continue
			}
			next, more := m.Update(sub())
			m = next.(*Model)
			if more != nil && budget > 0 {
				budget--
				cmd = more
			}
		}
	}
	return m
}

// The store is written by the scheduler, so the list only catches up when the
// send's own message triggers a reload.
func TestSendRefreshesTheList(t *testing.T) {
	m, st, fake := newTestModel(t, 100, 30)
	if _, err := st.Create(&model.Message{
		Content: "echo hi",
		Target:  model.Target{Workspace: "w1", Pane: "w1:p1", PaneLabel: "qodercli-1", Agent: "qodercli"},
		Trigger: model.Trigger{Kind: model.TriggerManual},
		Status:  model.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)

	_, cmd := m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = pump(t, m, cmd, 8)

	if len(fake.Sends()) != 1 {
		t.Fatalf("herdr received %d sends, want 1", len(fake.Sends()))
	}
	if len(m.msgs) != 1 || m.msgs[0].Status != model.StatusSent {
		t.Fatalf("list still shows %v", m.msgs)
	}
	if !strings.Contains(m.notice, "sent") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestSendWithoutTargetIsRefused(t *testing.T) {
	m, st, fake := newTestModel(t, 100, 30)
	if _, err := st.Create(&model.Message{
		Content: "orphan", Trigger: model.Trigger{Kind: model.TriggerManual}, Status: model.StatusDraft,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	_, cmd := m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = pump(t, m, cmd, 8)

	if len(fake.Sends()) != 0 {
		t.Fatal("an untargeted message was sent")
	}
	if !strings.Contains(m.notice, "按 p") {
		t.Fatalf("notice = %q", m.notice)
	}
}

// The bar and the target picker show live herdr state, so a tick has to read
// herdr even when no message is waiting on it.
func TestTickRefreshesSnapshot(t *testing.T) {
	m, st, _ := newTestModel(t, 100, 30)
	if _, err := st.Create(&model.Message{
		Content: "already delivered",
		Trigger: model.Trigger{Kind: model.TriggerManual},
		Status:  model.StatusSent,
	}); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	m.snapshot = nil

	_, cmd := m.Update(tickMsg(time.Now()))
	m = pump(t, m, cmd, 6)
	if m.snapshot == nil {
		t.Fatal("no snapshot after a tick")
	}
	if m.snapErr != nil {
		t.Fatalf("snapErr = %v", m.snapErr)
	}
}
