package herdr

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Fake is an in-memory backend used by tests and by `--dry-run`, which lets the
// outbox be exercised on a machine without a running herdr server.
type Fake struct {
	mu          sync.Mutex
	Workspaces  []Workspace
	Panes       []Pane
	Unreachable bool

	Sent     []FakeSend
	Focused  []string
	FailSend error
	// PromptMode makes agent prompt fail with agent_prompt_stalled for panes listed
	// in Stalled, matching herdr's "input written but agent did not move" case.
	Stalled map[string]bool
	// Blocked makes agent prompt refuse for a pane sitting at an approval dialog.
	Blocked map[string]bool
	seq     int64
}

type FakeSend struct {
	PaneID  string
	Text    string
	Surface string
}

func NewFake() *Fake {
	return &Fake{Stalled: map[string]bool{}, Blocked: map[string]bool{}}
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Probe(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Unreachable {
		return fmt.Errorf("%w: fake server is down", ErrUnavailable)
	}
	return nil
}

func (f *Fake) Snapshot(ctx context.Context) (*Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Unreachable {
		return nil, fmt.Errorf("%w: fake server is down", ErrUnavailable)
	}
	panes := make([]Pane, len(f.Panes))
	copy(panes, f.Panes)
	agents := make([]Pane, 0, len(panes))
	for _, p := range panes {
		if p.Agent != "" {
			agents = append(agents, p)
		}
	}
	ws := make([]Workspace, len(f.Workspaces))
	copy(ws, f.Workspaces)
	return Build(ws, panes, agents), nil
}

// SetStatus advances state_change_seq the way herdr does when an agent moves
// between working and idle, so edge detection can be tested faithfully.
func (f *Fake) SetStatus(paneID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	for i := range f.Panes {
		if f.Panes[i].ID == paneID {
			f.Panes[i].AgentStatus = status
			f.Panes[i].StateChangeSeq = f.seq
			return
		}
	}
	f.Panes = append(f.Panes, Pane{ID: paneID, Agent: "qodercli", AgentStatus: status, StateChangeSeq: f.seq})
}

func (f *Fake) AddPane(p Pane) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	p.StateChangeSeq = f.seq
	f.Panes = append(f.Panes, p)
	for _, w := range f.Workspaces {
		if w.ID == p.WorkspaceID {
			return
		}
	}
	f.Workspaces = append(f.Workspaces, Workspace{ID: p.WorkspaceID, Label: p.WorkspaceID})
}

// RemovePane drops a pane, mirroring a closed herdr pane.
func (f *Fake) RemovePane(paneID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.Panes[:0]
	for _, p := range f.Panes {
		if p.ID != paneID {
			out = append(out, p)
		}
	}
	f.Panes = out
}

func (f *Fake) Send(ctx context.Context, paneID, text string) (SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailSend != nil {
		return SendResult{}, f.FailSend
	}
	if strings.TrimSpace(text) == "" {
		return SendResult{}, fmt.Errorf("message body is empty")
	}
	var target *Pane
	for i := range f.Panes {
		if f.Panes[i].ID == paneID {
			target = &f.Panes[i]
			break
		}
	}
	if target == nil {
		return SendResult{}, &CLIError{Code: "pane_not_found", Message: "unknown pane " + paneID}
	}
	if target.Agent == "" {
		f.Sent = append(f.Sent, FakeSend{PaneID: paneID, Text: text, Surface: "pane"})
		return SendResult{Surface: "pane"}, nil
	}
	if f.Blocked[paneID] {
		return SendResult{}, &CLIError{Code: "agent_blocked", Message: "agent is waiting for approval"}
	}
	if f.Stalled[paneID] {
		f.Sent = append(f.Sent, FakeSend{PaneID: paneID, Text: text, Surface: "agent"})
		target.AgentStatus = StatusWorking
		return SendResult{Surface: "agent", Warning: "agent did not start within its observation window"}, nil
	}
	f.Sent = append(f.Sent, FakeSend{PaneID: paneID, Text: text, Surface: "agent"})
	target.AgentStatus = StatusWorking
	f.seq++
	target.StateChangeSeq = f.seq
	return SendResult{Surface: "agent"}, nil
}

func (f *Fake) Sends() []FakeSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeSend, len(f.Sent))
	copy(out, f.Sent)
	return out
}

func (f *Fake) LastSend() (FakeSend, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Sent) == 0 {
		return FakeSend{}, false
	}
	return f.Sent[len(f.Sent)-1], true
}

func (f *Fake) Focus(ctx context.Context, paneID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.Panes {
		if p.ID == paneID {
			if p.Agent == "" {
				return fmt.Errorf("pane %s has no agent to focus", paneID)
			}
			f.Focused = append(f.Focused, paneID)
			return nil
		}
	}
	return &CLIError{Code: "pane_not_found", Message: "unknown pane " + paneID}
}

// ReadPane returns mock terminal output for testing.
func (f *Fake) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.Panes {
		if p.ID == paneID {
			if p.Agent != "" {
				return fmt.Sprintf("[%s agent output]\nStatus: %s\nRecent work in progress...", p.Agent, p.AgentStatus), nil
			}
			return "[shell output]\n$ _", nil
		}
	}
	return "", &CLIError{Code: "pane_not_found", Message: "unknown pane " + paneID}
}

var _ Backend = (*Fake)(nil)
