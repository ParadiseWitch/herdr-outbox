// Package store persists outbox messages as Markdown files with YAML frontmatter.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"herdr-outbox/internal/model"
)

const delimiter = "---"

var ErrNotFound = errors.New("message not found")

type Store struct {
	Dir string
	mu  sync.Mutex
}

func Open(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create outbox dir %s: %w", abs, err)
	}
	return &Store{Dir: abs}, nil
}

// DefaultDir resolves the outbox directory, honouring HERDR_OUTBOX_DIR.
func DefaultDir() (string, error) {
	if env := strings.TrimSpace(os.Getenv("HERDR_OUTBOX_DIR")); env != "" {
		return env, nil
	}
	base, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(base) == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("resolve config dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "herdr-outbox"), nil
}

func (s *Store) Path(id string) string {
	return filepath.Join(s.Dir, SafeName(id)+".md")
}

// SafeName keeps an id that came from a frontmatter field or a filename from
// escaping the outbox directory.
func SafeName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "unnamed"
	}
	repl := strings.NewReplacer("/", "-", "\\", "-", "..", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	id = repl.Replace(id)
	var sb strings.Builder
	for _, r := range id {
		if r == ' ' || r == '_' || r == '-' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	out := strings.Trim(sb.String(), ".-")
	if out == "" {
		return "unnamed"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

// NewID is time ordered so file names sort in creation order.
func NewID() string {
	buf := make([]byte, 2)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("%s-%s", time.Now().Format("20060102-150405"), hex.EncodeToString(buf))
}

func (s *Store) Create(initial *model.Message) (*model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := model.Now()
	if initial.ID == "" {
		initial.ID = NewID()
	}
	if initial.CreatedAt.IsZero() {
		initial.CreatedAt = now
	}
	initial.UpdatedAt = now
	if initial.Status == "" {
		initial.Status = model.StatusDraft
	}
	if initial.Title == "" {
		initial.Title = model.DeriveTitle(initial.Content)
	}
	if err := s.saveLocked(initial); err != nil {
		return nil, err
	}
	return initial, nil
}

func (s *Store) Get(id string) (*model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(id)
}

func (s *Store) getLocked(id string) (*model.Message, error) {
	raw, err := os.ReadFile(s.Path(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return nil, err
	}
	m, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path(id), err)
	}
	if m.ID == "" {
		m.ID = id
	}
	return m, nil
}

// List returns every message, preferring armed work and favourites.
func (s *Store) List() ([]*model.Message, error) {
	s.mu.Lock()
	entries, err := os.ReadDir(s.Dir)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var out []*model.Message
	var problems []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		m, err := s.Get(id)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 && len(problems) > 0 {
		return nil, fmt.Errorf("no readable messages: %s", strings.Join(problems, "; "))
	}
	Sort(out)
	return out, nil
}

func Sort(ms []*model.Message) {
	sort.SliceStable(ms, func(i, j int) bool {
		a, b := ms[i], ms[j]
		if a.Favorite != b.Favorite {
			return a.Favorite
		}
		if ra, rb := model.SortRank(a), model.SortRank(b); ra != rb {
			return ra < rb
		}
		if a.Trigger.Kind == model.TriggerScheduled && b.Trigger.Kind == model.TriggerScheduled &&
			a.Trigger.SendAt != nil && b.Trigger.SendAt != nil {
			return a.Trigger.SendAt.Before(*b.Trigger.SendAt)
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
}

func (s *Store) Save(m *model.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(m)
}

func (s *Store) saveLocked(m *model.Message) error {
	if m.ID == "" {
		return errors.New("message has no id")
	}
	m.Title = strings.TrimSpace(m.Title)
	raw, err := Render(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.Path(m.ID), raw)
}

// Touch persists an update with a fresh UpdatedAt, which also re-versions the
// message so a later automatic send is not mistaken for a duplicate.
func (s *Store) Touch(m *model.Message, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.UpdatedAt = at
	return s.saveLocked(m)
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.Path(id))
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return err
}

// ReloadFromDisk re-reads a message after $EDITOR has written it. User edits to
// frontmatter are authoritative, except that editing a sent message returns it to
// draft instead of silently re-arming an automatic send.
func (s *Store) ReloadFromDisk(id string, before *model.Message) (*model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	after, err := s.getLocked(id)
	if err != nil {
		return nil, err
	}
	if after.CreatedAt.IsZero() {
		after.CreatedAt = before.CreatedAt
	}
	if after.UpdatedAt.IsZero() {
		after.UpdatedAt = before.UpdatedAt
	}
	if before.Status == model.StatusSent && after.Content != before.Content {
		after.Status = model.StatusDraft
		after.SentAt = nil
	}
	return after, nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Render serialises a message back to Markdown with YAML frontmatter.
func Render(m *model.Message) ([]byte, error) {
	body := strings.ReplaceAll(m.Content, "\r\n", "\n")
	if !strings.HasSuffix(body, "\n") && body != "" {
		body += "\n"
	}
	meta, err := yaml.Marshal(m)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(meta), "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	var sb strings.Builder
	sb.WriteString(delimiter)
	sb.WriteString("\n")
	sb.WriteString(strings.Join(lines, "\n"))
	sb.WriteString("\n")
	sb.WriteString(delimiter)
	sb.WriteString("\n\n")
	sb.WriteString(body)
	return []byte(sb.String()), nil
}

// Parse reads a Markdown + frontmatter file. Missing frontmatter is tolerated so
// a hand-written note in the directory still loads.
func Parse(raw []byte) (*model.Message, error) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	var front, body string
	hasFrontmatter := false
	if strings.HasPrefix(text, delimiter+"\n") {
		rest := strings.TrimPrefix(text, delimiter+"\n")
		idx := findFrontmatterEnd(rest)
		if idx < 0 {
			return nil, errors.New("unterminated YAML frontmatter")
		}
		front = rest[:idx]
		body = strings.TrimLeft(rest[idx:], "\n")
		body = strings.TrimPrefix(body, delimiter+"\n")
		body = strings.TrimPrefix(body, delimiter)
		hasFrontmatter = true
	} else {
		body = text
	}
	m := &model.Message{}
	if strings.TrimSpace(front) != "" {
		dec := yaml.NewDecoder(strings.NewReader(front))
		dec.KnownFields(false)
		if err := dec.Decode(m); err != nil && err.Error() != "EOF" {
			return nil, fmt.Errorf("bad frontmatter: %w", err)
		}
	}
	m.Content = strings.TrimLeft(body, "\r\n")
	if m.Status == "" {
		m.Status = model.StatusDraft
	}
	if m.Trigger.Kind == "" {
		m.Trigger.Kind = model.TriggerManual
	}
	if !m.Trigger.Kind.Valid() {
		return nil, fmt.Errorf("unknown trigger %q", m.Trigger.Kind)
	}
	if !m.Status.Valid() {
		return nil, fmt.Errorf("unknown status %q", m.Status)
	}
	// Only derive the title from content for bare notes without frontmatter.
	// Files managed by the app always have frontmatter, so an empty title
	// there means the user explicitly cleared it.
	if !hasFrontmatter && m.Title == "" {
		m.Title = model.DeriveTitle(m.Content)
	}
	return m, nil
}

func findFrontmatterEnd(s string) int {
	pos := 0
	for {
		i := strings.Index(s[pos:], "\n"+delimiter)
		if i < 0 {
			return -1
		}
		candidate := pos + i + 1
		after := s[candidate+len(delimiter):]
		if strings.HasPrefix(after, "\n") || strings.HasPrefix(after, "\r\n") || after == "" {
			return candidate
		}
		pos = candidate
	}
}
