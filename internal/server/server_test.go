package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/attaradev/ditto/internal/apiv2"
	copypkg "github.com/attaradev/ditto/internal/copy"
	"github.com/attaradev/ditto/internal/oidc"
	"github.com/attaradev/ditto/internal/refresh"
	"github.com/attaradev/ditto/internal/server"
	"github.com/attaradev/ditto/internal/store"
)

type authStub struct{}

func (a *authStub) Authenticate(_ context.Context, h oidc.AuthHeader) (*oidc.Principal, error) {
	switch h {
	case "Bearer owner-token":
		return &oidc.Principal{Subject: "user-1"}, nil
	case "Bearer admin-token":
		return &oidc.Principal{Subject: "admin-1", IsAdmin: true}, nil
	default:
		return nil, oidc.ErrUnauthorized
	}
}

type controllerStub struct {
	createOpts copypkg.CreateOptions
	createResp *store.Copy
	destroyed  []string
}

func (c *controllerStub) Create(_ context.Context, opts copypkg.CreateOptions) (*store.Copy, error) {
	c.createOpts = opts
	return c.createResp, nil
}

func (c *controllerStub) Destroy(_ context.Context, id string) error {
	c.destroyed = append(c.destroyed, id)
	return nil
}

type refresherStub struct {
	target string
	opts   refresh.Options
	resp   *refresh.Result
}

func (r *refresherStub) Refresh(_ context.Context, targetName string, opts refresh.Options) (*refresh.Result, error) {
	r.target = targetName
	r.opts = opts
	return r.resp, nil
}

func TestServerCreateSetsOwnerSubject(t *testing.T) {
	cs, es := newStores(t)
	controller := &controllerStub{
		createResp: &store.Copy{
			ID:               "copy-1",
			Status:           store.StatusReady,
			Port:             5543,
			ConnectionString: "postgres://user:pass@db.example.com:5543/ditto?sslmode=verify-full",
			RunID:            "run-1",
			JobName:          "job-1",
			TTLSeconds:       3600,
			CreatedAt:        time.Now().UTC(),
		},
	}
	api := newTestAPI(cs, es, controller)

	ttl := 600
	body, err := json.Marshal(apiv2.CreateCopyRequest{
		TTLSeconds: &ttl,
		RunID:      "run-1",
		JobName:    "job-1",
		Obfuscate:  true,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v2/copies", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer owner-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if controller.createOpts.OwnerSubject != "user-1" {
		t.Fatalf("OwnerSubject: got %q, want %q", controller.createOpts.OwnerSubject, "user-1")
	}
	if !controller.createOpts.Obfuscate {
		t.Fatal("Obfuscate: got false, want true")
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["connection_string"]; !ok {
		t.Fatal("create response missing connection_string")
	}
}

func TestServerCreateRejectsLocalDumpURI(t *testing.T) {
	cs, es := newStores(t)
	api := newTestAPI(cs, es, &controllerStub{})

	localPath := filepath.Join(t.TempDir(), "dump.gz")
	if err := os.WriteFile(localPath, []byte("fake"), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}

	for _, uri := range []string{localPath, "/etc/passwd", "../secret.gz", "relative/path.gz"} {
		body, err := json.Marshal(apiv2.CreateCopyRequest{DumpURI: uri})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v2/copies", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer owner-token")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		api.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("URI %q: got status %d, want %d", uri, rec.Code, http.StatusBadRequest)
		}
	}
}

func TestServerListAndEventsRedactSecrets(t *testing.T) {
	cs, es := newStores(t)
	controller := &controllerStub{}
	api := newTestAPI(cs, es, controller)

	seedCopyForOwner(t, cs, &store.Copy{
		ID:               "copy-1",
		Status:           store.StatusReady,
		OwnerSubject:     "user-1",
		ConnectionString: "postgres://hidden",
		TTLSeconds:       3600,
	})
	seedCopyForOwner(t, cs, &store.Copy{
		ID:               "copy-2",
		Status:           store.StatusReady,
		OwnerSubject:     "user-2",
		ConnectionString: "postgres://hidden-2",
		TTLSeconds:       3600,
	})
	seedEventWithSecrets(t, es)

	listRec := serveAuthedRequest(t, api, http.MethodGet, "/v2/copies")
	assertStatusCode(t, listRec, http.StatusOK, "list")
	assertListDoesNotLeakConnectionString(t, decodeListResponse(t, listRec))

	eventRec := serveAuthedRequest(t, api, http.MethodGet, "/v2/copies/copy-1/events")
	assertStatusCode(t, eventRec, http.StatusOK, "events")
	assertEventsRedactSecrets(t, decodeEventResponse(t, eventRec))
}

func seedCopyForOwner(t *testing.T, cs *store.CopyStore, copy *store.Copy) {
	t.Helper()

	if err := cs.Create(copy); err != nil {
		t.Fatalf("create copy %s: %v", copy.ID, err)
	}
}

func seedEventWithSecrets(t *testing.T, es *store.EventStore) {
	t.Helper()

	if err := es.Append("copy", "copy-1", "ready", "system", map[string]any{
		"connection_string": "postgres://secret",
		"nested": map[string]any{
			"dsn": "mysql://secret",
		},
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}
}

func serveAuthedRequest(t *testing.T, api *server.Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer owner-token")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	return rec
}

func assertStatusCode(t *testing.T, rec *httptest.ResponseRecorder, want int, label string) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("%s status: got %d, want %d body=%s", label, rec.Code, want, rec.Body.String())
	}
}

