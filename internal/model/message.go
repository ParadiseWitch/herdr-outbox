// Package model defines outbox messages and their lifecycle states.
package model

import (
	"fmt"
	"strings"
	"time"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusPending   Status = "pending"
	StatusScheduled Status = "scheduled"
	StatusWaiting   Status = "waiting"
	StatusSending   Status = "sending"
	StatusSent      Status = "sent"
	StatusFailed    Status = "failed"
)

func (s Status) Label() string {
	switch s {
	case StatusDraft:
		return "草稿"
	case StatusPending:
		return "待发送"
	case StatusScheduled:
		return "定时"
	case StatusWaiting:
		return "等待中"
	case StatusSending:
		return "发送中"
	case StatusSent:
		return "已发送"
	case StatusFailed:
		return "失败"
	default:
		return string(s)
	}
}

func (s Status) Valid() bool {
	switch s {
	case StatusDraft, StatusPending, StatusScheduled, StatusWaiting, StatusSending, StatusSent, StatusFailed:
		return true
	}
	return false
}

// Armed reports whether the scheduler may still act on this message.
func (s Status) Armed() bool {
	switch s {
	case StatusPending, StatusScheduled, StatusWaiting, StatusSending:
		return true
	}
	return false
}

type TriggerKind string

const (
	TriggerManual          TriggerKind = "manual"
	TriggerScheduled       TriggerKind = "scheduled"
	TriggerAfterCompletion TriggerKind = "after_completion"
)

func (k TriggerKind) Valid() bool {
	switch k {
	case TriggerManual, TriggerScheduled, TriggerAfterCompletion:
		return true
	}
	return false
}

func (k TriggerKind) Status() Status {
	switch k {
	case TriggerScheduled:
		return StatusScheduled
	case TriggerAfterCompletion:
		return StatusWaiting
	default:
		return StatusPending
	}
}

func (k TriggerKind) Label() string {
	switch k {
	case TriggerScheduled:
		return "定时"
	case TriggerAfterCompletion:
		return "完成后"
	default:
		return "手动"
	}
}

type Trigger struct {
	Kind TriggerKind `yaml:"kind"`
	// SendAt is the fire time for TriggerScheduled.
	SendAt *time.Time `yaml:"send_at,omitempty"`
	// SettleSeconds suppresses idle/working flicker before an after_completion send.
	SettleSeconds int `yaml:"settle_seconds,omitempty"`
}

// Target addresses a herdr pane. IDs win over labels when both resolve.
type Target struct {
	Workspace      string `yaml:"workspace,omitempty"`
	WorkspaceLabel string `yaml:"workspace_label,omitempty"`
	Pane           string `yaml:"pane,omitempty"`
	PaneLabel      string `yaml:"pane_label,omitempty"`
	Agent          string `yaml:"agent,omitempty"`
}

func (t Target) Empty() bool { return t.Pane == "" && t.Workspace == "" }

func (t Target) Display() string {
	switch {
	case t.PaneLabel != "" && t.WorkspaceLabel != "":
		return t.WorkspaceLabel + " / " + t.PaneLabel
	case t.Pane != "":
		return t.Pane
	case t.WorkspaceLabel != "":
		return t.WorkspaceLabel
	default:
		return "无目标"
	}
}

