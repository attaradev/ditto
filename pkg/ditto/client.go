// Package ditto provides a Go client for provisioning ephemeral database copies
// from a running ditto host.
package ditto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/attaradev/ditto/internal/apiv2"
)

// Client talks to a ditto host to provision ephemeral database copies.
type Client struct {
	baseURL string
	token   string
	ttl     time.Duration
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithServerURL sets the base URL of the ditto host (e.g. "http://ditto.internal:8080").
func WithServerURL(url string) Option {
	return func(c *Client) { c.baseURL = url }
}

// WithToken sets the Bearer token used to authenticate with the ditto host.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithTTL sets the lifetime of copies created by the client.
func WithTTL(d time.Duration) Option {
	return func(c *Client) { c.ttl = d }
}

// New creates a Client. At minimum WithServerURL must be provided.
func New(opts ...Option) *Client {
	c := &Client{
		http: &http.Client{Timeout: 5 * time.Minute},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// create provisions a new copy and returns its details.
func (c *Client) create(ctx context.Context) (*apiv2.CreateCopyResponse, error) {
	body := apiv2.CreateCopyRequest{}
	if c.ttl > 0 {
		ttl := int(c.ttl.Seconds())
		body.TTLSeconds = &ttl
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v2/copies", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return nil, fmt.Errorf("ditto: create copy: %s (status %d)", e.Error, resp.StatusCode)
	}

	var cr apiv2.CreateCopyResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("ditto: decode response: %w", err)
	}
	return &cr, nil
}

// WithCopy creates a copy, calls fn with its connection string, then destroys
// the copy when fn returns — regardless of whether fn returns an error.
// This is the non-test counterpart to NewCopy; use it in scripts, CLIs, or
// any context where testing.TB is unavailable.
//
//	err := client.WithCopy(ctx, func(dsn string) error {
//	    return migrate(dsn)
//	})
func (c *Client) WithCopy(ctx context.Context, fn func(dsn string) error) error {
	cr, err := c.create(ctx)
	if err != nil {
		return fmt.Errorf("ditto.WithCopy: create: %w", err)
	}
	defer func() {
		if err := c.destroy(ctx, cr.ID); err != nil {
			// Non-fatal: log but don't overwrite fn's error.
			_ = err
		}
	}()
	return fn(cr.ConnectionString)
}

// destroy deletes the copy identified by id.
func (c *Client) destroy(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v2/copies/"+id, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("ditto: destroy copy %s: %s (status %d)", id, e.Error, resp.StatusCode)
	}
	return nil
}
