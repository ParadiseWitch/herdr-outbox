// Package scheduler arms and fires outbox messages against herdr state.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/store"
)

type EventKind string

const (
	EventSent      EventKind = "sent"
	EventFailed    EventKind = "failed"
	EventDeferred  EventKind = "deferred"
	EventSkipped   EventKind = "skipped"
	EventRecovered EventKind = "recovered"
	EventArmed     EventKind = "armed"
	EventMissed    EventKind = "missed"
)

// Event is a user visible outcome of one scheduler action.
type Event struct {
	Kind      EventKind
	MessageID string
	Title     string
	Pane      string
	Detail    string
	At        time.Time
}

func (e Event) String() string {
	if e.Detail == "" {
		return fmt.Sprintf("%s %s", e.Kind, e.Title)
	}
	return fmt.Sprintf("%s %s (%s)", e.Kind, e.Title, e.Detail)
}

type Engine struct {
	Store   *store.Store
	Backend herdr.Backend
	Ledger  *Ledger
	Now     func() time.Time
	// SnapshotMaxAge bounds how stale a cached herdr read may be when the UI asks.
	SnapshotMaxAge time.Duration

	mu       sync.Mutex
	snapshot *herdr.Snapshot
	probeErr error
}

func NewEngine(st *store.Store, backend herdr.Backend, ledger *Ledger) *Engine {
	return &Engine{Store: st, Backend: backend, Ledger: ledger, Now: model.Now, SnapshotMaxAge: 2 * time.Second}
}

func (e *Engine) clock() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return model.Now()
}

// Snapshot returns a fresh herdr read, reusing a still-valid cache.
func (e *Engine) Snapshot(ctx context.Context) (*herdr.Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotLocked(ctx, e.SnapshotMaxAge)
}

func (e *Engine) snapshotLocked(ctx context.Context, maxAge time.Duration) (*herdr.Snapshot, error) {
	if e.snapshot != nil {
		age := e.clock().Sub(e.snapshot.FetchedAt)
		if maxAge > 0 && age >= 0 && age < maxAge {
			return e.snapshot, nil
		}
	}
	snap, err := e.Backend.Snapshot(ctx)
	if err != nil {
		e.probeErr = err
		return nil, err
	}
	// Freshness is judged against the engine clock, which tests control.
	snap.FetchedAt = e.clock()
	e.snapshot = snap
	e.probeErr = nil
	return snap, nil
}

// CachedSnapshot returns the most recent successful read without contacting herdr.
func (e *Engine) CachedSnapshot() (*herdr.Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snapshot == nil {
		return nil, e.probeErr
	}
	return e.snapshot, nil
}

func (e *Engine) BackendName() string { return e.Backend.Name() }

// Reconcile resolves work left half-done by a previous process. A message parked in
// `sending` is never resent automatically: the ledger decides whether it went out or
// whether the outcome is simply unknown.
func (e *Engine) Reconcile(ctx context.Context) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	msgs, err := e.Store.List()
	if err != nil {
		return []Event{{Kind: EventFailed, Title: "outbox", Detail: err.Error(), At: e.clock()}}
	}
	var events []Event
	for _, m := range msgs {
		if m.Status != model.StatusSending {
			continue
		}
		if rec, ok := e.Ledger.Acked(m.ID, m.Version()); ok {
			m.Status = model.StatusSent
			at := rec.TS
			m.SentAt = &at
			m.SentVia = rec.Surface
			m.LastError = ""
			events = append(events, Event{Kind: EventRecovered, MessageID: m.ID, Title: m.Summary(),
				Pane: m.Target.Display(), Detail: "已确认发送", At: at})
		} else {
			m.Status = model.StatusFailed
			m.LastError = "发送在确认前中断；请检查面板，然后按 s 重试"
			events = append(events, Event{Kind: EventRecovered, MessageID: m.ID, Title: m.Summary(),
				Pane: m.Target.Display(), Detail: "结果未知；已标记为失败", At: e.clock()})
		}
		if err := e.saveState(m); err != nil {
			events = append(events, Event{Kind: EventFailed, MessageID: m.ID, Title: m.Summary(), Detail: err.Error(), At: e.clock()})
		}
	}
	return events
}

