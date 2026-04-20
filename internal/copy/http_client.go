package copy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/attaradev/ditto/internal/apiv2"
	"github.com/attaradev/ditto/internal/store"
)

// HTTPClient implements CopyClient by talking to a shared ditto host.
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
	body := apiv2.CreateCopyRequest{
		RunID:     opts.RunID,
		JobName:   opts.JobName,
		DumpURI:   opts.DumpURI,
		Obfuscate: opts.Obfuscate,
	}
	if opts.TTLSeconds > 0 {
		ttl := opts.TTLSeconds
		body.TTLSeconds = &ttl
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v2/copies", bytes.NewReader(data))
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
	var cp apiv2.CreateCopyResponse
	if err := json.NewDecoder(resp.Body).Decode(&cp); err != nil {
		return nil, fmt.Errorf("http_client.Create decode: %w", err)
	}
	return copyFromCreateResponse(cp), nil
}

func (c *HTTPClient) Destroy(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/v2/copies/"+id, nil)
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
		c.baseURL+"/v2/copies", nil)
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
	var copies []apiv2.CopySummary
	if err := json.NewDecoder(resp.Body).Decode(&copies); err != nil {
		return nil, fmt.Errorf("http_client.List decode: %w", err)
	}
	out := make([]*store.Copy, 0, len(copies))
	for _, copyRecord := range copies {
		out = append(out, copyFromSummary(copyRecord))
	}
	return out, nil
}

func (c *HTTPClient) Events(ctx context.Context, id string) ([]apiv2.CopyEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/v2/copies/"+id+"/events", nil)
	if err != nil {
		return nil, fmt.Errorf("http_client.Events: %w", err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http_client.Events: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, decodeHTTPError(resp)
	}
	var events []apiv2.CopyEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("http_client.Events decode: %w", err)
	}
	return events, nil
}

func (c *HTTPClient) Status(ctx context.Context) (*apiv2.StatusResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/v2/status", nil)
	if err != nil {
		return nil, fmt.Errorf("http_client.Status: %w", err)
	}
	c.setAuth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http_client.Status: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, decodeHTTPError(resp)
	}
	var status apiv2.StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("http_client.Status decode: %w", err)
	}
	return &status, nil
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

func copyFromCreateResponse(resp apiv2.CreateCopyResponse) *store.Copy {
	return &store.Copy{
		ID:               resp.ID,
		Status:           store.CopyStatus(resp.Status),
		Port:             resp.Port,
		ConnectionString: resp.ConnectionString,
		RunID:            resp.RunID,
		JobName:          resp.JobName,
		ErrorMessage:     resp.ErrorMessage,
		CreatedAt:        resp.CreatedAt,
		ReadyAt:          resp.ReadyAt,
		TTLSeconds:       resp.TTLSeconds,
		Warm:             resp.Warm,
	}
}

func copyFromSummary(resp apiv2.CopySummary) *store.Copy {
	return &store.Copy{
		ID:           resp.ID,
		Status:       store.CopyStatus(resp.Status),
		Port:         resp.Port,
		RunID:        resp.RunID,
		JobName:      resp.JobName,
		ErrorMessage: resp.ErrorMessage,
		CreatedAt:    resp.CreatedAt,
		ReadyAt:      resp.ReadyAt,
		DestroyedAt:  resp.DestroyedAt,
		TTLSeconds:   resp.TTLSeconds,
		Warm:         resp.Warm,
	}
}

// Compile-time interface check.
var _ CopyClient = (*HTTPClient)(nil)
