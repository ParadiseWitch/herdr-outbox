package ui

import (
	"context"
	"time"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
	"herdr-outbox/internal/store"
)

// LocalProvider implements DataProvider by wrapping the store and engine directly.
// It is used when the TUI runs against a local data directory (no server).
type LocalProvider struct {
	Store  *store.Store
	Engine *scheduler.Engine
}

func (p *LocalProvider) ListMessages(_ context.Context) ([]*model.Message, error) {
	return p.Store.List()
}

func (p *LocalProvider) GetMessage(_ context.Context, id string) (*model.Message, error) {
	return p.Store.Get(id)
}

func (p *LocalProvider) CreateMessage(_ context.Context, msg *model.Message) (*model.Message, error) {
	return p.Store.Create(msg)
}

func (p *LocalProvider) UpdateMessage(_ context.Context, id string, update api.UpdateRequest) (*model.Message, error) {
	m, err := p.Store.Get(id)
	if err != nil {
		return nil, err
	}
	if update.Favorite != nil {
		m.Favorite = *update.Favorite
	}
	if update.Title != nil {
		m.Title = *update.Title
	}
	if update.Status != nil {
		m.Status = model.Status(*update.Status)
		m.BaselineSeq = 0
		m.ObservedWorking = false
		m.SettleSince = nil
		m.LastError = ""
	}
	m.UpdatedAt = model.Now()
	if err := p.Store.Save(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (p *LocalProvider) DeleteMessage(_ context.Context, id string) error {
	return p.Store.Delete(id)
}

func (p *LocalProvider) GetContent(_ context.Context, id string) (string, error) {
	m, err := p.Store.Get(id)
	if err != nil {
		return "", err
	}
	return m.Content, nil
}

func (p *LocalProvider) UpdateContent(_ context.Context, id string, content string) (*model.Message, error) {
	m, err := p.Store.Get(id)
	if err != nil {
		return nil, err
	}
	m.Content = content
	m.UpdatedAt = model.Now()
	if err := p.Store.Save(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (p *LocalProvider) SendNow(ctx context.Context, id string) ([]scheduler.Event, error) {
	return p.Engine.SendNow(ctx, id)
}

func (p *LocalProvider) SetTrigger(ctx context.Context, id string, kind model.TriggerKind, sendAt *time.Time) (*model.Message, error) {
	return p.Engine.SetTrigger(ctx, id, kind, sendAt)
}

func (p *LocalProvider) SetTarget(ctx context.Context, id string, target model.Target) (*model.Message, error) {
	return p.Engine.SetTarget(ctx, id, target)
}

func (p *LocalProvider) Snapshot(ctx context.Context) (*herdr.Snapshot, error) {
	return p.Engine.Snapshot(ctx)
}

func (p *LocalProvider) BackendName() string {
	return p.Engine.BackendName()
}

func (p *LocalProvider) LogRecords(_ context.Context, n int) ([]scheduler.Record, string, error) {
	recs := p.Engine.Ledger.Tail(n)
	return recs, p.Engine.Ledger.Path(), nil
}

func (p *LocalProvider) ReadPane(ctx context.Context, paneID string, lines int) (string, error) {
	return p.Engine.Backend.ReadPane(ctx, paneID, lines)
}

func (p *LocalProvider) Events(_ context.Context) (<-chan api.Event, error) {
	ch := make(chan api.Event)
	close(ch)
	return ch, nil
}

func (p *LocalProvider) FilePath(id string) (string, error) {
	return p.Store.Path(id), nil
}

func (p *LocalProvider) LogPath() string {
	return p.Engine.Ledger.Path()
}

func (p *LocalProvider) Reconcile(ctx context.Context) []scheduler.Event {
	return p.Engine.Reconcile(ctx)
}

func (p *LocalProvider) Step(ctx context.Context) ([]scheduler.Event, error) {
	return p.Engine.Step(ctx)
}

func (p *LocalProvider) CachedSnapshot() (*herdr.Snapshot, error) {
	return p.Engine.CachedSnapshot()
}
