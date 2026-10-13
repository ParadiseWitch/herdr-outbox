// Package herdr adapts the herdr socket CLI for herdr-outbox.
//
// Everything the outbox needs from herdr comes through the Backend interface, so
// an alternative transport (raw socket, gRPC, a fixture) can be dropped in without
// touching the scheduler or the TUI.
package herdr

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"herdr-outbox/internal/model"
)

// Agent lifecycle states reported by herdr.
const (
	StatusIdle     = "idle"
	StatusWorking  = "working"
	StatusBlocked  = "blocked"
	StatusDone     = "done"
	StatusUnknown  = "unknown"
	StatusDetached = "detached"
)

// Settled means the agent is ready to take the next prompt.
func Settled(agentStatus string) bool {
	switch agentStatus {
	case StatusIdle, StatusDone:
		return true
	}
	return false
}

// StatusLabel renders a lifecycle state for the Chinese UI, keeping the raw word
// for anything herdr adds that this build does not know about.
func StatusLabel(s string) string {
	switch s {
	case StatusIdle:
		return "空闲"
	case StatusWorking:
		return "工作中"
	case StatusBlocked:
		return "等待审批"
	case StatusDone:
		return "已完成"
	case StatusUnknown:
		return "未识别"
	case StatusDetached:
		return "已脱离"
	case "":
		return "无代理"
	}
	return s
}

// Session is one herdr server: its own workspace/tab/pane id namespace. IDs are
// per-session counters, so `w1:p1` in one session is a different terminal than
// `w1:p1` in another; anything that crosses sessions must be qualified.
type Session struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Socket  string `json:"socket_path"`
	Default bool   `json:"default"`
	// FocusedPane is this session's own focused pane, bare id. Each running
	// session has one, so a merged snapshot cannot answer "the focused pane"
	// without naming the session.
	FocusedPane string `json:"focused_pane,omitempty"`
	// Err is why this session could not be read this round. An armed message
	// aimed at it must hold, not be marked failed or "target gone".
	Err string `json:"err,omitempty"`
}

