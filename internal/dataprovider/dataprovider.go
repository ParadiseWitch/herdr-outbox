// Package dataprovider defines the abstraction layer between the TUI and its data source.
package dataprovider

import (
	"context"
	"time"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
)

// DataProvider is the interface the TUI depends on. A local implementation wraps
// the store and engine directly; a remote implementation talks to the server over HTTP.
type DataProvider interface {
	ListMessages(ctx context.Context) ([]*model.Message, error)
	GetMessage(ctx context.Context, id string) (*model.Message, error)
	CreateMessage(ctx context.Context, msg *model.Message) (*model.Message, error)
	UpdateMessage(ctx context.Context, id string, update api.UpdateRequest) (*model.Message, error)
	DeleteMessage(ctx context.Context, id string) error
	GetContent(ctx context.Context, id string) (string, error)
	UpdateContent(ctx context.Context, id string, content string) (*model.Message, error)

	SendNow(ctx context.Context, id string) ([]scheduler.Event, error)
	SetTrigger(ctx context.Context, id string, kind model.TriggerKind, sendAt *time.Time) (*model.Message, error)
	SetTarget(ctx context.Context, id string, target model.Target) (*model.Message, error)

	Snapshot(ctx context.Context) (*herdr.Snapshot, error)
	BackendName() string
	LogRecords(ctx context.Context, n int) ([]scheduler.Record, string, error)
	ReadPane(ctx context.Context, paneID string, lines int) (string, error)

	Events(ctx context.Context) (<-chan api.Event, error)

	FilePath(id string) (string, error)
	LogPath() string
}

// Scheduler is optionally implemented by a DataProvider that runs the scheduler
// loop locally. When the provider does not implement Scheduler, the TUI skips
// tick-based scheduling (the server handles it).
type Scheduler interface {
	Reconcile(ctx context.Context) []scheduler.Event
	Step(ctx context.Context) ([]scheduler.Event, error)
	CachedSnapshot() (*herdr.Snapshot, error)
}