func decodeListResponse(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()

	var listResp []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return listResp
}

func assertListDoesNotLeakConnectionString(t *testing.T, listResp []map[string]any) {
	t.Helper()

	if len(listResp) != 1 {
		t.Fatalf("owner list length: got %d, want %d", len(listResp), 1)
	}
	if _, ok := listResp[0]["connection_string"]; ok {
		t.Fatal("list response leaked connection_string")
	}
}

func decodeEventResponse(t *testing.T, rec *httptest.ResponseRecorder) []apiv2.CopyEvent {
	t.Helper()

	var events []apiv2.CopyEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	return events
}

func assertEventsRedactSecrets(t *testing.T, events []apiv2.CopyEvent) {
	t.Helper()

	if got := events[0].Metadata["connection_string"]; got != "[redacted]" {
		t.Fatalf("connection_string redaction: got %#v, want %#v", got, "[redacted]")
	}
	nested, ok := events[0].Metadata["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested metadata type: got %T", events[0].Metadata["nested"])
	}
	if got := nested["dsn"]; got != "[redacted]" {
		t.Fatalf("nested dsn redaction: got %#v, want %#v", got, "[redacted]")
	}
}

func TestServerStatusRequiresAdmin(t *testing.T) {
	cs, es := newStores(t)
	api := newTestAPI(cs, es, &controllerStub{})

	ownerReq := httptest.NewRequest(http.MethodGet, "/v2/status", nil)
	ownerReq.Header.Set("Authorization", "Bearer owner-token")
	ownerRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(ownerRec, ownerReq)
	if ownerRec.Code != http.StatusForbidden {
		t.Fatalf("owner status code: got %d, want %d", ownerRec.Code, http.StatusForbidden)
	}

	adminReq := httptest.NewRequest(http.MethodGet, "/v2/status", nil)
	adminReq.Header.Set("Authorization", "Bearer admin-token")
	adminRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("admin status code: got %d, want %d body=%s", adminRec.Code, http.StatusOK, adminRec.Body.String())
	}
}

func TestServerTargetRefreshRequiresAdmin(t *testing.T) {
	cs, es := newStores(t)
	refresher := &refresherStub{resp: &refresh.Result{Target: "staging", Engine: "postgres", DryRun: true}}
	api := newTestAPIWithRefresher(cs, es, &controllerStub{}, refresher)

	body, err := json.Marshal(apiv2.RefreshTargetRequest{Confirm: "staging", DryRun: true})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	ownerReq := httptest.NewRequest(http.MethodPost, "/v2/targets/staging/refresh", bytes.NewReader(body))
	ownerReq.Header.Set("Authorization", "Bearer owner-token")
	ownerRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(ownerRec, ownerReq)
	if ownerRec.Code != http.StatusForbidden {
		t.Fatalf("owner status code: got %d, want %d", ownerRec.Code, http.StatusForbidden)
	}

	adminReq := httptest.NewRequest(http.MethodPost, "/v2/targets/staging/refresh", bytes.NewReader(body))
	adminReq.Header.Set("Authorization", "Bearer admin-token")
	adminReq.Header.Set("Content-Type", "application/json")
	adminRec := httptest.NewRecorder()
	api.Handler().ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK {
		t.Fatalf("admin status code: got %d, want %d body=%s", adminRec.Code, http.StatusOK, adminRec.Body.String())
	}
	if refresher.target != "staging" {
		t.Fatalf("target: got %q, want staging", refresher.target)
	}
	if !refresher.opts.DryRun || refresher.opts.Confirm != "staging" {
		t.Fatalf("opts: got %+v", refresher.opts)
	}
}

func TestServerTargetRefreshRejectsLocalDumpURI(t *testing.T) {
	cs, es := newStores(t)
	api := newTestAPIWithRefresher(cs, es, &controllerStub{}, &refresherStub{})

	body, err := json.Marshal(apiv2.RefreshTargetRequest{DumpURI: "/tmp/dump.gz", Confirm: "staging"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v2/targets/staging/refresh", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func newTestAPI(cs *store.CopyStore, es *store.EventStore, controller *controllerStub) *server.Server {
	return newTestAPIWithRefresher(cs, es, controller, nil)
}

func newTestAPIWithRefresher(cs *store.CopyStore, es *store.EventStore, controller *controllerStub, refresher server.Refresher) *server.Server {
	return server.New(server.Config{
		Addr:       ":0",
		Controller: controller,
		Refresher:  refresher,
		Copies:     cs,
		Events:     es,
		Auth:       &authStub{},
		StatusFn: func() server.StatusResponse {
			return server.StatusResponse{
				Version:       "test",
				ActiveCopies:  1,
				WarmCopies:    0,
				PortPoolFree:  10,
				AdvertiseHost: "db.example.com",
			}
		},
	})
}

func newStores(t *testing.T) (*store.CopyStore, *store.EventStore) {
	t.Helper()

	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store.NewCopyStore(db), store.NewEventStore(db)
}
