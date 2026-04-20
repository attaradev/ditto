package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrUnauthorized = errors.New("unauthorized")

type Config struct {
	Issuer     string
	Audience   string
	JWKSURL    string
	AdminClaim string
	AdminValue string
}

type Principal struct {
	Subject string
	Claims  map[string]any
	IsAdmin bool
}

type Validator struct {
	cfg        Config
	httpClient *http.Client

	mu        sync.RWMutex
	keys      map[string]publicKey
	expiresAt time.Time
}

type publicKey struct {
	KeyID string
	Key   crypto.PublicKey
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type jwksDocument struct {
	Keys []jsonWebKey `json:"keys"`
}

type jsonWebKey struct {
	KTY string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Crv string `json:"crv"`
}

func New(cfg Config) *Validator {
	return &Validator{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *Validator) Authenticate(ctx context.Context, authHeader string) (*Principal, error) {
	token := strings.TrimSpace(authHeader)
	if !strings.HasPrefix(token, "Bearer ") {
		return nil, ErrUnauthorized
	}
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return nil, ErrUnauthorized
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrUnauthorized
	}

	var header jwtHeader
	if err := decodeSegment(parts[0], &header); err != nil {
		return nil, ErrUnauthorized
	}
	if header.Alg == "" || header.Alg == "none" {
		return nil, ErrUnauthorized
	}

	claims := map[string]any{}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return nil, ErrUnauthorized
	}

	key, err := v.keyFor(ctx, header.Kid)
	if err != nil {
		return nil, err
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrUnauthorized
	}
	if err := verifyJWTSignature(header.Alg, key.Key, parts[0]+"."+parts[1], signature); err != nil {
		return nil, ErrUnauthorized
	}

	if err := v.validateClaims(claims, time.Now()); err != nil {
		return nil, ErrUnauthorized
	}

	sub, _ := claims["sub"].(string)
	return &Principal{
		Subject: sub,
		Claims:  claims,
		IsAdmin: matchesClaim(claims[v.cfg.AdminClaim], v.cfg.AdminValue),
	}, nil
}

func (v *Validator) validateClaims(claims map[string]any, now time.Time) error {
	iss, _ := claims["iss"].(string)
	if iss != v.cfg.Issuer {
		return ErrUnauthorized
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return ErrUnauthorized
	}

	if !audienceContains(claims["aud"], v.cfg.Audience) {
		return ErrUnauthorized
	}

	exp, ok := numericClaim(claims["exp"])
	if !ok || now.Unix() >= exp {
		return ErrUnauthorized
	}

	if nbf, ok := numericClaim(claims["nbf"]); ok && now.Unix() < nbf {
		return ErrUnauthorized
	}

	return nil
}

func (v *Validator) keyFor(ctx context.Context, kid string) (publicKey, error) {
	v.mu.RLock()
	keys := v.keys
	expiresAt := v.expiresAt
	v.mu.RUnlock()

	if len(keys) > 0 && time.Now().Before(expiresAt) {
		if key, ok := findKey(keys, kid); ok {
			return key, nil
		}
	}

	if err := v.refreshKeys(ctx); err != nil {
		return publicKey{}, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()
	if key, ok := findKey(v.keys, kid); ok {
		return key, nil
	}
	return publicKey{}, ErrUnauthorized
}

func findKey(keys map[string]publicKey, kid string) (publicKey, bool) {
	if kid != "" {
		key, ok := keys[kid]
		return key, ok
	}
	if len(keys) == 1 {
		for _, key := range keys {
			return key, true
		}
	}
	return publicKey{}, false
}

func (v *Validator) refreshKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return fmt.Errorf("oidc: build jwks request: %w", err)
	}
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("oidc: fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("oidc: fetch jwks: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var doc jwksDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("oidc: decode jwks: %w", err)
	}

	keys := make(map[string]publicKey, len(doc.Keys))
	for _, key := range doc.Keys {
		pub, err := parseJWK(key)
		if err != nil {
			return fmt.Errorf("oidc: parse jwk %q: %w", key.Kid, err)
		}
		keys[key.Kid] = publicKey{KeyID: key.Kid, Key: pub}
	}
	if len(keys) == 0 {
		return fmt.Errorf("oidc: jwks returned no usable keys")
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.keys = keys
	v.expiresAt = time.Now().Add(cacheTTL(resp.Header.Get("Cache-Control")))
	return nil
}

