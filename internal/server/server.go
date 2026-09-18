// Package server runs the herdr-outbox HTTP API and scheduler loop.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/scheduler"
	"herdr-outbox/internal/store"
)

type Server struct {
	store     *store.Store
	engine    *scheduler.Engine
	ledger    *scheduler.Ledger
	broadcast *api.Broadcaster
	dir       string
	interval  time.Duration
	started   time.Time

	httpServer  *http.Server
	listener    net.Listener
	schedCancel context.CancelFunc

	// done is closed by RequestShutdown so the blocked main loop can return; an
	// in-process HTTP handler cannot cancel the caller's context.
	done chan struct{}
	once sync.Once
}

func New(st *store.Store, engine *scheduler.Engine, ledger *scheduler.Ledger, dir string, interval time.Duration) *Server {
	return &Server{
		store:     st,
		engine:    engine,
		ledger:    ledger,
		broadcast: api.NewBroadcaster(),
		dir:       dir,
		interval:  interval,
		done:      make(chan struct{}),
	}
}

// Start binds the listener, writes discovery files, and starts the HTTP server
// and scheduler loop. It returns once the server is ready to accept connections.
func (s *Server) Start(ctx context.Context) error {
	if s.done == nil {
		s.done = make(chan struct{})
	}
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = ln

	port := ln.Addr().(*net.TCPAddr).Port
	if err := api.WriteDiscovery(s.dir, port); err != nil {
		ln.Close()
		return fmt.Errorf("write discovery: %w", err)
	}

	s.httpServer = &http.Server{Handler: mux}
	s.started = time.Now()

	schedCtx, schedCancel := context.WithCancel(ctx)
	s.schedCancel = schedCancel
	go func() {
		s.engine.Run(schedCtx, s.interval, func(ev scheduler.Event) {
			s.broadcast.Publish(toAPIEvent(ev))
		})
	}()

	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server: %v", err)
		}
	}()

	return nil
}

// RequestShutdown asks Wait to return. It never blocks, so a handler can call it
// while it is still holding an open response.
func (s *Server) RequestShutdown() {
	s.once.Do(func() { close(s.done) })
}

// Wait blocks until ctx is cancelled or a shutdown was requested, then drains.
func (s *Server) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
	case <-s.done:
	}
	return s.Stop(context.Background())
}

// Stop cancels the scheduler, releases long-lived streams, and drains the
// listener. Discovery files go first so a client reconnect cannot race back in.
func (s *Server) Stop(ctx context.Context) error {
	api.RemoveDiscovery(s.dir)
	s.RequestShutdown()
	if s.schedCancel != nil {
		s.schedCancel()
	}
	if s.httpServer == nil {
		return nil
	}
	// Shutdown only waits for connections it considers idle, and an SSE stream is
	// never idle, so closing the listener guarantees the drain finishes.
	defer s.httpServer.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the listener address, available after Start.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/messages", s.handleListMessages)
	mux.HandleFunc("GET /api/messages/{id}", s.handleGetMessage)
	mux.HandleFunc("POST /api/messages", s.handleCreateMessage)
	mux.HandleFunc("PUT /api/messages/{id}", s.handleUpdateMessage)
	mux.HandleFunc("DELETE /api/messages/{id}", s.handleDeleteMessage)
	mux.HandleFunc("GET /api/messages/{id}/content", s.handleGetContent)
	mux.HandleFunc("PUT /api/messages/{id}/content", s.handleUpdateContent)
	mux.HandleFunc("POST /api/messages/{id}/send", s.handleSendNow)
	mux.HandleFunc("PUT /api/messages/{id}/trigger", s.handleSetTrigger)
	mux.HandleFunc("PUT /api/messages/{id}/target", s.handleSetTarget)
	mux.HandleFunc("GET /api/snapshot", s.handleSnapshot)
	mux.HandleFunc("GET /api/log", s.handleLog)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/panes/{id}/read", s.handleReadPane)
	mux.HandleFunc("POST /api/server/stop", s.handleServerStop)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, api.ErrorResponse{Error: msg})
}
