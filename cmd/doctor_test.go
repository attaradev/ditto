package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckRemoteServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/copies" {
			t.Fatalf("path: got %q, want /v2/copies", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ok, msg := checkRemoteServer(t.Context(), srv.URL, "token")
	if !ok {
		t.Fatalf("checkRemoteServer ok: got false msg=%s", msg)
	}

	ok, msg = checkRemoteServer(t.Context(), srv.URL, "")
	if ok {
		t.Fatalf("checkRemoteServer without token: got ok msg=%s", msg)
	}
}
