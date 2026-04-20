package apiv2

import "time"

type CreateCopyRequest struct {
	TTLSeconds *int   `json:"ttl_seconds,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	JobName    string `json:"job_name,omitempty"`
	DumpURI    string `json:"dump_uri,omitempty"`
	Obfuscate  bool   `json:"obfuscate,omitempty"`
}

type CreateCopyResponse struct {
	ID               string     `json:"id"`
	Status           string     `json:"status"`
	Port             int        `json:"port,omitempty"`
	ConnectionString string     `json:"connection_string"`
	RunID            string     `json:"run_id,omitempty"`
	JobName          string     `json:"job_name,omitempty"`
	ErrorMessage     string     `json:"error_message,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ReadyAt          *time.Time `json:"ready_at,omitempty"`
	TTLSeconds       int        `json:"ttl_seconds"`
	Warm             bool       `json:"warm"`
}

type CopySummary struct {
	ID           string     `json:"id"`
	Status       string     `json:"status"`
	Port         int        `json:"port,omitempty"`
	RunID        string     `json:"run_id,omitempty"`
	JobName      string     `json:"job_name,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	ReadyAt      *time.Time `json:"ready_at,omitempty"`
	DestroyedAt  *time.Time `json:"destroyed_at,omitempty"`
	TTLSeconds   int        `json:"ttl_seconds"`
	Warm         bool       `json:"warm"`
}

type CopyEvent struct {
	Action    string         `json:"action"`
	Actor     string         `json:"actor"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type StatusResponse struct {
	Version       string `json:"version"`
	ActiveCopies  int    `json:"active_copies"`
	WarmCopies    int    `json:"warm_copies"`
	PortPoolFree  int    `json:"port_pool_free"`
	AdvertiseHost string `json:"advertise_host,omitempty"`
}
