package oidc

import (
	"context"
	"log/slog"
	"strings"
)

// StaticTokenValidator authenticates requests using a single shared secret token.
// Every valid request is assigned a fixed subject ("static") with admin privileges.
//
// This is intentionally simple — suitable for single-operator evaluation and
// trusted internal networks. Use OIDC (Validator) for multi-user environments.
type StaticTokenValidator struct {
	token string
}

// NewStaticToken creates a StaticTokenValidator. token must be non-empty.
func NewStaticToken(token string) *StaticTokenValidator {
	return &StaticTokenValidator{token: token}
}

// Authenticate accepts "Bearer <token>" where token matches the configured
// static secret. Emits a startup-style warning on every successful auth to
// remind operators to migrate to OIDC.
func (v *StaticTokenValidator) Authenticate(_ context.Context, authHeader string) (*Principal, error) {
	raw := strings.TrimSpace(authHeader)
	if !strings.HasPrefix(raw, "Bearer ") {
		return nil, ErrUnauthorized
	}
	tok := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
	if tok == "" || tok != v.token {
		return nil, ErrUnauthorized
	}
	slog.Warn("static token auth is active — all copies share a single identity; configure OIDC for multi-user environments")
	return &Principal{
		Subject: "static",
		Claims:  map[string]any{"sub": "static"},
		IsAdmin: true,
	}, nil
}
