package copy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/attaradev/ditto/internal/store"
)

// HTTPClient implements CopyClient by talking to a remote ditto server.
type HTTPClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewHTTPClient creates a client. baseURL is e.g. "http://ditto.internal:8080".
// token is the Bearer auth token; pass "" to skip authentication.
func NewHTTPClient(baseURL, token string) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 5 * time.Minute},
	}
}

func (c *HTTPClient) Create(ctx context.Context, opts CreateOptions) (*store.Copy, error) {
	body, _ := json.Marshal(map[string]any{
		"ttl_seconds": opts.TTLSeconds,
		"run_id":      opts.RunID,
		"job_name":    opts.JobName,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/copies", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("http_client.Create: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http_client.Create: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		return nil, decodeHTTPError(resp)
	}
	var cp store.Copy
	if err := json.NewDecoder(resp.Body).Decode(&cp); err != nil {
		return nil, fmt.Errorf("http_client.Create decode: %w", err)
	}
	return &cp, nil
}

func (c *HTTPClient) Destroy(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/v1/copies/"+id, nil)
	if err != nil {
		return fmt.Errorf("http_client.Destroy: %w", err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http_client.Destroy: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		return decodeHTTPError(resp)
	}
	return nil
}

func (c *HTTPClient) List(ctx context.Context) ([]*store.Copy, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/v1/copies", nil)
	if err != nil {
		return nil, fmt.Errorf("http_client.List: %w", err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http_client.List: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, decodeHTTPError(resp)
	}
	var copies []*store.Copy
	if err := json.NewDecoder(resp.Body).Decode(&copies); err != nil {
		return nil, fmt.Errorf("http_client.List decode: %w", err)
	}
	return copies, nil
}

func (c *HTTPClient) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func decodeHTTPError(resp *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Error != "" {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, body.Error)
	}
	return fmt.Errorf("server returned %d", resp.StatusCode)
}

// Compile-time interface check.
var _ CopyClient = (*HTTPClient)(nil)
