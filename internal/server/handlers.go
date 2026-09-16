package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
)

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]api.Message, len(msgs))
	for i, m := range msgs {
		out[i] = toAPIMessage(m)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.store.Get(id)
	if err != nil {
		status := http.StatusInternalServerError
		if isNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPIMessage(m))
}

func (s *Server) handleCreateMessage(w http.ResponseWriter, r *http.Request) {
	var req api.Message
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	m := fromAPIMessage(&req)
	created, err := s.store.Create(m)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.broadcast.Publish(api.Event{Kind: "message_created", MessageID: created.ID, Title: created.Summary(), At: time.Now()})
	writeJSON(w, http.StatusCreated, toAPIMessage(created))
}

func (s *Server) handleUpdateMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.store.Get(id)
	if err != nil {
		status := http.StatusInternalServerError
		if isNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	var req api.UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Favorite != nil {
		m.Favorite = *req.Favorite
	}
	if req.Title != nil {
		m.Title = *req.Title
	}
	if req.Status != nil {
		m.Status = model.Status(*req.Status)
		m.BaselineSeq = 0
		m.ObservedWorking = false
		m.SettleSince = nil
		m.LastError = ""
	}
	m.UpdatedAt = model.Now()
	if err := s.store.Save(m); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.broadcast.Publish(api.Event{Kind: "message_updated", MessageID: m.ID, Title: m.Summary(), At: time.Now()})
	writeJSON(w, http.StatusOK, toAPIMessage(m))
}

func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.Delete(id); err != nil {
		status := http.StatusInternalServerError
		if isNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	s.broadcast.Publish(api.Event{Kind: "message_deleted", MessageID: id, At: time.Now()})
	writeJSON(w, http.StatusOK, map[string]string{"ok": "deleted"})
}

func (s *Server) handleGetContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.store.Get(id)
	if err != nil {
		status := http.StatusInternalServerError
		if isNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, m.Content)
}

func (s *Server) handleUpdateContent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.store.Get(id)
	if err != nil {
		status := http.StatusInternalServerError
		if isNotFound(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	var req api.ContentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	m.Content = req.Content
	m.UpdatedAt = model.Now()
	if err := s.store.Save(m); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.broadcast.Publish(api.Event{Kind: "message_updated", MessageID: m.ID, Title: m.Summary(), At: time.Now()})
	writeJSON(w, http.StatusOK, toAPIMessage(m))
}

func (s *Server) handleSendNow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	events, err := s.engine.SendNow(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	apiEvs := make([]api.Event, len(events))
	for i, ev := range events {
		apiEvs[i] = toAPIEvent(ev)
		s.broadcast.Publish(apiEvs[i])
	}
	writeJSON(w, http.StatusOK, api.SendResult{Events: apiEvs})
}

func (s *Server) handleSetTrigger(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req api.TriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	kind := model.TriggerKind(req.Kind)
	if !kind.Valid() {
		writeError(w, http.StatusBadRequest, "unknown trigger: "+req.Kind)
		return
	}
	m, err := s.engine.SetTrigger(r.Context(), id, kind, req.SendAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.broadcast.Publish(api.Event{Kind: "message_updated", MessageID: m.ID, Title: m.Summary(), At: time.Now()})
	writeJSON(w, http.StatusOK, toAPIMessage(m))
}

func (s *Server) handleSetTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req api.TargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	t := model.Target{
		Workspace:      req.Target.Workspace,
		WorkspaceLabel: req.Target.WorkspaceLabel,
		Pane:           req.Target.Pane,
		PaneLabel:      req.Target.PaneLabel,
		Agent:          req.Target.Agent,
	}
	m, err := s.engine.SetTarget(r.Context(), id, t)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.broadcast.Publish(api.Event{Kind: "message_updated", MessageID: m.ID, Title: m.Summary(), At: time.Now()})
	writeJSON(w, http.StatusOK, toAPIMessage(m))
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	snap, err := s.engine.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPISnapshot(snap))
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	n := 20
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			n = v
		}
	}
	recs := s.ledger.Tail(n)
	apiRecs := make([]api.LogRecord, len(recs))
	for i, r := range recs {
		apiRecs[i] = api.LogRecord{
			TS:      r.TS,
			ID:      r.ID,
			Version: r.Version,
			Phase:   string(r.Phase),
			Pane:    r.Pane,
			Surface: r.Surface,
			Reason:  r.Reason,
			Error:   r.Error,
		}
	}
	writeJSON(w, http.StatusOK, api.LogResponse{Records: apiRecs, Path: s.ledger.Path()})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	msgs, _ := s.store.List()
	writeJSON(w, http.StatusOK, api.ServerStatus{
		OK:       true,
		Backend:  s.engine.BackendName(),
		Dir:      s.dir,
		Uptime:   time.Since(s.started).Round(time.Second).String(),
		Messages: len(msgs),
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch, id := s.broadcast.Subscribe()
	defer s.broadcast.Unsubscribe(id)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, data)
			flusher.Flush()
		}
	}
}

func (s *Server) handleServerStop(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"ok": "stopping"})
	go func() {
		time.Sleep(100 * time.Millisecond)
		s.Stop(context.Background())
	}()
}

func (s *Server) handleReadPane(w http.ResponseWriter, r *http.Request) {
	paneID := r.PathValue("id")
	lines := 20
	if n := r.URL.Query().Get("lines"); n != "" {
		if v, err := strconv.Atoi(n); err == nil && v > 0 {
			lines = v
		}
	}
	content, err := s.engine.Backend.ReadPane(r.Context(), paneID, lines)
	if err != nil {
		status := http.StatusInternalServerError
		if isNotFound(err) || herdr.PaneMissing(err) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

func isNotFound(err error) bool {
	return err != nil && (err.Error() == "message not found" || len(err.Error()) > 16 && err.Error()[:16] == "message not found")
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

func toAPIEvent(ev scheduler.Event) api.Event {
	return api.Event{
		Kind:      string(ev.Kind),
		MessageID: ev.MessageID,
		Title:     ev.Title,
		Pane:      ev.Pane,
		Detail:    ev.Detail,
		At:        ev.At,
	}
}

func toAPISnapshot(snap *herdr.Snapshot) api.SnapshotResponse {
	ws := make([]api.Workspace, len(snap.Workspaces))
	for i, w := range snap.Workspaces {
		ws[i] = api.Workspace{
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
	panes := make([]api.Pane, len(snap.Panes))
	for i, p := range snap.Panes {
		panes[i] = api.Pane{
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
	return api.SnapshotResponse{
		Workspaces: ws,
		Panes:      panes,
		FetchedAt:  snap.FetchedAt,
		Source:     snap.Source,
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
