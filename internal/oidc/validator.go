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

// AuthHeader is the value of the HTTP Authorization header passed to Authenticate.
type AuthHeader string

// compactJWT is a raw compact-serialised JWT (header.claims.signature).
type compactJWT string

type Config struct {
	Issuer    string
	Audience  string
	JWKSURL   string
	AdminRule AdminRule
}

// AdminRule describes the JWT claim that grants admin access.
// A principal is admin when the claim named Key equals Value.
type AdminRule struct {
	Key   string
	Value string
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
	keys      keySet
	expiresAt time.Time
}

// keySet is the cache of JWKS public keys, keyed by key ID.
type keySet map[string]publicKey

func (ks keySet) find(kid string) (publicKey, bool) {
	if kid != "" {
		key, ok := ks[kid]
		return key, ok
	}
	if len(ks) == 1 {
		for _, key := range ks {
			return key, true
		}
	}
	return publicKey{}, false
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

// jwtToken holds the decoded parts of a compact-serialised JWT.
type jwtToken struct {
	header    jwtHeader
	claims    jwtClaims
	signed    []byte // header.claims — the input to the signature
	signature []byte
}

// jwtClaims wraps the raw JSON claims map with typed accessors so callers
// never need to perform string-key lookups or type assertions inline.
type jwtClaims map[string]any

func (c jwtClaims) issuer() string     { s, _ := c["iss"].(string); return s }
func (c jwtClaims) subject() string    { s, _ := c["sub"].(string); return s }
func (c jwtClaims) audience() any      { return c["aud"] }
func (c jwtClaims) isAdmin(rule AdminRule) bool {
	return matchesClaim(c[rule.Key], rule.Value)
}

func (c jwtClaims) expiry() (int64, bool)    { return numericClaim(c["exp"]) }
func (c jwtClaims) notBefore() (int64, bool) { return numericClaim(c["nbf"]) }

func New(cfg Config) *Validator {
	return &Validator{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *Validator) Authenticate(ctx context.Context, h AuthHeader) (*Principal, error) {
	tok, err := parseJWT(h)
	if err != nil {
		return nil, err
	}

	key, err := v.keyFor(ctx, tok.header.Kid)
	if err != nil {
		return nil, err
	}

	if err := tok.verifySignature(key.Key); err != nil {
		return nil, ErrUnauthorized
	}

	if err := v.validateClaims(tok.claims, time.Now()); err != nil {
		return nil, ErrUnauthorized
	}

	return &Principal{
		Subject: tok.claims.subject(),
		Claims:  map[string]any(tok.claims),
		IsAdmin: tok.claims.isAdmin(v.cfg.AdminRule),
	}, nil
}

// parseJWT extracts the Bearer token from authHeader and fully parses it into
// a jwtToken, returning ErrUnauthorized for any structural problem.
func parseJWT(h AuthHeader) (jwtToken, error) {
	compact, err := extractBearerToken(h)
	if err != nil {
		return jwtToken{}, err
	}
	return decodeCompactJWT(compact)
}

// extractBearerToken strips the "Bearer " scheme from the Authorization header
// and returns the raw compact token string.
func extractBearerToken(h AuthHeader) (compactJWT, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(string(h)), "Bearer ")
	if !ok {
		return "", ErrUnauthorized
	}
	raw := strings.TrimSpace(rest)
	if raw == "" {
		return "", ErrUnauthorized
	}
	return compactJWT(raw), nil
}

// decodeCompactJWT decodes a compact-serialised JWT (header.claims.signature)
// into a jwtToken without verifying the signature.
func decodeCompactJWT(compact compactJWT) (jwtToken, error) {
	parts := strings.Split(string(compact), ".")
	if len(parts) != 3 {
		return jwtToken{}, ErrUnauthorized
	}

	var header jwtHeader
	if err := decodeSegment(parts[0], &header); err != nil {
		return jwtToken{}, ErrUnauthorized
	}
	if header.Alg == "" || header.Alg == "none" {
		return jwtToken{}, ErrUnauthorized
	}

	claims := jwtClaims{}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return jwtToken{}, ErrUnauthorized
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return jwtToken{}, ErrUnauthorized
	}

	return jwtToken{
		header:    header,
		claims:    claims,
		signed:    []byte(parts[0] + "." + parts[1]),
		signature: signature,
	}, nil
}

