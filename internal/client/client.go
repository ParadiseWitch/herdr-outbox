// Package client implements DataProvider over HTTP, talking to the herdr-outbox server.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/dataprovider"
	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
)

var _ dataprovider.DataProvider = (*Client)(nil)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) ListMessages(ctx context.Context) ([]*model.Message, error) {
	var apiMsgs []api.Message
	if err := c.get(ctx, "/api/messages", &apiMsgs); err != nil {
		return nil, err
	}
	out := make([]*model.Message, len(apiMsgs))
	for i := range apiMsgs {
		out[i] = fromAPIMessage(&apiMsgs[i])
	}
	return out, nil
}

func (c *Client) GetMessage(ctx context.Context, id string) (*model.Message, error) {
	var m api.Message
	if err := c.get(ctx, "/api/messages/"+id, &m); err != nil {
		return nil, err
	}
	return fromAPIMessage(&m), nil
}

func (c *Client) CreateMessage(ctx context.Context, msg *model.Message) (*model.Message, error) {
	req := toAPIMessage(msg)
	var resp api.Message
	if err := c.post(ctx, "/api/messages", req, &resp); err != nil {
		return nil, err
	}
	return fromAPIMessage(&resp), nil
}

func (c *Client) UpdateMessage(ctx context.Context, id string, update api.UpdateRequest) (*model.Message, error) {
	var resp api.Message
	if err := c.put(ctx, "/api/messages/"+id, update, &resp); err != nil {
		return nil, err
	}
	return fromAPIMessage(&resp), nil
}

func (c *Client) DeleteMessage(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/api/messages/"+id, nil, nil)
}

func (c *Client) GetContent(ctx context.Context, id string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/messages/"+id+"/content", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", readError(resp)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (c *Client) UpdateContent(ctx context.Context, id string, content string) (*model.Message, error) {
	var resp api.Message
	if err := c.put(ctx, "/api/messages/"+id+"/content", api.ContentRequest{Content: content}, &resp); err != nil {
		return nil, err
	}
	return fromAPIMessage(&resp), nil
}

func (c *Client) SendNow(ctx context.Context, id string) ([]scheduler.Event, error) {
	var resp api.SendResult
	if err := c.post(ctx, "/api/messages/"+id+"/send", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]scheduler.Event, len(resp.Events))
	for i, ev := range resp.Events {
		out[i] = fromAPIEvent(ev)
	}
	return out, nil
}

func (c *Client) SetTrigger(ctx context.Context, id string, kind model.TriggerKind, sendAt *time.Time) (*model.Message, error) {
	req := api.TriggerRequest{Kind: string(kind), SendAt: sendAt}
	var resp api.Message
	if err := c.put(ctx, "/api/messages/"+id+"/trigger", req, &resp); err != nil {
		return nil, err
	}
	return fromAPIMessage(&resp), nil
}

func (c *Client) SetTarget(ctx context.Context, id string, target model.Target) (*model.Message, error) {
	req := api.TargetRequest{Target: api.Target{
		Workspace:      target.Workspace,
		WorkspaceLabel: target.WorkspaceLabel,
		Pane:           target.Pane,
		PaneLabel:      target.PaneLabel,
		Agent:          target.Agent,
	}}
	var resp api.Message
	if err := c.put(ctx, "/api/messages/"+id+"/target", req, &resp); err != nil {
		return nil, err
	}
	return fromAPIMessage(&resp), nil
}

func (c *Client) Snapshot(ctx context.Context) (*herdr.Snapshot, error) {
	var resp api.SnapshotResponse
	if err := c.get(ctx, "/api/snapshot", &resp); err != nil {
		return nil, err
	}
	return fromAPISnapshot(&resp), nil
}

func (c *Client) BackendName() string {
	var status api.ServerStatus
	if err := c.get(context.Background(), "/api/status", &status); err != nil {
		return "unknown"
	}
	return status.Backend
}

func (c *Client) LogRecords(ctx context.Context, n int) ([]scheduler.Record, string, error) {
	var resp api.LogResponse
	if err := c.get(ctx, "/api/log?n="+strconv.Itoa(n), &resp); err != nil {
		return nil, "", err
	}
	out := make([]scheduler.Record, len(resp.Records))
	for i, r := range resp.Records {
		out[i] = scheduler.Record{
			TS:      r.TS,
			ID:      r.ID,
			Version: r.Version,
			Phase:   scheduler.Phase(r.Phase),
			Pane:    r.Pane,
			Surface: r.Surface,
			Reason:  r.Reason,
		}
	}
	return out, resp.Path, nil
}

func (c *Client) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	var resp struct {
		Content string `json:"content"`
	}
	if err := c.get(ctx, fmt.Sprintf("/api/panes/%s/read?lines=%d", paneID, lines), &resp); err != nil {
		return "", err
	}
	return resp.Content, nil
}

func (c *Client) FilePath(id string) (string, error) {
	return "", fmt.Errorf("file path not available for remote server")
}

func (c *Client) LogPath() string {
	return ""
}

func (c *Client) Events(ctx context.Context) (<-chan api.Event, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, readError(resp)
	}
	ch := make(chan api.Event, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		decodeSSE(resp.Body, ch)
	}()
	return ch, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, "GET", path, nil, out)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, "POST", path, body, out)
}