// Step advances every armed message by one tick.
func (e *Engine) Step(ctx context.Context) ([]Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	msgs, err := e.Store.List()
	if err != nil {
		return nil, err
	}
	if !needsHerdr(msgs) {
		return nil, nil
	}
	snap, err := e.snapshotLocked(ctx, e.SnapshotMaxAge)
	if err != nil {
		return nil, err
	}
	now := e.clock()

	var events []Event
	for _, m := range msgs {
		evs, err := e.advance(ctx, m, snap, now)
		events = append(events, evs...)
		if err != nil {
			events = append(events, Event{Kind: EventFailed, MessageID: m.ID, Title: m.Summary(), Detail: err.Error(), At: now})
		}
	}
	return events, nil
}

func needsHerdr(msgs []*model.Message) bool {
	for _, m := range msgs {
		switch m.Status {
		case model.StatusWaiting, model.StatusScheduled:
			return true
		}
	}
	return false
}

// advance evaluates one message. It takes the engine lock already held by Step.
func (e *Engine) advance(ctx context.Context, m *model.Message, snap *herdr.Snapshot, now time.Time) ([]Event, error) {
	switch m.Status {
	case model.StatusScheduled:
		if m.Trigger.SendAt == nil {
			m.Rebind(now)
			return []Event{{Kind: EventFailed, MessageID: m.ID, Title: m.Summary(), Detail: "定时触发缺少 send_at；已重置", At: now}}, e.saveEdit(m)
		}
		if now.Before(*m.Trigger.SendAt) {
			return nil, nil
		}
		ev, err := e.dispatchLocked(ctx, m, snap, "scheduled", false)
		return ev, err
	case model.StatusWaiting:
		return e.advanceCompletion(ctx, m, snap, now)
	default:
		return nil, nil
	}
}

// advanceCompletion implements the after_completion trigger.
//
// Sending requires a real state transition after arming, proven by herdr's
// state_change_seq moving past the baseline recorded at arm time. That survives a
// missed poll, ignores a pane that was already idle, and cannot re-fire because the
// sequence does not move again until the agent does new work.
func (e *Engine) advanceCompletion(ctx context.Context, m *model.Message, snap *herdr.Snapshot, now time.Time) ([]Event, error) {
	pane, ok := snap.Resolve(m.Target)
	if !ok {
		if m.LastError == "" {
			m.LastError = "目标面板已消失；按 p 重新选择"
			return []Event{{Kind: EventDeferred, MessageID: m.ID, Title: m.Summary(), Detail: m.LastError, At: now}}, e.saveState(m)
		}
		return nil, nil
	}
	e.bindTarget(m, pane)

	switch pane.AgentStatus {
	case herdr.StatusWorking:
		var events []Event
		changed := false
		if m.BaselineSeq == 0 {
			// Armed while herdr was unreachable: bind now so the task in flight
			// counts as the completion to wait for.
			m.BaselineSeq = pane.StateChangeSeq
			changed = true
		}
		if !m.ObservedWorking {
			m.ObservedWorking = true
			changed = true
			events = append(events, Event{Kind: EventArmed, MessageID: m.ID, Title: m.Summary(),
				Pane: pane.Display(), Detail: "代理工作中；完成后将发送", At: now})
		}
		if m.SettleSince != nil {
			m.SettleSince = nil
			changed = true
		}
		if m.LastError != "" {
			m.LastError = ""
			changed = true
		}
		if changed {
			return events, e.saveState(m)
		}
		return events, nil

	case herdr.StatusBlocked:
		if m.SettleSince != nil {
			m.SettleSince = nil
			return []Event{{Kind: EventDeferred, MessageID: m.ID, Title: m.Summary(),
				Pane: pane.Display(), Detail: "代理等待审批中；消息暂存", At: now}}, e.saveState(m)
		}
		return nil, nil

	case herdr.StatusIdle, herdr.StatusDone:
		if m.BaselineSeq == 0 {
			m.BaselineSeq = pane.StateChangeSeq
			return nil, e.saveState(m)
		}
		if pane.StateChangeSeq == m.BaselineSeq {
			return nil, nil
		}
		if !m.ObservedWorking && pane.StateChangeSeq <= m.BaselineSeq {
			return nil, nil
		}
		window := m.SettleWindow()
		if m.SettleSince == nil {
			stamp := now
			m.SettleSince = &stamp
			return nil, e.saveState(m)
		}
		if now.Sub(*m.SettleSince) < window {
			return nil, nil
		}
		ev, err := e.dispatchLocked(ctx, m, snap, "after_completion", false)
		return ev, err

	default:
		if m.ObservedWorking {
			if m.SettleSince != nil {
				m.SettleSince = nil
				return nil, e.saveState(m)
			}
			return nil, nil
		}
		return nil, nil
	}
}

