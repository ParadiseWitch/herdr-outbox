package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"herdr-outbox/internal/model"
)

func TestQueueRank(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.scopeFollow = false
	m.filter.Status = filterAll
	a := putQueued(t, m, "a", 1)
	b := putQueued(t, m, "b", 2)
	man := &model.Message{Content: "m", Target: model.Target{Workspace: "w1", Pane: "w1:p1"},
		Trigger: model.Trigger{Kind: model.TriggerManual}, Status: model.StatusPending}
	if _, err := m.provider.(*LocalProvider).Store.Create(man); err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	if r := m.queueRank(a); r != 1 {
		t.Fatalf("a rank = %d, want 1", r)
	}
	if r := m.queueRank(b); r != 2 {
		t.Fatalf("b rank = %d, want 2", r)
	}
	for _, msg := range m.msgs {
		if msg.ID == man.ID && m.queueRank(msg) != 0 {
			t.Fatal("manual message got a rank")
		}
	}
}

// A failed head still holds its slot, so it keeps rank 1 and the successor stays
// rank 2 — the badge mirrors the frozen chain, not just the waiting rows.
func TestQueueRankCountsFailedHead(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.scopeFollow = false
	m.filter.Status = filterAll
	head := putQueued(t, m, "head", 1)
	head.Status = model.StatusFailed
	if err := m.provider.(*LocalProvider).Store.Save(head); err != nil {
		t.Fatal(err)
	}
	succ := putQueued(t, m, "succ", 2)
	m.reload(t)
	for _, msg := range m.msgs {
		switch msg.ID {
		case head.ID:
			if r := m.queueRank(msg); r != 1 {
				t.Fatalf("failed head rank = %d, want 1", r)
			}
		case succ.ID:
			if r := m.queueRank(msg); r != 2 {
				t.Fatalf("successor rank = %d, want 2", r)
			}
		}
	}
}

// The circled badge replaces the leading gutter, so a ranked row must stay
// exactly width-1 wide like every other row -- otherwise the panel divider lands
// a column off. ① is an East-Asian ambiguous-width rune, so this pins that the
// renderer and the padding agree it occupies a single column.
func TestBadgeKeepsRowWidth(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.scopeFollow = false
	m.filter.Status = filterAll
	putQueued(t, m, "hello world this is a fairly long queued body", 1)
	putQueued(t, m, "second", 2)
	m.reload(t)
	var sawBadge bool
	for _, msg := range m.msgs {
		for _, r := range m.listRows(msg, 60, false) {
			if w := ansi.StringWidth(r); w != 59 {
				t.Fatalf("row display width = %d, want 59 (width-1): %q", w, r)
			}
		}
		if strings.ContainsAny(m.listRows(msg, 60, false)[0], "①②") {
			sawBadge = true
		}
	}
	if !sawBadge {
		t.Fatal("no circled badge rendered")
	}
}