func cacheTTL(cacheControl string) time.Duration {
	const fallback = 5 * time.Minute
	for _, part := range strings.Split(cacheControl, ",") {
		part = strings.TrimSpace(part)
		if !strings.HasPrefix(part, "max-age=") {
			continue
		}
		seconds, err := strconv.Atoi(strings.TrimPrefix(part, "max-age="))
		if err != nil || seconds <= 0 {
			return fallback
		}
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func parseJWK(key jsonWebKey) (crypto.PublicKey, error) {
	switch key.KTY {
	case "RSA":
		nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
		if err != nil {
			return nil, fmt.Errorf("decode n: %w", err)
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil {
			return nil, fmt.Errorf("decode e: %w", err)
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		if e == 0 {
			return nil, fmt.Errorf("invalid rsa exponent")
		}
		return &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: e,
		}, nil
	case "EC":
		curve, err := ellipticCurve(key.Crv)
		if err != nil {
			return nil, err
		}
		xBytes, err := base64.RawURLEncoding.DecodeString(key.X)
		if err != nil {
			return nil, fmt.Errorf("decode x: %w", err)
		}
		yBytes, err := base64.RawURLEncoding.DecodeString(key.Y)
		if err != nil {
			return nil, fmt.Errorf("decode y: %w", err)
		}
		pub := &ecdsa.PublicKey{
			Curve: curve,
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		}
		if !curve.IsOnCurve(pub.X, pub.Y) { //nolint:staticcheck // crypto/ecdh migration requires restructuring EC key parsing
			return nil, fmt.Errorf("ec key is not on curve")
		}
		return pub, nil
	default:
		return nil, fmt.Errorf("unsupported jwk kty %q", key.KTY)
	}
}

func ellipticCurve(name string) (elliptic.Curve, error) {
	switch name {
	case "P-256":
		return elliptic.P256(), nil
	case "P-384":
		return elliptic.P384(), nil
	case "P-521":
		return elliptic.P521(), nil
	default:
		return nil, fmt.Errorf("unsupported ec curve %q", name)
	}
}

func verifyJWTSignature(alg string, key crypto.PublicKey, signed string, signature []byte) error {
	hash, err := hashForAlg(alg)
	if err != nil {
		return err
	}
	h := hash.New()
	_, _ = h.Write([]byte(signed))
	sum := h.Sum(nil)

	switch alg {
	case "RS256", "RS384", "RS512":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return ErrUnauthorized
		}
		return rsa.VerifyPKCS1v15(pub, hash, sum, signature)
	case "PS256", "PS384", "PS512":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return ErrUnauthorized
		}
		return rsa.VerifyPSS(pub, hash, sum, signature, nil)
	case "ES256", "ES384", "ES512":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return ErrUnauthorized
		}
		size := (pub.Curve.Params().BitSize + 7) / 8
		if len(signature) != size*2 {
			return ErrUnauthorized
		}
		r := new(big.Int).SetBytes(signature[:size])
		s := new(big.Int).SetBytes(signature[size:])
		if !ecdsa.Verify(pub, sum, r, s) {
			return ErrUnauthorized
		}
		return nil
	default:
		return ErrUnauthorized
	}
}

func hashForAlg(alg string) (crypto.Hash, error) {
	switch alg {
	case "RS256", "PS256", "ES256":
		return crypto.SHA256, nil
	case "RS384", "PS384", "ES384":
		return crypto.SHA384, nil
	case "RS512", "PS512", "ES512":
		return crypto.SHA512, nil
	default:
		return 0, fmt.Errorf("unsupported alg %q", alg)
	}
}

func decodeSegment(segment string, dst any) error {
	data, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func numericClaim(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}

func audienceContains(v any, want string) bool {
	switch aud := v.(type) {
	case string:
		return aud == want
	case []any:
		for _, entry := range aud {
			if s, ok := entry.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func matchesClaim(v any, want string) bool {
	if want == "" {
		return false
	}
	switch claim := v.(type) {
	case string:
		return claim == want
	case bool:
		return strconv.FormatBool(claim) == want
	case float64:
		return strconv.FormatFloat(claim, 'f', -1, 64) == want
	case []any:
		for _, entry := range claim {
			if matchesClaim(entry, want) {
				return true
			}
		}
	}
	return false
}

var (
	_ crypto.Hash = crypto.SHA256
	_             = sha256.New
	_             = sha512.New
)