// bindTarget caches what the resolved pane looks like so the list can show a live
// name instead of a bare pane id.
func (e *Engine) bindTarget(m *model.Message, pane herdr.Pane) {
	m.Target.Pane = pane.ID
	if pane.WsLabel != "" {
		m.Target.WorkspaceLabel = pane.WsLabel
	}
	if pane.WorkspaceID != "" {
		m.Target.Workspace = pane.WorkspaceID
	}
	if name := pane.Name(); name != "" {
		m.Target.PaneLabel = name
	}
	if pane.Agent != "" {
		m.Target.Agent = pane.Agent
	}
}

// Arm prepares a message for its trigger and returns it. For after_completion this
// records the baseline sequence from the current herdr state.
func (e *Engine) Arm(ctx context.Context, m *model.Message) (*herdr.Pane, error) {
	snap, err := e.Snapshot(ctx)
	if err != nil {
		if m.Trigger.Kind == model.TriggerAfterCompletion {
			m.Rebind(e.clock())
			return nil, err
		}
		return nil, err
	}
	return e.armFrom(m, snap), nil
}

func (e *Engine) armFrom(m *model.Message, snap *herdr.Snapshot) *herdr.Pane {
	now := e.clock()
	m.Rebind(now)
	pane, ok := snap.Resolve(m.Target)
	if !ok {
		return nil
	}
	e.bindTarget(m, pane)
	m.BaselineSeq = pane.StateChangeSeq
	m.ObservedWorking = pane.AgentStatus == herdr.StatusWorking
	return &pane
}

// SetTrigger re-arms a message with a new trigger.
func (e *Engine) SetTrigger(ctx context.Context, id string, kind model.TriggerKind, sendAt *time.Time) (*model.Message, error) {
	m, err := e.Store.Get(id)
	if err != nil {
		return nil, err
	}
	if !kind.Valid() {
		return nil, fmt.Errorf("unknown trigger %q", kind)
	}
	if m.Status == model.StatusSending {
		return nil, errors.New("send in progress")
	}
	m.Trigger.Kind = kind
	m.Trigger.SendAt = sendAt
	if kind == model.TriggerScheduled && sendAt == nil {
		m.Status = model.StatusDraft
		m.UpdatedAt = e.clock()
		return m, e.saveEdit(m)
	}
	if _, err := e.Arm(ctx, m); err != nil {
		return m, err
	}
	return m, e.saveEdit(m)
}

// SetTarget points a message at a pane and re-arms it.
func (e *Engine) SetTarget(ctx context.Context, id string, t model.Target) (*model.Message, error) {
	m, err := e.Store.Get(id)
	if err != nil {
		return nil, err
	}
	if m.Status == model.StatusSending {
		return nil, errors.New("send in progress")
	}
	m.Target = t
	if _, err := e.Arm(ctx, m); err != nil && !herdr.IsUnavailable(err) {
		return m, err
	}
	return m, e.saveEdit(m)
}

// SendNow performs an explicit user send. It is the one path allowed to re-send an
// already delivered revision, because the user asked for it.
func (e *Engine) SendNow(ctx context.Context, id string) ([]Event, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, err := e.Store.Get(id)
	if err != nil {
		return nil, err
	}
	m.UpdatedAt = e.clock()
	if err := e.saveEdit(m); err != nil {
		return nil, err
	}
	snap, serr := e.snapshotLocked(ctx, 0)
	if serr != nil {
		m.Status = model.StatusFailed
		m.LastError = serr.Error()
		_ = e.saveState(m)
		return nil, serr
	}
	return e.dispatchLocked(ctx, m, snap, "manual", true)
}