func putQueued(t *testing.T, m *Model, body string, pos int) *model.Message {
	t.Helper()
	msg := &model.Message{Content: body,
		Target:   model.Target{Workspace: "w1", Pane: "w1:p1"},
		Trigger:  model.Trigger{Kind: model.TriggerAfterCompletion},
		Status:   model.StatusWaiting,
		QueuePos: pos}
	created, err := m.provider.(*LocalProvider).Store.Create(msg)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestBracketReordersQueue(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.scopeFollow = false
	m.filter.Status = filterAll
	a := putQueued(t, m, "a", 1)
	b := putQueued(t, m, "b", 2)
	m.reload(t)

	// Cursor on b (pos 2), press [ to move it up past a.
	m.cursor = indexOfVisible(m, b.ID)
	pressRune(t, m, '[')
	m.reload(t)
	if f := findMsg(m, a.ID); f.QueuePos != 2 {
		t.Fatalf("a pos = %d, want 2 after moving b up", f.QueuePos)
	}
	if f := findMsg(m, b.ID); f.QueuePos != 1 {
		t.Fatalf("b pos = %d, want 1", f.QueuePos)
	}

	// ] moves it back down; the head cannot be pushed above slot 1.
	m.cursor = indexOfVisible(m, b.ID)
	pressRune(t, m, ']')
	m.reload(t)
	if f := findMsg(m, b.ID); f.QueuePos != 2 {
		t.Fatalf("b pos = %d, want 2 after moving b down", f.QueuePos)
	}
	m.cursor = indexOfVisible(m, b.ID)
	pressRune(t, m, ']') // already last: no-op
	m.reload(t)
	if f := findMsg(m, b.ID); f.QueuePos != 2 {
		t.Fatalf("b moved past the end: pos = %d, want 2", f.QueuePos)
	}
}

// A non-queued message must be untouched by the reorder keys.
func TestBracketNoOpOnUnqueued(t *testing.T) {
	m, _, _ := newTestModel(t, 120, 40)
	m.scopeFollow = false
	m.filter.Status = filterAll
	man := &model.Message{Content: "m", Target: model.Target{Workspace: "w1", Pane: "w1:p1"},
		Trigger: model.Trigger{Kind: model.TriggerManual}, Status: model.StatusPending}
	created, err := m.provider.(*LocalProvider).Store.Create(man)
	if err != nil {
		t.Fatal(err)
	}
	m.reload(t)
	m.cursor = indexOfVisible(m, created.ID)
	pressRune(t, m, '[')
	m.reload(t)
	if f := findMsg(m, created.ID); f.QueuePos != 0 {
		t.Fatalf("manual message got a slot: %d", f.QueuePos)
	}
}

func indexOfVisible(m *Model, id string) int {
	for i, msg := range m.visible() {
		if msg.ID == id {
			return i
		}
	}
	return 0
}

func findMsg(m *Model, id string) *model.Message {
	for _, msg := range m.msgs {
		if msg.ID == id {
			return msg
		}
	}
	return nil
}

// A message created from the 队列中 tab with no default pane (the popup case)
// must not silently drop the queue trigger. It hands the user straight to the
// target picker and, once a pane is chosen, adopts after_completion at the end of
// that pane's queue.
func TestNewFromQueuePromptsForTargetThenQueues(t *testing.T) {
	m, st, _ := newTestModel(t, 100, 40)
	m.scopeFollow = false
	// Two existing queued messages on w1:p1 so "end of queue" is observable.
	putQueued(t, m, "已有1", 1)
	putQueued(t, m, "已有2", 2)

	// Popup: no currentPane, so defaultTarget is empty.
	m.filter.Status = filterQueued
	m.reload(t)
	m = sendKey(t, m, typed("n"), 8)
	m = sendKey(t, m, typed("新消息"), 4)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS}, 12)

	// Saving a queue message with no pane opens the target picker instead of
	// falling back to a manual draft.
	if m.mode != modeTargetPicker {
		t.Fatalf("mode after save = %v, want modeTargetPicker", m.mode)
	}
	if m.picker.afterTargetTrigger != model.TriggerAfterCompletion {
		t.Fatalf("pending trigger = %q, want after_completion", string(m.picker.afterTargetTrigger))
	}

	// Pick w1:p1 (applyTarget is the seam the picker's enter key calls); the
	// message should land at the end of that pane's queue.
	next, cmd := m.applyTarget(model.Target{Workspace: "w1", Pane: "w1:p1", Tab: "w1:t1"})
	m = pump(t, next.(*Model), cmd, 16)
	m.reload(t)

	msgs, _ := st.List()
	var created *model.Message
	for _, msg := range msgs {
		if strings.HasPrefix(msg.Content, "新消息") {
			created = msg
		}
	}
	if created == nil {
		t.Fatal("new message not found")
	}
	if created.Trigger.Kind != model.TriggerAfterCompletion {
		t.Fatalf("trigger = %q, want after_completion", string(created.Trigger.Kind))
	}
	if created.Status != model.StatusWaiting {
		t.Fatalf("status = %v, want waiting", created.Status)
	}
	if created.QueuePos != 3 {
		t.Fatalf("queue pos = %d, want 3 (end of w1:p1)", created.QueuePos)
	}
}

// With a default pane present (running inside a pane), the same flow must NOT
// interrupt with a picker -- it queues directly at the end.
func TestNewFromQueueWithPaneQueuesDirectly(t *testing.T) {
	m, st, _ := newTestModel(t, 100, 40)
	m.scopeFollow = false
	m.currentPane = "w1:p1"
	putQueued(t, m, "已有1", 1)

	m.filter.Status = filterQueued
	m.reload(t)
	m = sendKey(t, m, typed("n"), 8)
	m = sendKey(t, m, typed("直排"), 4)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS}, 12)

	if m.mode != modeBrowse {
		t.Fatalf("mode after save = %v, want modeBrowse (no picker)", m.mode)
	}
	msgs, _ := st.List()
	for _, msg := range msgs {
		if strings.HasPrefix(msg.Content, "直排") {
			if msg.Trigger.Kind != model.TriggerAfterCompletion || msg.QueuePos != 2 {
				t.Fatalf("queued directly: kind=%q pos=%d, want after_completion pos=2",
					string(msg.Trigger.Kind), msg.QueuePos)
			}
			return
		}
	}
	t.Fatal("new message not found")
}
