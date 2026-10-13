package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MultiBackend reads every running herdr session and routes a send to the session
// that owns the pane. It satisfies Backend, so the scheduler, the store and the
// HTTP layer keep their single-snapshot shape: one Snapshot carries the merged
// topology of all sessions, and every id inside it is stamped with its session
// because a workspace id like `w1` is a per-session counter that two sessions can
// hand out independently.
type MultiBackend struct {
	Bin         string
	ReadTimeout time.Duration
	SendTimeout time.Duration
	// Primary is the session unqualified ids belong to. Filled in on the first
	// snapshot when left empty, from the ambient socket or the default session.
	Primary string

	mu      sync.Mutex
	cli     map[string]*CLI
	rosterT time.Time
	roster  []Session
}

// sessionListLimit bounds how many running sessions one snapshot reads, so an
// unusual number of them cannot turn a tick into a process storm.
const sessionListLimit = 16

// snapshotConcurrency caps simultaneous subprocesses per snapshot.
const snapshotConcurrency = 4

func NewMultiBackend(bin, primary string) *MultiBackend {
	return &MultiBackend{
		Bin:         bin,
		ReadTimeout: 15 * time.Second,
		SendTimeout: 60 * time.Second,
		Primary:     strings.TrimSpace(primary),
		cli:         map[string]*CLI{},
	}
}

func (m *MultiBackend) Name() string { return "herdr-multi" }

// forSession returns the CLI talking to one session, creating it on demand. Each
// CLI passes --session explicitly, which herdr ranks above an inherited
// HERDR_SOCKET_PATH, so the socket of the popup that launched the outbox cannot
// hijack routing to another session.
func (m *MultiBackend) forSession(session string) *CLI {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.cli[session]; ok {
		return c
	}
	c := &CLI{Bin: m.Bin, Session: session, ReadTimeout: m.ReadTimeout, SendTimeout: m.SendTimeout}
	m.cli[session] = c
	return c
}

// Probe reports whether at least one session answers.
func (m *MultiBackend) Probe(ctx context.Context) error {
	_, err := m.Snapshot(ctx)
	return err
}

