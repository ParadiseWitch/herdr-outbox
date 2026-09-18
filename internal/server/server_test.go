package server

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/client"
	"herdr-outbox/internal/herdr"
	"herdr-outbox/internal/model"
	"herdr-outbox/internal/scheduler"
	"herdr-outbox/internal/store"
)

func newTestServer(t *testing.T) (*Server, context.Context, string, *herdr.Fake) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "outbox")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	led, err := scheduler.OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake := herdr.NewFake()
	fake.Workspaces = []herdr.Workspace{{ID: "w1", Label: "dev"}}
	fake.AddPane(herdr.Pane{ID: "w1:p1", WorkspaceID: "w1", AgentStatus: herdr.StatusIdle})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return New(st, scheduler.NewEngine(st, fake, led), led, dir, time.Hour), ctx, dir, fake
}

// A TUI holds /api/events open for its whole lifetime, so a drain that waits for
// idle connections would never finish and the process would outlive `server stop`.
func TestWaitReturnsWhileAStreamIsOpen(t *testing.T) {
	srv, ctx, dir, _ := newTestServer(t)
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	base := "http://" + srv.Addr()

	resp, err := http.Get(base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream: %s", resp.Status)
	}
	streamClosed := make(chan struct{})
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				close(streamClosed)
				return
			}
		}
	}()

	srv.RequestShutdown()
	done := make(chan error, 1)
	go func() { done <- srv.Wait(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait blocked behind an open event stream")
	}
	select {
	case <-streamClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("the event stream outlived the shutdown")
	}
	if api.IsRunning(dir) {
		t.Fatal("discovery files survived the shutdown")
	}
}

// The stop endpoint has to wake the process that is blocking in Wait, not just
// close its listener.
func TestStopEndpointUnblocksWait(t *testing.T) {
	srv, ctx, _, _ := newTestServer(t)
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Wait(ctx) }()

	resp, err := http.Post("http://"+srv.Addr()+"/api/server/stop", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stop handler answered but the process was never released")
	}
}

// Cancelling the caller's context is the signal path; it must reach the same end.
func TestContextCancelStopsServer(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	led, err := scheduler.OpenLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	fake := herdr.NewFake()
	fake.Workspaces = []herdr.Workspace{{ID: "w1", Label: "dev"}}
	fake.AddPane(herdr.Pane{ID: "w1:p1", WorkspaceID: "w1", AgentStatus: herdr.StatusIdle})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := New(st, scheduler.NewEngine(st, fake, led), led, dir, time.Hour)
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Wait(ctx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return")
	}
	if api.IsRunning(dir) {
		t.Fatal("discovery files survived the shutdown")
	}
}

// The TUI decides whether a message may be armed without a live snapshot by
// matching herdr.ErrUnavailable, and errors.Is cannot see a sentinel through a JSON
// body. So an outage leaves the building as 502 and comes back as the sentinel.
func TestUnreachableHerdrCrossesTheWire(t *testing.T) {
	srv, ctx, _, fake := newTestServer(t)
	fake.Unreachable = true
	if err := srv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cl := client.New("http://" + srv.Addr())

	if _, err := cl.Snapshot(context.Background()); !herdr.IsUnavailable(err) {
		t.Fatalf("snapshot lost the sentinel across the wire: %v", err)
	}

	created, err := cl.CreateMessage(context.Background(), &model.Message{
		Content: "hold the trigger",
		Target:  model.Target{Pane: "w1:p1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.SetTrigger(context.Background(), created.ID, model.TriggerAfterCompletion, nil); err != nil {
		t.Fatalf("set trigger refused during an outage: %v", err)
	}
	saved, err := srv.store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Trigger.Kind != model.TriggerAfterCompletion || saved.Status != model.StatusWaiting {
		t.Fatalf("trigger not persisted: kind=%q status=%q", saved.Trigger.Kind, saved.Status)
	}
	got, err := cl.GetMessage(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaselineSet {
		t.Fatal("the client was told a baseline exists that herdr never supplied")
	}
}
