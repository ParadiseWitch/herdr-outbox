// Package server runs the herdr-outbox HTTP API and scheduler loop.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"herdr-outbox/internal/api"
	"herdr-outbox/internal/scheduler"
	"herdr-outbox/internal/store"
)

type Server struct {
	store    *store.Store
	engine   *scheduler.Engine
	ledger   *scheduler.Ledger
	broadcast *api.Broadcaster
	dir      string
	interval time.Duration
	started  time.Time

	httpServer *http.Server
	listener   net.Listener
}

func New(st *store.Store, engine *scheduler.Engine, ledger *scheduler.Ledger, dir string, interval time.Duration) *Server {
	return &Server{
		store:     st,
		engine:    engine,
		ledger:    ledger,
		broadcast: api.NewBroadcaster(),
		dir:       dir,
		interval:  interval,
	}
}

// Start binds the listener, writes discovery files, and starts the HTTP server
// and scheduler loop. It returns once the server is ready to accept connections.
func (s *Server) Start(ctx context.Context) error {
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
	go func() {
		s.engine.Run(schedCtx, s.interval, func(ev scheduler.Event) {
			s.broadcast.Publish(toAPIEvent(ev))
		})
	}()

	go func() {
		<-schedCtx.Done()
		schedCancel()
	}()

	go func() {
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("server: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server, scheduler, and removes discovery files.
func (s *Server) Stop(ctx context.Context) error {
	api.RemoveDiscovery(s.dir)
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
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