// listSessions reads the session roster. It is cached because the topology is
// re-read every tick while sessions are created far less often.
func (m *MultiBackend) listSessions(ctx context.Context) ([]Session, error) {
	m.mu.Lock()
	if m.roster != nil && time.Since(m.rosterT) < 30*time.Second {
		out := m.roster
		m.mu.Unlock()
		return out, nil
	}
	m.mu.Unlock()

	// `session list` is global: no --session, so herdr uses the ambient socket to
	// find the sessions directory.
	raw, err := m.forSession("").call(ctx, m.ReadTimeout, "session", "list", "--json")
	if err != nil {
		return nil, err
	}
	var payload struct {
		Sessions []Session `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	out := payload.Sessions
	m.mu.Lock()
	m.roster = out
	m.rosterT = time.Now()
	m.mu.Unlock()
	return out, nil
}

// InvalidateRoster forgets the cached roster.
func (m *MultiBackend) InvalidateRoster() {
	m.mu.Lock()
	m.roster = nil
	m.mu.Unlock()
}

// resolvePrimary names the session that unqualified ids belong to. Panes are
// stamped with it, so it has to be a real name: Target.Session is empty for every
// message written before multi-session support, and the resolver maps that empty
// value onto Primary. An empty Primary would match nothing.
//
// Without --session, herdr answers the ambient socket, so the session whose
// socket_path equals $HERDR_SOCKET_PATH is the primary; failing that, the session
// herdr marks default.
func resolvePrimary(explicit string, sessions []Session) string {
	if explicit != "" {
		return explicit
	}
	ambient := strings.TrimSpace(os.Getenv("HERDR_SOCKET_PATH"))
	if ambient != "" {
		want := filepath.Clean(ambient)
		for _, s := range sessions {
			if s.Socket != "" && filepath.Clean(s.Socket) == want {
				return s.Name
			}
		}
	}
	for _, s := range sessions {
		if s.Default {
			return s.Name
		}
	}
	if len(sessions) > 0 {
		return sessions[0].Name
	}
	return ""
}

// Snapshot merges every running session into one view. A session that cannot be
// read is reported in its Session.Err while the rest still answer: one dead
// session must not blind the whole outbox, and must not make an armed message
// aimed at it look failed. If nothing answers at all, herdr is unreachable.
func (m *MultiBackend) Snapshot(ctx context.Context) (*Snapshot, error) {
	sessions, err := m.listSessions(ctx)
	if err != nil {
		// No roster: fall back to the ambient session alone, so a herdr without
		// `session list --json` behaves exactly as it does today.
		return m.forSession(m.Primary).Snapshot(ctx)
	}

	primary := resolvePrimary(m.Primary, sessions)
	m.mu.Lock()
	m.Primary = primary
	m.mu.Unlock()

	targets := make([]Session, 0, len(sessions))
	sidebar := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		switch {
		case !s.Running:
			sidebar = append(sidebar, s)
		case len(targets) >= sessionListLimit:
			s.Err = fmt.Sprintf("未读取：running session 超过 %d 个上限", sessionListLimit)
			sidebar = append(sidebar, s)
		default:
			targets = append(targets, s)
		}
	}

	type result struct {
		snap *Snapshot
		err  string
	}
	results := make([]result, len(targets))
	sem := make(chan struct{}, snapshotConcurrency)
	var wg sync.WaitGroup
	for i, s := range targets {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			snap, serr := m.forSession(name).Snapshot(ctx)
			if serr != nil {
				results[i] = result{err: serr.Error()}
				return
			}
			results[i] = result{snap: snap}
		}(i, s.Name)
	}
	wg.Wait()

	merged := &Snapshot{
		Primary:   primary,
		Source:    m.Bin,
		FetchedAt: time.Now(),
	}
	roster := make([]Session, 0, len(targets)+len(sidebar))
	anyOk := false
	for i, s := range targets {
		r := results[i]
		if r.err != "" {
			s.Err = r.err
			roster = append(roster, s)
			continue
		}
		anyOk = true
		// Focus and the seq-read failure are per session and came from the same
		// read, so they are carried over rather than recomputed across sessions.
		if len(r.snap.Sessions) == 1 {
			s.FocusedPane = r.snap.Sessions[0].FocusedPane
		}
		if e := r.snap.SeqError(s.Name); e != "" {
			if merged.SeqUnavailable == nil {
				merged.SeqUnavailable = map[string]string{}
			}
			merged.SeqUnavailable[s.Name] = e
		}
		merged.Workspaces = append(merged.Workspaces, r.snap.Workspaces...)
		merged.Tabs = append(merged.Tabs, r.snap.Tabs...)
		merged.Panes = append(merged.Panes, r.snap.Panes...)
		roster = append(roster, s)
	}
	roster = append(roster, sidebar...)
	merged.Sessions = roster
	if !anyOk {
		return nil, fmt.Errorf("%w: 所有 running session 都读不到", ErrUnavailable)
	}
	return merged, nil
}

// route splits a possibly-qualified pane id into the CLI owning it and the bare
// id that CLI expects. An unqualified id belongs to the primary session: matching
// it against another session's identically counted pane would deliver the message
// into the wrong terminal.
func (m *MultiBackend) route(paneID string) (*CLI, string) {
	session, bare := SplitQualified(paneID)
	if session == "" {
		m.mu.Lock()
		session = m.Primary
		m.mu.Unlock()
	}
	return m.forSession(session), bare
}

func (m *MultiBackend) Send(ctx context.Context, paneID, text string) (SendResult, error) {
	cli, bare := m.route(paneID)
	return cli.Send(ctx, bare, text)
}

func (m *MultiBackend) Focus(ctx context.Context, paneID string) error {
	cli, bare := m.route(paneID)
	return cli.Focus(ctx, bare)
}

func (m *MultiBackend) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	cli, bare := m.route(paneID)
	return cli.ReadPane(ctx, bare, lines)
}

var _ Backend = (*MultiBackend)(nil)
