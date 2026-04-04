// Package server implements the ditto HTTP API server.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/store"
)

// StatusResponse is the payload returned by GET /v1/status.
type StatusResponse struct {
	Version      string `json:"version"`
	ActiveCopies int    `json:"active_copies"`
	WarmCopies   int    `json:"warm_copies"`
	PortPoolFree int    `json:"port_pool_free"`
}

// Server wraps net/http and exposes the ditto copy lifecycle over HTTP.
type Server struct {
	client   copypkg.CopyClient
	token    string
	statusFn func() StatusResponse
	srv      *http.Server
}

// New creates a Server. addr is a listen address like ":8080".
// token is the expected Bearer token; pass "" to disable auth.
// statusFn is called by GET /v1/status to produce operational metrics.
func New(addr string, client copypkg.CopyClient, token string, statusFn func() StatusResponse) *Server {
	s := &Server{
		client:   client,
		token:    token,
		statusFn: statusFn,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/copies", s.auth(s.handleCreate))
	mux.HandleFunc("DELETE /v1/copies/{id}", s.auth(s.handleDelete))
	mux.HandleFunc("GET /v1/copies", s.auth(s.handleList))
	mux.HandleFunc("GET /v1/status", s.auth(s.handleStatus))

	s.srv = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Minute, // copy create can take up to several minutes
		IdleTimeout:  120 * time.Second,
	}
	return s
}

// Start begins listening and blocks until ctx is cancelled, then shuts down
// with a 15-second grace period.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("server: listening", "addr", s.srv.Addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.srv.Shutdown(shutCtx)
}

// --- handlers ---

type createRequest struct {
	TTLSeconds int    `json:"ttl_seconds"`
	RunID      string `json:"run_id"`
	JobName    string `json:"job_name"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	c, err := s.client.Create(r.Context(), copypkg.CreateOptions{
		TTLSeconds: req.TTLSeconds,
		RunID:      req.RunID,
		JobName:    req.JobName,
	})
	if err != nil {
		slog.Error("server: create copy failed", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(c)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing copy id")
		return
	}

	if err := s.client.Destroy(r.Context(), id); err != nil {
		slog.Error("server: destroy copy failed", "id", id, "err", err) //nolint:gosec // id is a URL path segment, not user-controlled log injection
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	copies, err := s.client.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return an empty JSON array rather than null when there are no copies.
	if copies == nil {
		copies = []*store.Copy{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(copies)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	_ = r // unused but required by handler signature
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.statusFn())
}

// --- auth middleware ---

// auth wraps a handler with Bearer token verification.
// When s.token is empty, all requests are allowed through.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

// --- helpers ---

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
