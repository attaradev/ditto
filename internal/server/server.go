// Package server implements the ditto shared-host HTTP API.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/attaradev/ditto/internal/apiv2"
	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/dumpfetch"
	"github.com/attaradev/ditto/internal/oidc"
	"github.com/attaradev/ditto/internal/refresh"
	"github.com/attaradev/ditto/internal/store"
)

type Controller interface {
	Create(ctx context.Context, opts copypkg.CreateOptions) (*store.Copy, error)
	Destroy(ctx context.Context, id string) error
}

type Refresher interface {
	Refresh(ctx context.Context, targetName string, opts refresh.Options) (*refresh.Result, error)
}

type Authenticator interface {
	Authenticate(ctx context.Context, authHeader string) (*oidc.Principal, error)
}

type StatusResponse = apiv2.StatusResponse

type Server struct {
	controller Controller
	refresher  Refresher
	copies     *store.CopyStore
	events     *store.EventStore
	auth       Authenticator
	statusFn   func() StatusResponse
	srv        *http.Server
}

type principalKey struct{}

func New(
	addr string,
	controller Controller,
	refresher Refresher,
	copies *store.CopyStore,
	events *store.EventStore,
	auth Authenticator,
	statusFn func() StatusResponse,
) *Server {
	s := &Server{
		controller: controller,
		refresher:  refresher,
		copies:     copies,
		events:     events,
		auth:       auth,
		statusFn:   statusFn,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v2/copies", s.authenticated(s.handleCreate))
	mux.HandleFunc("GET /v2/copies", s.authenticated(s.handleList))
	mux.HandleFunc("DELETE /v2/copies/{id}", s.authenticated(s.handleDelete))
	mux.HandleFunc("GET /v2/copies/{id}/events", s.authenticated(s.handleEvents))
	mux.HandleFunc("GET /v2/status", s.authenticated(s.handleStatus))
	mux.HandleFunc("POST /v2/targets/{name}/refresh", s.authenticated(s.handleTargetRefresh))

	s.srv = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}
	return s
}

func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("host api: listening", "addr", s.srv.Addr)
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

