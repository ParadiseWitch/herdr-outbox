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

type Workspace struct {
	ID          string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	Focused     bool   `json:"focused"`
	AgentStatus string `json:"agent_status"`
	PaneCount   int    `json:"pane_count"`
	TabCount    int    `json:"tab_count"`
	ActiveTab   string `json:"active_tab_id"`
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
	// Synthetic, filled in by Build: ordinal name such as qodercli-2 and the
	// owning workspace label.
	Ordinal string `json:"-"`
	WsLabel string `json:"-"`
}

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

// Snapshot is one consistent-enough read of herdr runtime state.
type Snapshot struct {
	Workspaces []Workspace
	Panes      []Pane
	FetchedAt  time.Time
	Source     string
	// AgentSeqError means the read that supplies state_change_seq failed. Pane
	// status still looks usable because `pane list` carries it, but without a
	// sequence there is no completion edge to detect.
	AgentSeqError string `json:"agent_seq_error,omitempty"`
}

func Build(workspaces []Workspace, panes []Pane, agents []Pane) *Snapshot {
	out := make([]Pane, len(panes))
	copy(out, panes)
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
	for _, w := range workspaces {
		wsLabels[w.ID] = w.Display()
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
	return &Snapshot{Workspaces: workspaces, Panes: out, FetchedAt: time.Now(), Source: "snapshot"}
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

func (s *Snapshot) Pane(id string) (Pane, bool) {
	if s == nil {
		return Pane{}, false
	}
	for _, p := range s.Panes {
		if p.ID == id {
			return p, true
		}
	}
	return Pane{}, false
}

func (s *Snapshot) Workspace(id string) (Workspace, bool) {
	if s == nil {
		return Workspace{}, false
	}
	for _, w := range s.Workspaces {
		if w.ID == id {
			return w, true
		}
	}
	return Workspace{}, false
}

// Resolve maps a stored target onto a live pane. It prefers the pane id and falls
// back to herdr labels and the synthetic agentkind-N name so hand written targets
// like `pane: qoder-2` keep working.
func (s *Snapshot) Resolve(t model.Target) (Pane, bool) {
	if s == nil {
		return Pane{}, false
	}
	wsMatch := func(p Pane) bool {
		if t.Workspace == "" {
			return true
		}
		return p.WorkspaceID == t.Workspace || strings.EqualFold(p.WsLabel, t.Workspace)
	}
	if t.Pane != "" {
		for _, p := range s.Panes {
			if p.ID == t.Pane && wsMatch(p) {
				return p, true
			}
		}
		for _, p := range s.Panes {
			if strings.EqualFold(p.ID, t.Pane) && wsMatch(p) {
				return p, true
			}
		}
		for _, p := range s.Panes {
			if !wsMatch(p) {
				continue
			}
			key := t.Pane
			if i := strings.LastIndex(p.ID, ":"); i >= 0 {
				if strings.EqualFold(p.ID[i+1:], key) {
					return p, true
				}
			}
		}
	}
	name := t.Pane
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
	if t.Pane == "" && t.PaneLabel == "" && t.Workspace != "" {
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