type Message struct {
	ID        string     `yaml:"id"`
	Title     string     `yaml:"title,omitempty"`
	Target    Target     `yaml:"target"`
	Trigger   Trigger    `yaml:"trigger"`
	Status    Status     `yaml:"status"`
	Favorite  bool       `yaml:"favorite,omitempty"`
	CreatedAt time.Time  `yaml:"created_at"`
	UpdatedAt time.Time  `yaml:"updated_at"`
	SentAt    *time.Time `yaml:"sent_at,omitempty"`

	Attempts  int    `yaml:"attempts,omitempty"`
	LastError string `yaml:"last_error,omitempty"`
	SentVia   string `yaml:"sent_via,omitempty"`

	// Completion bookkeeping for TriggerAfterCompletion.
	// BaselineSeq is the herdr state_change_seq observed when the message was armed;
	// a send requires the sequence to move past it, which proves a real transition
	// happened after arming and survives a missed poll.
	// BaselineSet separates "recorded a baseline of 0" from "never armed": herdr has
	// not made a state change yet on a freshly started agent, so 0 is a real value.
	BaselineSeq     int64      `yaml:"baseline_seq,omitempty"`
	BaselineSet     bool       `yaml:"baseline_set,omitempty"`
	ObservedWorking bool       `yaml:"observed_working,omitempty"`
	SettleSince     *time.Time `yaml:"settle_since,omitempty"`

	// Content is the Markdown body, stored outside the frontmatter.
	Content string `yaml:"-"`
}

// Version identifies one armed revision of a message. It changes when the user
// edits or re-arms the message, which lets the dispatch ledger reject a second
// automatic send of a revision that was already delivered.
func (m *Message) Version() string {
	return m.UpdatedAt.UTC().Format("20060102T150405.000000000")
}

func (m *Message) SettleWindow() time.Duration {
	s := m.Trigger.SettleSeconds
	if s <= 0 {
		s = 3
	}
	return time.Duration(s) * time.Second
}

// Rebind resets completion tracking so the message waits for a fresh edge.
func (m *Message) Rebind(at time.Time) {
	m.ClearBaseline()
	m.Status = m.Trigger.Kind.Status()
	m.UpdatedAt = at
}

// ClearBaseline forgets a recorded arming. Anything that changes a message's
// target, trigger or status has to call it, or the scheduler keeps comparing the
// pane's sequence against a baseline from the old arrangement.
func (m *Message) ClearBaseline() {
	m.BaselineSeq = 0
	m.BaselineSet = false
	m.ObservedWorking = false
	m.SettleSince = nil
}

func (m *Message) Summary() string {
	if m.Title != "" {
		return m.Title
	}
	return DeriveTitle(m.Content)
}

func (m *Message) BodyLines() []string {
	return strings.Split(strings.ReplaceAll(m.Content, "\r\n", "\n"), "\n")
}

func (m *Message) Validate() error {
	if strings.TrimSpace(m.Content) == "" {
		return fmt.Errorf("message is empty")
	}
	if !m.Status.Valid() {
		return fmt.Errorf("unknown status %q", m.Status)
	}
	if !m.Trigger.Kind.Valid() {
		return fmt.Errorf("unknown trigger %q", m.Trigger.Kind)
	}
	switch m.Trigger.Kind {
	case TriggerScheduled:
		if m.Trigger.SendAt == nil {
			return fmt.Errorf("scheduled trigger has no send_at")
		}
		if m.Status.Armed() && m.Target.Pane == "" {
			return fmt.Errorf("scheduled message has no target pane")
		}
	}
	return nil
}

// DeriveTitle picks a short list label from Markdown body text.
func DeriveTitle(content string) string {
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		t = strings.TrimSpace(strings.TrimLeft(t, "#"))
		t = strings.TrimPrefix(t, ">")
		t = strings.TrimSpace(t)
		t = strings.Trim(t, "*_`")
		if t == "" {
			continue
		}
		r := []rune(t)
		if len(r) > 60 {
			return string(r[:57]) + "..."
		}
		return t
	}
	return ""
}

// SortKey orders the outbox: favorites, then armed work oldest-first, then rest.
func SortRank(m *Message) int {
	switch m.Status {
	case StatusSending:
		return 0
	case StatusWaiting:
		return 1
	case StatusScheduled:
		return 2
	case StatusPending:
		return 3
	case StatusFailed:
		return 4
	case StatusDraft:
		return 5
	default:
		return 6
	}
}

func Now() time.Time { return time.Now().Local() }