// dispatchLocked writes the message to its target exactly once per revision.
func (e *Engine) dispatchLocked(ctx context.Context, m *model.Message, snap *herdr.Snapshot, reason string, force bool) ([]Event, error) {
	now := e.clock()
	if m.Status == model.StatusSent && !force {
		return []Event{{Kind: EventSkipped, MessageID: m.ID, Title: m.Summary(), Detail: "已发送过", At: now}}, nil
	}
	if err := m.Validate(); err != nil {
		m.Status = model.StatusFailed
		m.LastError = err.Error()
		return []Event{{Kind: EventFailed, MessageID: m.ID, Title: m.Summary(), Detail: err.Error(), At: now}}, e.saveState(m)
	}
	version := m.Version()
	if !force {
		if rec, ok := e.Ledger.Acked(m.ID, version); ok {
			m.Status = model.StatusSent
			m.SentAt = &rec.TS
			m.SentVia = rec.Surface
			return []Event{{Kind: EventSkipped, MessageID: m.ID, Title: m.Summary(),
				Detail: "此版本已于 " + rec.TS.Format("15:04:05") + " 送达", At: now}}, e.saveState(m)
		}
	}
	pane, ok := snap.Resolve(m.Target)
	if !ok {
		m.Status = model.StatusFailed
		m.LastError = "在 herdr 中未找到目标面板；按 p 选择一个活跃面板"
		return []Event{{Kind: EventFailed, MessageID: m.ID, Title: m.Summary(), Detail: m.LastError, At: now}}, e.saveState(m)
	}
	e.bindTarget(m, pane)

	m.Status = model.StatusSending
	m.Attempts++
	m.LastError = ""
	if err := e.saveState(m); err != nil {
		return nil, err
	}
	if err := e.Ledger.Append(Record{TS: now, ID: m.ID, Version: version, Phase: PhaseAttempted,
		Pane: pane.ID, Reason: reason}); err != nil {
		return nil, err
	}

	result, sendErr := e.Backend.Send(ctx, pane.ID, m.Content)
	if sendErr != nil {
		return e.finishFailed(m, version, pane, reason, sendErr, now)
	}
	rec := Record{TS: e.clock(), ID: m.ID, Version: version, Phase: PhaseAcked, Pane: pane.ID,
		Surface: result.Surface, Reason: reason}
	if err := e.Ledger.Append(rec); err != nil {
		return []Event{{Kind: EventFailed, MessageID: m.ID, Title: m.Summary(),
			Detail: "已送达但日志写入失败: " + err.Error(), At: rec.TS}}, nil
	}
	sent := rec.TS
	m.Status = model.StatusSent
	m.SentAt = &sent
	m.SentVia = result.Surface
	m.LastError = ""
	m.SettleSince = nil
	detail := "通过 " + result.Surface
	if result.Warning != "" {
		detail += "; " + result.Warning
	}
	return []Event{{Kind: EventSent, MessageID: m.ID, Title: m.Summary(), Pane: pane.Display(), Detail: detail, At: sent}}, e.saveState(m)
}

func (e *Engine) finishFailed(m *model.Message, version string, pane herdr.Pane, reason string, sendErr error, now time.Time) ([]Event, error) {
	_ = e.Ledger.Append(Record{TS: now, ID: m.ID, Version: version, Phase: PhaseFailed,
		Pane: pane.ID, Reason: reason, Error: sendErr.Error()})
	msg := strings.TrimSpace(sendErr.Error())
	if herdr.Blocked(sendErr) && reason == "after_completion" {
		m.Status = model.StatusWaiting
		m.LastError = msg
		m.SettleSince = nil
		return []Event{{Kind: EventDeferred, MessageID: m.ID, Title: m.Summary(),
			Pane: pane.Display(), Detail: "暂存: " + msg, At: now}}, e.saveState(m)
	}
	m.Status = model.StatusFailed
	m.LastError = msg
	return []Event{{Kind: EventFailed, MessageID: m.ID, Title: m.Summary(),
		Pane: pane.Display(), Detail: msg, At: now}}, e.saveState(m)
}

// saveState persists a lifecycle change without bumping the revision, so the
// ledger guard for this revision stays valid.
func (e *Engine) saveState(m *model.Message) error {
	return e.Store.Save(m)
}

// saveEdit persists a user-visible change and re-versions the message.
func (e *Engine) saveEdit(m *model.Message) error {
	return e.Store.Touch(m, e.clock())
}

// Run is the daemon loop: reconcile once, then tick forever.
func (e *Engine) Run(ctx context.Context, interval time.Duration, emit func(Event)) error {
	if interval <= 0 {
		interval = time.Second
	}
	for _, ev := range e.Reconcile(ctx) {
		if emit != nil {
			emit(ev)
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		events, err := e.Step(ctx)
		if err != nil && emit != nil {
			emit(Event{Kind: EventMissed, Title: "scheduler", Detail: err.Error(), At: e.clock()})
		}
		for _, ev := range events {
			if emit != nil {
				emit(ev)
			}
		}
	}
}
