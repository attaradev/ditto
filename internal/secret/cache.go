// Package secret provides a time-bounded AWS Secrets Manager password cache
// shared by all database engine implementations.
package secret

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// Cache is a thread-safe, time-bounded cache for a single Secrets Manager secret.
// The zero value is ready to use.
type Cache struct {
	mu        sync.RWMutex
	arn       string
	value     string
	fetchedAt time.Time
}

// Resolve returns the password for the given source. If arn is empty,
// directPassword is returned immediately. Otherwise the secret is fetched
// from AWS Secrets Manager and cached for 5 minutes.
func (c *Cache) Resolve(ctx context.Context, arn, directPassword string) (string, error) {
	if arn == "" {
		return directPassword, nil
	}
	return c.get(ctx, arn)
}

func (c *Cache) get(ctx context.Context, arn string) (string, error) {
	const ttl = 5 * time.Minute

	c.mu.RLock()
	if c.arn == arn && time.Since(c.fetchedAt) < ttl {
		v := c.value
		c.mu.RUnlock()
		return v, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	// Re-check after acquiring write lock to prevent thundering herd.
	if c.arn == arn && time.Since(c.fetchedAt) < ttl {
		return c.value, nil
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("load AWS config: %w", err)
	}
	svc := secretsmanager.NewFromConfig(cfg)
	out, err := svc.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: &arn,
	})
	if err != nil {
		return "", fmt.Errorf("get secret %s: %w", arn, err)
	}

	raw := ""
	if out.SecretString != nil {
		raw = *out.SecretString
	}

	// The secret may be a raw string or a JSON object with a "password" key.
	password := raw
	var obj map[string]string
	if json.Unmarshal([]byte(raw), &obj) == nil {
		if p, ok := obj["password"]; ok {
			password = p
		}
	}

	c.arn = arn
	c.value = password
	c.fetchedAt = time.Now()
	return password, nil
}