func (c *Client) put(ctx context.Context, path string, body any, out any) error {
	return c.do(ctx, "PUT", path, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
		bodyReader = &buf
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return readError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func readError(resp *http.Response) error {
	var errResp api.ErrorResponse
	if json.NewDecoder(resp.Body).Decode(&errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("server: %s", errResp.Error)
	}
	return fmt.Errorf("server returned %s", resp.Status)
}

func decodeSSE(r io.Reader, ch chan<- api.Event) {
	var dataBuf strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n == 0 {
			if err != nil {
				return
			}
			continue
		}
		dataBuf.Write(buf[:n])
		for {
			s := dataBuf.String()
			idx := strings.Index(s, "\n\n")
			if idx < 0 {
				break
			}
			block := s[:idx]
			dataBuf = strings.Builder{}
			dataBuf.WriteString(s[idx+2:])
			for _, line := range strings.Split(block, "\n") {
				if strings.HasPrefix(line, "data: ") {
					var ev api.Event
					if json.Unmarshal([]byte(line[6:]), &ev) == nil {
						ch <- ev
					}
				}
			}
		}
	}
}

func toAPIMessage(m *model.Message) api.Message {
	return api.Message{
		ID:        m.ID,
		Title:     m.Title,
		Content:   m.Content,
		Target:    toAPITarget(m.Target),
		Trigger:   toAPITrigger(m.Trigger),
		Status:    string(m.Status),
		Favorite:  m.Favorite,
		CreatedAt: m.CreatedAt,
		UpdatedAt: m.UpdatedAt,
		SentAt:    m.SentAt,
		Attempts:  m.Attempts,
		LastError: m.LastError,
		SentVia:   m.SentVia,
		BaselineSeq:     m.BaselineSeq,
		ObservedWorking: m.ObservedWorking,
		SettleSince:     m.SettleSince,
	}
}

func toAPITarget(t model.Target) api.Target {
	return api.Target{
		Workspace:      t.Workspace,
		WorkspaceLabel: t.WorkspaceLabel,
		Pane:           t.Pane,
		PaneLabel:      t.PaneLabel,
		Agent:          t.Agent,
	}
}

func toAPITrigger(t model.Trigger) api.Trigger {
	return api.Trigger{
		Kind:          string(t.Kind),
		SendAt:        t.SendAt,
		SettleSeconds: t.SettleSeconds,
	}
}

func fromAPIMessage(a *api.Message) *model.Message {
	return &model.Message{
		ID:        a.ID,
		Title:     a.Title,
		Content:   a.Content,
		Target:    fromAPITarget(a.Target),
		Trigger:   fromAPITrigger(a.Trigger),
		Status:    model.Status(a.Status),
		Favorite:  a.Favorite,
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
		SentAt:    a.SentAt,
		Attempts:  a.Attempts,
		LastError: a.LastError,
		SentVia:   a.SentVia,
		BaselineSeq:     a.BaselineSeq,
		ObservedWorking: a.ObservedWorking,
		SettleSince:     a.SettleSince,
	}
}

func fromAPITarget(t api.Target) model.Target {
	return model.Target{
		Workspace:      t.Workspace,
		WorkspaceLabel: t.WorkspaceLabel,
		Pane:           t.Pane,
		PaneLabel:      t.PaneLabel,
		Agent:          t.Agent,
	}
}

func fromAPITrigger(t api.Trigger) model.Trigger {
	return model.Trigger{
		Kind:          model.TriggerKind(t.Kind),
		SendAt:        t.SendAt,
		SettleSeconds: t.SettleSeconds,
	}
}

func fromAPIEvent(ev api.Event) scheduler.Event {
	return scheduler.Event{
		Kind:      scheduler.EventKind(ev.Kind),
		MessageID: ev.MessageID,
		Title:     ev.Title,
		Pane:      ev.Pane,
		Detail:    ev.Detail,
		At:        ev.At,
	}
}

func fromAPISnapshot(resp *api.SnapshotResponse) *herdr.Snapshot {
	ws := make([]herdr.Workspace, len(resp.Workspaces))
	for i, w := range resp.Workspaces {
		ws[i] = herdr.Workspace{
			ID:          w.ID,
			Label:       w.Label,
			Number:      w.Number,
			Focused:     w.Focused,
			AgentStatus: w.AgentStatus,
			PaneCount:   w.PaneCount,
			TabCount:    w.TabCount,
			ActiveTab:   w.ActiveTab,
		}
	}
	panes := make([]herdr.Pane, len(resp.Panes))
	for i, p := range resp.Panes {
		panes[i] = herdr.Pane{
			ID:             p.ID,
			WorkspaceID:    p.WorkspaceID,
			TabID:          p.TabID,
			Label:          p.Label,
			Agent:          p.Agent,
			AgentStatus:    p.AgentStatus,
			StateChangeSeq: p.StateChangeSeq,
			Focused:        p.Focused,
			Ordinal:        p.Ordinal,
			WsLabel:        p.WsLabel,
		}
	}
	return &herdr.Snapshot{
		Workspaces: ws,
		Panes:      panes,
		FetchedAt:  resp.FetchedAt,
		Source:     resp.Source,
	}
}
