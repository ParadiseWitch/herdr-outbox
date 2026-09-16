package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"herdr-outbox/internal/model"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTripKeepsEveryField(t *testing.T) {
	s := openStore(t)
	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	m := &model.Message{
		Title:    "Refactor API",
		Target:   model.Target{Workspace: "wK", WorkspaceLabel: "herdr-outbox", Pane: "wK:p1", PaneLabel: "herdr-outbox / qodercli-1", Agent: "qodercli"},
		Trigger:  model.Trigger{Kind: model.TriggerScheduled, SendAt: &at, SettleSeconds: 4},
		Status:   model.StatusScheduled,
		Favorite: true,
		Content:  "# Refactor API\n\n请继续检查刚才修改的 API，\n并补充对应的单元测试。\n",
	}
	created, err := s.Create(m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != m.Content {
		t.Fatalf("content changed: %q", got.Content)
	}
	if got.Target.Pane != "wK:p1" || got.Target.Agent != "qodercli" {
		t.Fatalf("target lost: %+v", got.Target)
	}
	if got.Trigger.Kind != model.TriggerScheduled || got.Trigger.SendAt == nil || !got.Trigger.SendAt.Equal(at) {
		t.Fatalf("trigger lost: %+v", got.Trigger)
	}
	if got.Status != model.StatusScheduled || !got.Favorite || got.Trigger.SettleSeconds != 4 {
		t.Fatalf("fields lost: %+v", got)
	}
	raw, err := os.ReadFile(s.Path(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.HasPrefix(text, "---\nid: ") {
		t.Fatalf("frontmatter missing or misordered:\n%s", text)
	}
	if strings.Contains(text, "\r\n") {
		t.Fatal("CRLF leaked into a stored file")
	}
	if !strings.Contains(text, "请继续检查") {
		t.Fatal("body lost")
	}
}

func TestSaveIsAtomicAndKeepsSingleFile(t *testing.T) {
	s := openStore(t)
	m, err := s.Create(&model.Message{Content: "first\n"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m.Content = "revision\n"
		m.Attempts = i
		if err := s.Save(m); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp files left behind: %v", names)
	}
	got, err := s.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "revision\n" || got.Attempts != 4 {
		t.Fatalf("bad final state: %+v", got)
	}
}

func TestListSortsFavouritesThenArmed(t *testing.T) {
	s := openStore(t)
	base := time.Now()
	add := func(id string, status model.Status, fav bool, age time.Duration) {
		m := &model.Message{ID: id, Status: status, Favorite: fav, Content: id + "\n",
			CreatedAt: base.Add(-age), UpdatedAt: base.Add(-age)}
		if err := s.Save(m); err != nil {
			t.Fatal(err)
		}
	}
	add("sent-old", model.StatusSent, false, time.Hour)
	add("wait-1", model.StatusWaiting, false, 10*time.Minute)
	add("fav-sent", model.StatusSent, true, 2*time.Hour)
	add("draft-1", model.StatusDraft, false, 5*time.Minute)
	add("fail-1", model.StatusFailed, false, time.Minute)

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, m := range list {
		order = append(order, m.ID)
	}
	want := []string{"fav-sent", "wait-1", "fail-1", "draft-1", "sent-old"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestHandWrittenNoteWithoutFrontmatterLoads(t *testing.T) {
	s := openStore(t)
	path := filepath.Join(s.Dir, "note.md")
	if err := os.WriteFile(path, []byte("# Scratch note\n\nremember to check redis\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := s.Get("note")
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != model.StatusDraft {
		t.Fatalf("bare notes should load as draft, got %s", m.Status)
	}
	if m.Title != "Scratch note" {
		t.Fatalf("title = %q", m.Title)
	}
	if !strings.Contains(m.Content, "remember to check redis") {
		t.Fatalf("content = %q", m.Content)
	}
}

func TestCRLFAndBadFrontmatter(t *testing.T) {
	s := openStore(t)
	path := filepath.Join(s.Dir, "crlf.md")
	if err := os.WriteFile(path, []byte("---\r\nid: crlf\r\nstatus: pending\r\n---\r\n\r\nbody line\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := s.Get("crlf")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Content, "\r") || m.Status != model.StatusPending {
		t.Fatalf("bad parse: %+v", m)
	}

	bad := filepath.Join(s.Dir, "bad.md")
	if err := os.WriteFile(bad, []byte("---\nid: bad\nstatus: bogus\n---\n\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("bad"); err == nil {
		t.Fatal("unknown status accepted")
	}
	unterminated := filepath.Join(s.Dir, "unterm.md")
	if err := os.WriteFile(unterminated, []byte("---\nid: unterm\nno end marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("unterm"); err == nil {
		t.Fatal("unterminated frontmatter accepted")
	}
}

func TestFrontmatterLikeBodyTextIsNotSwallowed(t *testing.T) {
	s := openStore(t)
	body := "---\nthis is a markdown rule, not frontmatter\n---\n\nreal body\n"
	m, err := s.Create(&model.Message{Content: body})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != body {
		t.Fatalf("body was rewritten:\n%q", got.Content)
	}
}

func TestSafeNameBlocksPathEscape(t *testing.T) {
	s := openStore(t)
	for _, id := range []string{"../../evil", `..\..\evil`, "/abs/path", "a\x00b", "...", "....//"} {
		got := SafeName(id)
		if strings.Contains(got, "/") || strings.Contains(got, "\\") || strings.Contains(got, "..") {
			t.Fatalf("SafeName(%q) = %q still escapes", id, got)
		}
		path := s.Path(id)
		rel, err := filepath.Rel(s.Dir, filepath.Dir(path))
		if err != nil || rel != "." {
			t.Fatalf("SafeName(%q) resolved outside the store: %s", id, path)
		}
	}
	if SafeName("") != "unnamed" {
		t.Fatal("empty id should map to a placeholder")
	}
}

func TestDeleteAndNotFound(t *testing.T) {
	s := openStore(t)
	m, err := s.Create(&model.Message{Content: "bye\n"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(m.ID); !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found, got %v", err)
	}
	if err := s.Delete(m.ID); err == nil {
		t.Fatal("deleting twice should report not found")
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("want empty list, got %d", len(list))
	}
}

func TestReloadFromDiskReclassesEditedSentMessage(t *testing.T) {
	s := openStore(t)
	now := time.Now()
	before := &model.Message{ID: "x", Status: model.StatusSent, Content: "old\n",
		CreatedAt: now, UpdatedAt: now, SentAt: &now}
	if err := s.Save(before); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path("x"), []byte("---\nid: x\nstatus: sent\n---\n\nnew text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := s.ReloadFromDisk("x", before)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != model.StatusDraft {
		t.Fatalf("editing a sent message should return it to draft, got %s", after.Status)
	}
	if after.SentAt != nil {
		t.Fatal("stale sent_at kept")
	}
	if after.CreatedAt.IsZero() {
		t.Fatal("created_at lost")
	}
}

func TestDefaultDirHonoursEnv(t *testing.T) {
	t.Setenv("HERDR_OUTBOX_DIR", filepath.Join(t.TempDir(), "custom"))
	dir, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dir, "custom") {
		t.Fatalf("HERDR_OUTBOX_DIR ignored: %s", dir)
	}
	t.Setenv("HERDR_OUTBOX_DIR", "")
	fallback, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.ToSlash(fallback), "herdr-outbox") {
		t.Fatalf("unexpected fallback %s", fallback)
	}
}
