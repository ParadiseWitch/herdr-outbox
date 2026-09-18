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
	Workspace      string `json:"workspace,omitempty"`
	WorkspaceLabel string `json:"workspace_label,omitempty"`
	Pane           string `json:"pane,omitempty"`
	PaneLabel      string `json:"pane_label,omitempty"`
	Agent          string `json:"agent,omitempty"`
}

type Trigger struct {
	Kind          string     `json:"kind"`
	SendAt        *time.Time `json:"send_at,omitempty"`
	SettleSeconds int        `json:"settle_seconds,omitempty"`
}

type SnapshotResponse struct {
	Workspaces []Workspace `json:"workspaces"`
	Panes      []Pane      `json:"panes"`
	FetchedAt  time.Time   `json:"fetched_at"`
	Source     string      `json:"source"`
	// AgentSeqError means herdr could not supply state_change_seq, so completion
	// triggers cannot be evaluated. Empty when the read succeeded.
	AgentSeqError string `json:"agent_seq_error,omitempty"`
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

type Pane struct {
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
