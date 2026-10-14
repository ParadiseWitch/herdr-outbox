// Package api defines types shared between the herdr-outbox server and client.
package api

import "time"

type Message struct {
	ID        string     `json:"id"`
	Title     string     `json:"title,omitempty"`
	Content   string     `json:"content"`
	Target    Target     `json:"target"`
	Trigger   Trigger    `json:"trigger"`
	Status    string     `json:"status"`
	Favorite  bool       `json:"favorite,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	SentAt    *time.Time `json:"sent_at,omitempty"`

	Attempts  int    `json:"attempts,omitempty"`
	LastError string `json:"last_error,omitempty"`
	SentVia   string `json:"sent_via,omitempty"`

	BaselineSeq     int64      `json:"baseline_seq,omitempty"`
	BaselineSet     bool       `json:"baseline_set,omitempty"`
	ObservedWorking bool       `json:"observed_working,omitempty"`
	SettleSince     *time.Time `json:"settle_since,omitempty"`
}

type Target struct {
	// Session qualifies the pane: ids are per-session counters, so `w1:p1` in one
	// session is a different terminal than `w1:p1` in another.
	Session        string `json:"session,omitempty"`
	Workspace      string `json:"workspace,omitempty"`
	WorkspaceLabel string `json:"workspace_label,omitempty"`
	// Tab is the level between workspace and pane; the labels cache what each id
	// was titled so a path still reads by name when herdr no longer reports it.
	Tab       string `json:"tab,omitempty"`
	TabLabel  string `json:"tab_label,omitempty"`
	Pane      string `json:"pane,omitempty"`
	PaneLabel string `json:"pane_label,omitempty"`
	Agent     string `json:"agent,omitempty"`
}

type Trigger struct {
	Kind          string     `json:"kind"`
	SendAt        *time.Time `json:"send_at,omitempty"`
	SettleSeconds int        `json:"settle_seconds,omitempty"`
}

type SnapshotResponse struct {
	Workspaces []Workspace `json:"workspaces"`
	Tabs       []Tab       `json:"tabs"`
	Panes      []Pane      `json:"panes"`
	// Sessions is the roster of every herdr session this snapshot covers,
	// stopped ones included, so a remote TUI can build the same four-level tree.
	Sessions []Session `json:"sessions"`
	// Primary is the session unqualified ids belong to.
	Primary   string    `json:"primary,omitempty"`
	FetchedAt time.Time `json:"fetched_at"`
	Source    string    `json:"source"`
	// SeqUnavailable maps a session to the reason its state_change_seq read
	// failed. Keyed per session: one session hosting no agents is not an outage
	// for the others.
	SeqUnavailable map[string]string `json:"seq_unavailable,omitempty"`
}

// Session mirrors herdr's own session roster entry.
type Session struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Socket  string `json:"socket_path,omitempty"`
	Default bool   `json:"default,omitempty"`
	// FocusedPane is this session's focused pane, bare id. Each running session
	// has one, so "the focused pane" is only meaningful with a session named.
	FocusedPane string `json:"focused_pane,omitempty"`
	// Err is why the session could not be read this round.
	Err string `json:"err,omitempty"`
}

type Workspace struct {
	Session     string `json:"session,omitempty"`
	ID          string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	Focused     bool   `json:"focused"`
	AgentStatus string `json:"agent_status"`
	PaneCount   int    `json:"pane_count"`
	TabCount    int    `json:"tab_count"`
	ActiveTab   string `json:"active_tab_id"`
}

// Tab is the level between a workspace and its panes in the herdr tree.
type Tab struct {
	Session     string `json:"session,omitempty"`
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number"`
	PaneCount   int    `json:"pane_count"`
	AgentStatus string `json:"agent_status"`
	Focused     bool   `json:"focused"`
}

type Pane struct {
	Session        string `json:"session,omitempty"`
	ID             string `json:"pane_id"`
	WorkspaceID    string `json:"workspace_id"`
	TabID          string `json:"tab_id"`
	Label          string `json:"label"`
	Agent          string `json:"agent"`
	AgentStatus    string `json:"agent_status"`
	StateChangeSeq int64  `json:"state_change_seq"`
	Focused        bool   `json:"focused"`
	Ordinal        string `json:"ordinal,omitempty"`
	WsLabel        string `json:"ws_label,omitempty"`
	TabLabel       string `json:"tab_label,omitempty"`
}

type SendResult struct {
	Events []Event `json:"events"`
}

type Event struct {
	Kind      string    `json:"kind"`
	MessageID string    `json:"message_id,omitempty"`
	Title     string    `json:"title,omitempty"`
	Pane      string    `json:"pane,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	At        time.Time `json:"at"`
}

type LogRecord struct {
	TS      time.Time `json:"ts"`
	ID      string    `json:"id"`
	Version string    `json:"version"`
	Phase   string    `json:"phase"`
	Pane    string    `json:"pane,omitempty"`
	Surface string    `json:"surface,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Error   string    `json:"error,omitempty"`
}

type LogResponse struct {
	Records []LogRecord `json:"records"`
	Path    string      `json:"path"`
}

type ServerStatus struct {
	OK       bool   `json:"ok"`
	Backend  string `json:"backend"`
	Dir      string `json:"dir"`
	Uptime   string `json:"uptime"`
	Messages int    `json:"messages"`
}

type UpdateRequest struct {
	Favorite *bool   `json:"favorite,omitempty"`
	Status   *string `json:"status,omitempty"`
	Title    *string `json:"title,omitempty"`
}

type TriggerRequest struct {
	Kind          string     `json:"kind"`
	SendAt        *time.Time `json:"send_at,omitempty"`
	SettleSeconds int        `json:"settle_seconds,omitempty"`
}

type TargetRequest struct {
	Target Target `json:"target"`
}

type ContentRequest struct {
	Content string `json:"content"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