func (v *Validator) validateClaims(claims jwtClaims, now time.Time) error {
	if claims.issuer() != v.cfg.Issuer {
		return ErrUnauthorized
	}

	if claims.subject() == "" {
		return ErrUnauthorized
	}

	if !audienceContains(claims.audience(), v.cfg.Audience) {
		return ErrUnauthorized
	}

	exp, ok := claims.expiry()
	if !ok || now.Unix() >= exp {
		return ErrUnauthorized
	}

	if nbf, ok := claims.notBefore(); ok && now.Unix() < nbf {
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
		if key, ok := keys.find(kid); ok {
			return key, nil
		}
	}

	if err := v.refreshKeys(ctx); err != nil {
		return publicKey{}, err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()
	if key, ok := v.keys.find(kid); ok {
		return key, nil
	}
	return publicKey{}, ErrUnauthorized
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

	keys := make(keySet, len(doc.Keys))
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
		return parseRSAJWK(key)
	case "EC":
		return parseECJWK(key)
	default:
		return nil, fmt.Errorf("unsupported jwk kty %q", key.KTY)
	}
}

func parseRSAJWK(key jsonWebKey) (*rsa.PublicKey, error) {
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
}

func parseECJWK(key jsonWebKey) (*ecdsa.PublicKey, error) {
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

// digest bundles a hash algorithm, its computed output, and the raw JWT signature,
// carrying all verification material through the verify helpers as a unit.
type digest struct {
	hash crypto.Hash
	sum  []byte
	sig  []byte // raw JWT signature bytes
}

func (tok jwtToken) verifySignature(key crypto.PublicKey) error {
	d, err := tok.computeDigest()
	if err != nil {
		return err
	}

	switch {
	case strings.HasPrefix(tok.header.Alg, "RS"):
		return verifyRSAPKCS1v15(key, d)
	case strings.HasPrefix(tok.header.Alg, "PS"):
		return verifyRSAPSS(key, d)
	case strings.HasPrefix(tok.header.Alg, "ES"):
		return verifyECDSA(key, d)
	default:
		return ErrUnauthorized
	}
}

func (tok jwtToken) computeDigest() (digest, error) {
	var h crypto.Hash
	switch tok.header.Alg {
	case "RS256", "PS256", "ES256":
		h = crypto.SHA256
	case "RS384", "PS384", "ES384":
		h = crypto.SHA384
	case "RS512", "PS512", "ES512":
		h = crypto.SHA512
	default:
		return digest{}, fmt.Errorf("unsupported alg %q", tok.header.Alg)
	}
	hw := h.New()
	_, _ = hw.Write(tok.signed)
	return digest{hash: h, sum: hw.Sum(nil), sig: tok.signature}, nil
}

func verifyRSAPKCS1v15(key crypto.PublicKey, d digest) error {
	pub, ok := key.(*rsa.PublicKey)
	if !ok {
		return ErrUnauthorized
	}
	return rsa.VerifyPKCS1v15(pub, d.hash, d.sum, d.sig)
}

func verifyRSAPSS(key crypto.PublicKey, d digest) error {
	pub, ok := key.(*rsa.PublicKey)
	if !ok {
		return ErrUnauthorized
	}
	return rsa.VerifyPSS(pub, d.hash, d.sum, d.sig, nil)
}

func verifyECDSA(key crypto.PublicKey, d digest) error {
	pub, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return ErrUnauthorized
	}
	size := (pub.Curve.Params().BitSize + 7) / 8
	if len(d.sig) != size*2 {
		return ErrUnauthorized
	}
	r := new(big.Int).SetBytes(d.sig[:size])
	s := new(big.Int).SetBytes(d.sig[size:])
	if !ecdsa.Verify(pub, d.sum, r, s) {
		return ErrUnauthorized
	}
	return nil
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