// SplitQualified reads `session/id`; the session name is a directory name, so it
// never contains a slash and the pane id never does either: at most one.
func SplitQualified(id string) (session, bare string) {
	if i := strings.IndexByte(id, '/'); i >= 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

// Qualify builds the `session/id` form of an id.
func Qualify(session, id string) string {
	if session == "" {
		return id
	}
	return session + "/" + id
}

type Workspace struct {
	// Session is stamped by the reader, not reported by herdr: pane and workspace
	// ids only mean something inside one session.
	Session     string `json:"-"`
	ID          string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	Focused     bool   `json:"focused"`
	AgentStatus string `json:"agent_status"`
	PaneCount   int    `json:"pane_count"`
	TabCount    int    `json:"tab_count"`
	ActiveTab   string `json:"active_tab_id"`
}

// Tab is one herdr tab: the grouping between a workspace and its panes. herdr
// reports the label; `tab list` is optional, so a missing label falls back to the
// id suffix.
type Tab struct {
	Session     string `json:"-"`
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	PaneCount   int    `json:"pane_count"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
}

func (t Tab) Display() string {
	if strings.TrimSpace(t.Label) != "" {
		return t.Label
	}
	if name := TabName(t.ID); name != "" {
		return "标签 " + name
	}
	return t.ID
}

// TabName is the short form of a tab id: w9:tB becomes tB, so a tab herdr gives
// no label still reads shorter than its full id.
func TabName(id string) string {
	if i := strings.LastIndexByte(id, ':'); i >= 0 && i+1 < len(id) {
		return id[i+1:]
	}
	return ""
}

func (w Workspace) Display() string {
	if strings.TrimSpace(w.Label) != "" {
		return w.Label
	}
	return w.ID
}

type AgentSession struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Value  string `json:"value"`
}

// Pane is a herdr terminal, enriched with the agent occupying it, if any.
type Pane struct {
	// Session is stamped by the reader: pane ids are per-session counters.
	Session            string        `json:"-"`
	ID                 string        `json:"pane_id"`
	WorkspaceID        string        `json:"workspace_id"`
	TabID              string        `json:"tab_id"`
	Label              string        `json:"label"`
	Agent              string        `json:"agent"`
	AgentStatus        string        `json:"agent_status"`
	AgentSession       *AgentSession `json:"agent_session"`
	Cwd                string        `json:"cwd"`
	Focused            bool          `json:"focused"`
	Revision           int64         `json:"revision"`
	StateChangeSeq     int64         `json:"state_change_seq"`
	TerminalTitle      string        `json:"terminal_title"`
	TerminalTitleClean string        `json:"terminal_title_stripped"`
	TerminalID         string        `json:"terminal_id"`

	// Synthetic, filled in by Build: ordinal name such as qodercli-2 and the
	// owning workspace label.
	Ordinal string `json:"-"`
	WsLabel string `json:"-"`
}

// QualifiedID is the `session/pane-id` form, the only pane identity that means
// something across sessions.
func (p Pane) QualifiedID() string { return Qualify(p.Session, p.ID) }

func (p Pane) HasAgent() bool { return p.Agent != "" }

// Trackable reports whether herdr gives this pane a lifecycle that an
// after_completion trigger can wait on. A plain shell stays `unknown` forever, so
// waiting for it to finish is waiting for an event that cannot happen.
func (p Pane) Trackable() bool {
	switch p.AgentStatus {
	case StatusWorking, StatusBlocked, StatusIdle, StatusDone:
		return true
	}
	return false
}

func (p Pane) Title() string {
	t := strings.TrimSpace(p.TerminalTitleClean)
	if t == "" {
		t = strings.TrimSpace(p.Label)
	}
	if t == "" {
		t = p.ID
	}
	return t
}

// Name is the pane on its own, without the workspace prefix, so a stored
// PaneLabel never repeats the workspace it already belongs to.
func (p Pane) Name() string {
	if p.Ordinal != "" {
		return p.Ordinal
	}
	if p.Agent != "" {
		return p.Agent
	}
	return p.ID
}

// Display is the short form shown in the target picker.
func (p Pane) Display() string {
	if p.WsLabel != "" {
		return p.WsLabel + " / " + p.Name()
	}
	return p.Name()
}

// Target is the stored form of this pane: labels are cached so the outbox can
// show a name even while herdr is unreachable.
func (p Pane) Target() model.Target {
	return model.Target{
		// Without the session, a pane picked in another session would be stored as
		// an ambiguous id and later resolve against the primary session.
		Session:        p.Session,
		Workspace:      p.WorkspaceID,
		WorkspaceLabel: p.WsLabel,
		Pane:           p.ID,
		PaneLabel:      p.Name(),
		Agent:          p.Agent,
	}
}

func (p Pane) SessionID() string {
	if p.AgentSession == nil {
		return ""
	}
	return p.AgentSession.Value
}

// Snapshot is one consistent-enough read of herdr runtime state. It can cover
// several sessions: workspace/tab/pane ids are per-session counters, so ids are
// only unique once qualified, and each running session has its own focused pane.
type Snapshot struct {
	Workspaces []Workspace
	Tabs       []Tab
	Panes      []Pane
	// Sessions is the roster, stopped sessions included, so the tree can show a
	// greyed-out session whose messages still exist.
	Sessions []Session
	// Primary is the session an unqualified id belongs to. Empty means this
	// snapshot covers one ambient session and every id is already unqualified.
	Primary   string
	FetchedAt time.Time
	Source    string
	// SeqUnavailable names the sessions whose state_change_seq read failed, keyed
	// by session. Pane status still looks usable, but without a sequence there is
	// no completion edge to detect — and a session that simply has no agents is
	// not an error, so this cannot be one global flag.
	SeqUnavailable map[string]string `json:"seq_unavailable,omitempty"`
}

// SessionOf names the session a target points at. Every message written before
// multi-session support has an empty Target.Session, which means the primary
// session; anything that keys or filters by session must resolve through this
// instead of reading the raw field, or those messages would land in a session no
// tree row names.
func (s *Snapshot) SessionOf(t model.Target) string {
	if t.Session != "" {
		return t.Session
	}
	if s == nil {
		return ""
	}
	return s.Primary
}

// QualifiedPane is the tree and filter key for a target: bare pane ids collide
// across sessions, so the key always carries the session.
func (s *Snapshot) QualifiedPane(t model.Target) string {
	if t.Pane == "" {
		return ""
	}
	return Qualify(s.SessionOf(t), t.Pane)
}

// MultiSession reports whether the roster names more than one session. Labels
// carry a session prefix only then: with a single ambient session the prefix is
// noise on every row, and there is nothing to disambiguate.
func (s *Snapshot) MultiSession() bool {
	return s != nil && len(s.Sessions) > 1
}

// FocusedIn returns the focused pane of one session. Snapshot.Focused would have
// to guess: with several running sessions each carrying a focused pane, "the
// first one" is decided by iteration order.
func (s *Snapshot) FocusedIn(session string) (Pane, bool) {
	if s == nil {
		return Pane{}, false
	}
	for _, p := range s.Panes {
		if p.Session == session && p.Focused {
			return p, true
		}
	}
	return Pane{}, false
}

// Focused returns the primary session's focused pane.
func (s *Snapshot) Focused() (Pane, bool) {
	if s == nil {
		return Pane{}, false
	}
	return s.FocusedIn(s.Primary)
}

// SeqError reports why session `session` cannot supply state_change_seq.
func (s *Snapshot) SeqError(session string) string {
	if s == nil || s.SeqUnavailable == nil {
		return ""
	}
	return s.SeqUnavailable[session]
}

// Build reads one session's topology. The session name is stamped on every
// entity because ids are per-session counters: a merged snapshot built by
// concatenating these results keeps id lookups from crossing sessions.
func Build(workspaces []Workspace, tabs []Tab, panes []Pane, agents []Pane) *Snapshot {
	return BuildSession("", workspaces, tabs, panes, agents)
}

func BuildSession(session string, workspaces []Workspace, tabs []Tab, panes []Pane, agents []Pane) *Snapshot {
	out := make([]Pane, len(panes))
	for i, p := range panes {
		p.Session = session
		out[i] = p
	}
	index := make(map[string]int, len(out))
	for i, p := range out {
		index[p.ID] = i
	}
	for _, a := range agents {
		i, ok := index[a.ID]
		if !ok {
			continue
		}
		out[i].Agent = a.Agent
		out[i].AgentStatus = a.AgentStatus
		out[i].AgentSession = a.AgentSession
		out[i].StateChangeSeq = a.StateChangeSeq
		out[i].Revision = a.Revision
		if a.TerminalTitle != "" {
			out[i].TerminalTitle = a.TerminalTitle
		}
		if a.TerminalTitleClean != "" {
			out[i].TerminalTitleClean = a.TerminalTitleClean
		}
		if a.Cwd != "" {
			out[i].Cwd = a.Cwd
		}
		out[i].Focused = out[i].Focused || a.Focused
	}
	wsLabels := make(map[string]string, len(workspaces))
	for i := range workspaces {
		workspaces[i].Session = session
		wsLabels[workspaces[i].ID] = workspaces[i].Display()
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.WorkspaceID != b.WorkspaceID {
			return a.WorkspaceID < b.WorkspaceID
		}
		if a.TabID != b.TabID {
			if TabSortKey(a.TabID) != TabSortKey(b.TabID) {
				return TabSortKey(a.TabID) < TabSortKey(b.TabID)
			}
			return a.TabID < b.TabID
		}
		return PaneSortKey(a.ID) < PaneSortKey(b.ID)
	})
	// Ordinals are assigned after sorting so qodercli-1 is stable across polls.
	counts := map[string]int{}
	for i, p := range out {
		out[i].WsLabel = wsLabels[p.WorkspaceID]
		if p.Agent == "" {
			continue
		}
		key := p.WorkspaceID + "|" + p.Agent
		counts[key]++
		out[i].Ordinal = p.Agent + "-" + strconv.Itoa(counts[key])
	}
	// Tabs come from herdr when `tab list` answered; otherwise they are derived
	// from the panes, so the tree always has a tab level to group by. Pane order
	// is already workspace-then-tab, so grouping inherits that order.
	ordered := make([]Tab, 0, len(out))
	tabAt := make(map[string]int, len(out))
	meta := make(map[string]Tab, len(tabs))
	for _, t := range tabs {
		meta[t.ID] = t
	}
	for _, p := range out {
		if p.TabID == "" {
			continue
		}
		if i, ok := tabAt[p.TabID]; ok {
			ordered[i].Focused = ordered[i].Focused || p.Focused
			ordered[i].PaneCount++
			continue
		}
		t := Tab{Session: session, ID: p.TabID, WorkspaceID: p.WorkspaceID, PaneCount: 1}
		if m, ok := meta[p.TabID]; ok {
			if m.Label != "" {
				t.Label = m.Label
			}
			t.Number = m.Number
			t.AgentStatus = m.AgentStatus
			t.Focused = m.Focused || p.Focused
			if m.WorkspaceID != "" {
				t.WorkspaceID = m.WorkspaceID
			}
		}
		ordered = append(ordered, t)
		tabAt[p.TabID] = len(ordered) - 1
	}
	return &Snapshot{Workspaces: workspaces, Tabs: ordered, Panes: out, FetchedAt: time.Now(), Source: "snapshot"}
}

// numericTail reads the digits at the end of an opaque id. herdr reuses suffixes
// with a letter for panes created after a close, so w9:p1N sorts beside w9:p1.
func numericTail(id string, marker byte) int {
	i := strings.LastIndexByte(id, marker)
	if i < 0 || i+1 >= len(id) {
		return 0
	}
	end := i + 1
	for end < len(id) && id[end] >= '0' && id[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(id[i+1 : end])
	if err != nil {
		return 0
	}
	return n
}

// PaneSortKey orders w1:p10 after w1:p2 instead of by raw string.
func PaneSortKey(id string) int {
	if i := strings.LastIndex(id, ":p"); i >= 0 {
		return numericTail(id[i:], 'p')
	}
	return numericTail(id, 'p')
}

// TabSortKey orders w1:t10 after w1:t2.
func TabSortKey(id string) int {
	return numericTail(id, 't')
}

// Pane finds a pane by id. The id may be qualified (`app/w1:p1`) or bare, in
// which case it is looked up in the primary session: pane ids are per-session
// counters, so an unqualified id must never match another session's pane.
func (s *Snapshot) Pane(id string) (Pane, bool) {
	if s == nil {
		return Pane{}, false
	}
	session, bare := SplitQualified(id)
	if session == "" {
		session = s.Primary
	}
	for _, p := range s.Panes {
		if p.ID == bare && p.Session == session {
			return p, true
		}
	}
	return Pane{}, false
}

// Workspace finds a workspace by id, qualified or bare-in-primary.
func (s *Snapshot) Workspace(id string) (Workspace, bool) {
	if s == nil {
		return Workspace{}, false
	}
	session, bare := SplitQualified(id)
	if session == "" {
		session = s.Primary
	}
	for _, w := range s.Workspaces {
		if w.ID == bare && w.Session == session {
			return w, true
		}
	}
	return Workspace{}, false
}

// Tab finds a tab by id, qualified or bare-in-primary, so the tree can label the
// middle level without mixing two sessions' `w1:t1`.
func (s *Snapshot) Tab(id string) (Tab, bool) {
	if s == nil || id == "" {
		return Tab{}, false
	}
	session, bare := SplitQualified(id)
	if session == "" {
		session = s.Primary
	}
	for _, t := range s.Tabs {
		if t.ID == bare && t.Session == session {
			return t, true
		}
	}
	return Tab{}, false
}

// Resolve maps a stored target onto a live pane. The pane id may be qualified
// (`app/w1:p1`); an unqualified target belongs to the primary session, because
// pane ids are per-session counters and matching across sessions would deliver a
// message into a different terminal that happens to share the id. Label and
// ordinal fallbacks stay inside the resolved session too.
func (s *Snapshot) Resolve(t model.Target) (Pane, bool) {
	if s == nil {
		return Pane{}, false
	}
	session, paneID := SplitQualified(t.Pane)
	if t.Session != "" {
		session = t.Session
	}
	if session == "" {
		session = s.Primary
	}
	_, wsID := SplitQualified(t.Workspace)
	wsMatch := func(p Pane) bool {
		if p.Session != session {
			return false
		}
		if wsID == "" {
			return true
		}
		return p.WorkspaceID == wsID || strings.EqualFold(p.WsLabel, wsID)
	}
	if paneID != "" {
		for _, p := range s.Panes {
			if p.ID == paneID && wsMatch(p) {
				return p, true
			}
		}
		for _, p := range s.Panes {
			if strings.EqualFold(p.ID, paneID) && wsMatch(p) {
				return p, true
			}
		}
		for _, p := range s.Panes {
			if !wsMatch(p) {
				continue
			}
			if i := strings.LastIndex(p.ID, ":"); i >= 0 {
				if strings.EqualFold(p.ID[i+1:], paneID) {
					return p, true
				}
			}
		}
	}
	name := paneID
	if name == "" {
		name = t.PaneLabel
	}
	if name != "" {
		for _, p := range s.Panes {
			if !wsMatch(p) {
				continue
			}
			if strings.EqualFold(p.Name(), name) || strings.EqualFold(p.Label, name) {
				return p, true
			}
		}
		if i := strings.LastIndex(name, "-"); i > 0 {
			kind, suffix := name[:i], name[i+1:]
			if _, err := strconv.Atoi(suffix); err == nil {
				var hits []Pane
				for _, p := range s.Panes {
					if wsMatch(p) && p.Agent != "" && (strings.EqualFold(p.Agent, kind) ||
						strings.HasPrefix(strings.ToLower(p.Agent), strings.ToLower(kind))) {
						hits = append(hits, p)
					}
				}
				if n, err := strconv.Atoi(suffix); err == nil && n >= 1 && n <= len(hits) {
					return hits[n-1], true
				}
			}
		}
	}
	if paneID == "" && t.PaneLabel == "" && wsID != "" {
		for _, p := range s.Panes {
			if wsMatch(p) && p.Focused {
				return p, true
			}
		}
	}
	return Pane{}, false
}

// ErrUnavailable means herdr could not be reached.
var ErrUnavailable = errors.New("herdr is not reachable")

type CLIError struct {
	Code    string
	Message string
}

func (e *CLIError) Error() string {
	if e.Code != "" {
		return e.Code + ": " + e.Message
	}
	return e.Message
}

func code(err error, want string) bool {
	var ce *CLIError
	if errors.As(err, &ce) {
		return ce.Code == want
	}
	return false
}

// Blocked reports herdr's agent_blocked: the pane is waiting on a human, so no
// input was written.
func Blocked(err error) bool { return code(err, "agent_blocked") }

// Stalled reports herdr's agent_prompt_stalled: input was written but the agent
// did not move within herdr's observation window. Treat as delivered.
func Stalled(err error) bool { return code(err, "agent_prompt_stalled") }

// NoAgent reports a pane that does not host a recognized agent. herdr answers
// `agent prompt` with agent_not_found both when the pane has no agent and when the
// pane is gone, so callers must let the pane surface decide which one it was.
func NoAgent(err error) bool {
	return code(err, "agent_not_found") || code(err, "no_agent") ||
		code(err, "unknown_agent") || code(err, "agent_unavailable")
}

// PaneMissing reports herdr's pane_not_found: the pane was closed.
func PaneMissing(err error) bool { return code(err, "pane_not_found") }

func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }

// SendResult describes how a message reached the pane.
type SendResult struct {
	Surface string // "agent" or "pane"
	Warning string
}

// Backend is the contract herdr-outbox needs from a workspace manager.
type Backend interface {
	Name() string
	// Probe reports whether the backend is usable right now.
	Probe(ctx context.Context) error
	Snapshot(ctx context.Context) (*Snapshot, error)
	// Send writes text to a pane and submits it. Implementations must not retry
	// internally: the caller records the attempt and never re-sends blindly.
	Send(ctx context.Context, paneID, text string) (SendResult, error)
	Focus(ctx context.Context, paneID string) error
	// ReadPane reads recent terminal output from a pane.
	ReadPane(ctx context.Context, paneID string, lines int) (string, error)
}

var _ Backend = (*CLI)(nil)
var _ Backend = (*Fake)(nil)