func (s *Server) Handler() http.Handler {
	return s.srv.Handler
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req apiv2.CreateCopyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.TTLSeconds != nil && *req.TTLSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "ttl_seconds must be greater than zero")
		return
	}

	ttlSeconds := 0
	if req.TTLSeconds != nil {
		ttlSeconds = *req.TTLSeconds
	}

	opts := copypkg.CreateOptions{
		TTLSeconds:   ttlSeconds,
		RunID:        req.RunID,
		JobName:      req.JobName,
		OwnerSubject: principal.Subject,
		Obfuscate:    req.Obfuscate,
	}
	if req.DumpURI != "" {
		if !strings.HasPrefix(req.DumpURI, "s3://") && !strings.HasPrefix(req.DumpURI, "https://") {
			writeError(w, http.StatusBadRequest, "dump_uri must be an s3:// or https:// URI")
			return
		}
		localPath, cleanup, err := dumpfetch.Fetch(r.Context(), req.DumpURI)
		if err != nil {
			writeError(w, http.StatusBadRequest, "resolve dump_uri: "+err.Error())
			return
		}
		defer cleanup()
		opts.DumpPath = localPath
	}

	copyRecord, err := s.controller.Create(r.Context(), opts)
	if err != nil {
		slog.Error("host api: create copy failed", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, toCreateResponse(copyRecord))
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	filter := store.ListFilter{}
	if !principal.IsAdmin {
		filter.OwnerSubject = principal.Subject
	}
	copies, err := s.copies.List(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if copies == nil {
		copies = []*store.Copy{}
	}

	resp := make([]apiv2.CopySummary, 0, len(copies))
	for _, copyRecord := range copies {
		resp = append(resp, toCopySummary(copyRecord))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	copyRecord, principal, ok := s.authorizedCopy(w, r)
	if !ok {
		return
	}
	if !principal.IsAdmin && copyRecord.OwnerSubject != principal.Subject {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if err := s.controller.Destroy(r.Context(), copyRecord.ID); err != nil {
		slog.Error("host api: destroy copy failed", "id", copyRecord.ID, "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	copyRecord, _, ok := s.authorizedCopy(w, r)
	if !ok {
		return
	}

	events, err := s.events.List(copyRecord.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := make([]apiv2.CopyEvent, 0, len(events))
	for _, event := range events {
		resp = append(resp, apiv2.CopyEvent{
			Action:    event.Action,
			Actor:     event.Actor,
			Metadata:  redactMap(event.Metadata),
			CreatedAt: event.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !principal.IsAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	writeJSON(w, http.StatusOK, s.statusFn())
}

func (s *Server) handleTargetRefresh(w http.ResponseWriter, r *http.Request) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !principal.IsAdmin {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.refresher == nil {
		writeError(w, http.StatusNotImplemented, "target refresh is not configured")
		return
	}

	var req apiv2.RefreshTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.DumpURI != "" && !strings.HasPrefix(req.DumpURI, "s3://") && !strings.HasPrefix(req.DumpURI, "https://") {
		writeError(w, http.StatusBadRequest, "dump_uri must be an s3:// or https:// URI")
		return
	}

	result, err := s.refresher.Refresh(r.Context(), r.PathValue("name"), refresh.Options{
		DumpURI:   req.DumpURI,
		Confirm:   req.Confirm,
		DryRun:    req.DryRun,
		Obfuscate: req.Obfuscate,
	})
	if err != nil {
		slog.Error("host api: target refresh failed", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, apiv2.RefreshTargetResponse{
		Target:     result.Target,
		Engine:     result.Engine,
		DumpPath:   result.DumpPath,
		DryRun:     result.DryRun,
		Cleaned:    result.Cleaned,
		Restored:   result.Restored,
		Obfuscated: result.Obfuscated,
	})
}

func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		principal, err := s.auth.Authenticate(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, principal)))
	}
}

func (s *Server) authorizedCopy(w http.ResponseWriter, r *http.Request) (*store.Copy, *oidc.Principal, bool) {
	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return nil, nil, false
	}

	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing copy id")
		return nil, nil, false
	}

	copyRecord, err := s.copies.Get(id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "copy not found")
			return nil, nil, false
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, nil, false
	}

	if !principal.IsAdmin && copyRecord.OwnerSubject != principal.Subject {
		writeError(w, http.StatusForbidden, "forbidden")
		return nil, nil, false
	}
	return copyRecord, principal, true
}

func principalFromContext(ctx context.Context) (*oidc.Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(*oidc.Principal)
	return principal, ok
}

func toCreateResponse(copyRecord *store.Copy) apiv2.CreateCopyResponse {
	return apiv2.CreateCopyResponse{
		ID:               copyRecord.ID,
		Status:           string(copyRecord.Status),
		Port:             copyRecord.Port,
		ConnectionString: copyRecord.ConnectionString,
		RunID:            copyRecord.RunID,
		JobName:          copyRecord.JobName,
		ErrorMessage:     copyRecord.ErrorMessage,
		CreatedAt:        copyRecord.CreatedAt,
		ReadyAt:          copyRecord.ReadyAt,
		TTLSeconds:       copyRecord.TTLSeconds,
		Warm:             copyRecord.Warm,
	}
}

func toCopySummary(copyRecord *store.Copy) apiv2.CopySummary {
	return apiv2.CopySummary{
		ID:           copyRecord.ID,
		Status:       string(copyRecord.Status),
		Port:         copyRecord.Port,
		RunID:        copyRecord.RunID,
		JobName:      copyRecord.JobName,
		ErrorMessage: copyRecord.ErrorMessage,
		CreatedAt:    copyRecord.CreatedAt,
		ReadyAt:      copyRecord.ReadyAt,
		DestroyedAt:  copyRecord.DestroyedAt,
		TTLSeconds:   copyRecord.TTLSeconds,
		Warm:         copyRecord.Warm,
	}
}

func redactMap(meta map[string]any) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	out := make(map[string]any, len(meta))
	for k, v := range meta {
		if shouldRedactKey(k) {
			out[k] = "[redacted]"
			continue
		}
		out[k] = redactValue(v)
	}
	return out
}

func redactValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		return redactMap(value)
	case []any:
		out := make([]any, 0, len(value))
		for _, item := range value {
			out = append(out, redactValue(item))
		}
		return out
	case string:
		if looksLikeConnectionString(value) {
			return "[redacted]"
		}
		return value
	default:
		return v
	}
}

func shouldRedactKey(key string) bool {
	switch strings.ToLower(key) {
	case "connection_string", "database_url", "dsn":
		return true
	default:
		return false
	}
}

func looksLikeConnectionString(value string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(trimmed, "postgres://") ||
		strings.HasPrefix(trimmed, "postgresql://") ||
		strings.Contains(trimmed, "@tcp(")
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

var (
	_ Controller = (*copypkg.Manager)(nil)
)
