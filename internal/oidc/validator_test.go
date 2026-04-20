package oidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestValidatorAuthenticate(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	validator := New(Config{
		Issuer:     "https://issuer.example.com/",
		Audience:   "ditto-ci",
		JWKSURL:    "https://jwks.example.com/keys",
		AdminClaim: "role",
		AdminValue: "admin",
	})
	validator.httpClient = staticJWKSClient(t, privateKey)

	token := signRS256Token(t, privateKey, map[string]any{
		"iss":  "https://issuer.example.com/",
		"sub":  "user-123",
		"aud":  []string{"ditto-ci"},
		"exp":  time.Now().Add(time.Hour).Unix(),
		"role": "admin",
	})

	principal, err := validator.Authenticate(t.Context(), "Bearer "+token)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if principal.Subject != "user-123" {
		t.Fatalf("Subject: got %q, want %q", principal.Subject, "user-123")
	}
	if !principal.IsAdmin {
		t.Fatal("IsAdmin: got false, want true")
	}
}

func TestValidatorRejectsWrongAudience(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	validator := New(Config{
		Issuer:   "https://issuer.example.com/",
		Audience: "ditto-ci",
		JWKSURL:  "https://jwks.example.com/keys",
	})
	validator.httpClient = staticJWKSClient(t, privateKey)

	token := signRS256Token(t, privateKey, map[string]any{
		"iss": "https://issuer.example.com/",
		"sub": "user-123",
		"aud": "different-audience",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	if _, err := validator.Authenticate(t.Context(), "Bearer "+token); err == nil {
		t.Fatal("Authenticate: expected error for wrong audience")
	}
}

func signRS256Token(t *testing.T, privateKey *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()

	header := map[string]any{
		"alg": "RS256",
		"kid": "test-key",
		"typ": "JWT",
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}

	headerSegment := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsSegment := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerSegment + "." + claimsSegment

	sum := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	return fmt.Sprintf("%s.%s", signingInput, base64.RawURLEncoding.EncodeToString(signature))
}

func staticJWKSClient(t *testing.T, privateKey *rsa.PrivateKey) *http.Client {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": "test-key",
				"use": "sig",
				"alg": "RS256",
				"n":   base64.RawURLEncoding.EncodeToString(privateKey.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}

	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type":  []string{"application/json"},
					"Cache-Control": []string{"max-age=60"},
				},
				Body: io.NopCloser(strings.NewReader(string(body))),
			}, nil
		}),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
