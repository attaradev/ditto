// Package secret resolves secret references to plaintext values.
// Multiple backends are supported; the format of the reference string selects
// the backend:
//
//   - ""                   — returns the plaintext fallback as-is (dev mode)
//   - "env:VAR_NAME"       — reads from environment variable VAR_NAME
//   - "file:/path/to/file" — reads the file and trims surrounding whitespace
//   - "arn:aws:..."        — AWS Secrets Manager (cached for 5 min)
//
// New backends can be added here without touching any other code.
package secret

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// Cache is a thread-safe resolver with a time-bounded cache for remote secret
// backends. The zero value is ready to use.
type Cache struct {
	mu        sync.RWMutex
	ref       string
	value     string
	fetchedAt time.Time
}

// Resolve returns the secret for ref. If ref is empty, plaintext is returned.
// See package doc for supported ref formats.
func (c *Cache) Resolve(ctx context.Context, ref, plaintext string) (string, error) {
	if ref == "" {
		return plaintext, nil
	}

	switch {
	case strings.HasPrefix(ref, "env:"):
		return resolveEnv(ref[len("env:"):])
	case strings.HasPrefix(ref, "file:"):
		return resolveFile(ref[len("file:"):])
	case strings.HasPrefix(ref, "arn:aws:"):
		return c.resolveAWS(ctx, ref)
	default:
		return "", fmt.Errorf("secret: unsupported reference format %q (use env:, file:, or arn:aws:...)", ref)
	}
}

// resolveEnv reads a secret from an environment variable.
func resolveEnv(varName string) (string, error) {
	v := os.Getenv(varName)
	if v == "" {
		return "", fmt.Errorf("secret: env var %q is not set or empty", varName)
	}
	return v, nil
}

// resolveFile reads a secret from a file, trimming surrounding whitespace.
func resolveFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("secret: read file %q: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// resolveAWS fetches from AWS Secrets Manager, caching the result for 5 min.
func (c *Cache) resolveAWS(ctx context.Context, arn string) (string, error) {
	const ttl = 5 * time.Minute

	c.mu.RLock()
	if c.ref == arn && time.Since(c.fetchedAt) < ttl {
		v := c.value
		c.mu.RUnlock()
		return v, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ref == arn && time.Since(c.fetchedAt) < ttl {
		return c.value, nil
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("secret: load AWS config: %w", err)
	}
	svc := secretsmanager.NewFromConfig(cfg)
	out, err := svc.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &arn,
	})
	if err != nil {
		return "", fmt.Errorf("secret: get AWS secret %s: %w", arn, err)
	}

	raw := ""
	if out.SecretString != nil {
		raw = *out.SecretString
	}

	// Accept either a raw string or a JSON object with a "password" key.
	password := raw
	var obj map[string]string
	if json.Unmarshal([]byte(raw), &obj) == nil {
		if p, ok := obj["password"]; ok {
			password = p
		}
	}

	c.ref = arn
	c.value = password
	c.fetchedAt = time.Now()
	return password, nil
}
