package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"herdr-outbox/internal/config"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/store"
)

func typed(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// sendKey drives one keystroke through the real handler and settles the reply,
// so tests see the same state the runtime would leave behind.
func sendKey(t *testing.T, m *Model, msg tea.KeyMsg, budget int) *Model {
	t.Helper()
	next, cmd := m.onKey(msg)
	return pump(t, next.(*Model), cmd, budget)
}

func newDraft(t *testing.T, st *store.Store, content string) *model.Message {
	t.Helper()
	created, err := st.Create(&model.Message{
		Content: content,
		Target:  model.Target{Workspace: "w1", Pane: "w1:p1", PaneLabel: "qodercli-1", Agent: "qodercli"},
		Trigger: model.Trigger{Kind: model.TriggerManual},
		Status:  model.StatusDraft,
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// TestBuiltinEditorIsTheDefault guards the config contract: with no config file
// the TUI edits in place rather than handing off to $EDITOR.
func TestBuiltinEditorIsTheDefault(t *testing.T) {
	m, _, _ := newTestModel(t, 100, 30)
	if m.editorMode != config.EditorBuiltin {
		t.Fatalf("editor mode = %q, want %q", m.editorMode, config.EditorBuiltin)
	}
}

func TestBuiltinEditorSavesThroughProvider(t *testing.T) {
	m, st, _ := newTestModel(t, 100, 30)
	created := newDraft(t, st, "原始内容")
	m.reload(t)

	m = sendKey(t, m, typed("e"), 8)
	if m.mode != modeEdit {
		t.Fatalf("mode after e = %v, want modeEdit", m.mode)
	}

	m = sendKey(t, m, typed("，再补一句"), 4)
	if !strings.Contains(m.textarea.Value(), "再补一句") {
		t.Fatalf("typed CJK text missing from buffer: %q", m.textarea.Value())
	}

	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS}, 8)
	if m.mode != modeBrowse {
		t.Fatalf("mode after save = %v, want modeBrowse", m.mode)
	}

	stored, err := st.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.Content, "再补一句") {
		t.Fatalf("saved content missing typed text: %q", stored.Content)
	}
	if !strings.Contains(stored.Content, "原始内容") {
		t.Fatalf("saved content dropped the original body: %q", stored.Content)
	}
}

// The rows and the detail panel render from the cached message list, so a save has
// to push that cache through the provider again — writing the file alone leaves the
// preview showing the pre-edit body until something else happens to reload.
func TestBuiltinEditorRefreshesPreview(t *testing.T) {
	m, st, _ := newTestModel(t, 100, 30)
	created := newDraft(t, st, "第一段")
	m.reload(t)

	m = sendKey(t, m, typed("e"), 8)
	m = sendKey(t, m, typed("\n第二段"), 4)
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyCtrlS}, 12)

	if m.mode != modeBrowse {
		t.Fatalf("mode after save = %v, want modeBrowse", m.mode)
	}
	sel := m.byID(created.ID)
	if sel == nil {
		t.Fatal("message dropped from the cached list")
	}
	if !strings.Contains(sel.Content, "第二段") {
		t.Fatalf("cached row still holds the pre-save body: %q", sel.Content)
	}
	if !strings.Contains(m.View(), "第二段") {
		t.Fatalf("rendered frame missing the saved text:\n%s", m.View())
	}
}

func TestBuiltinEditorDiscardLeavesContentAlone(t *testing.T) {
	m, st, _ := newTestModel(t, 100, 30)
	created := newDraft(t, st, "keep me")
	m.reload(t)

	m = sendKey(t, m, typed("e"), 8)
	m = sendKey(t, m, typed(" garbage"), 4)

	// The first esc only arms the confirmation, because the buffer is dirty.
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyEsc}, 4)
	if !m.editConfirmDiscard {
		t.Fatal("first esc should arm the discard confirmation")
	}
	m = sendKey(t, m, tea.KeyMsg{Type: tea.KeyEsc}, 4)

	if m.mode != modeBrowse {
		t.Fatalf("mode after discard = %v, want modeBrowse", m.mode)
	}
	stored, err := st.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The store normalises a trailing newline, so compare the trimmed body.
	if got := strings.TrimSpace(stored.Content); got != "keep me" {
		t.Fatalf("discarded edit leaked to store: %q", stored.Content)
	}
}

// TestEditorModeResolution covers the switch the config file controls.
func TestEditorModeResolution(t *testing.T) {
	if got := (config.Config{}).ResolveEditor(); got != config.EditorBuiltin {
		t.Fatalf("empty config resolved to %q, want builtin", got)
	}
	if got := (config.Config{Editor: config.EditorExternal}).ResolveEditor(); got != config.EditorExternal {
		t.Fatalf("external config resolved to %q", got)
	}
	if got := (config.Config{Editor: "bogus"}).ResolveEditor(); got != config.EditorBuiltin {
		t.Fatalf("unknown value resolved to %q, want builtin fallback", got)
	}
}
