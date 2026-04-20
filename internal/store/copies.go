package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CopyStatus represents the lifecycle state of an ephemeral database copy.
type CopyStatus string

const (
	StatusPending    CopyStatus = "pending"
	StatusCreating   CopyStatus = "creating"
	StatusReady      CopyStatus = "ready"
	StatusInUse      CopyStatus = "in_use"
	StatusDestroying CopyStatus = "destroying"
	StatusDestroyed  CopyStatus = "destroyed"
	StatusFailed     CopyStatus = "failed"
)

// Copy is the in-memory representation of a row in the copies table.
type Copy struct {
	ID               string     `json:"id"`
	Status           CopyStatus `json:"status"`
	Port             int        `json:"port"`
	ContainerID      string     `json:"container_id"`
	ConnectionString string     `json:"connection_string"`
	OwnerSubject     string     `json:"owner_subject"`
	RunID            string     `json:"run_id"`
	JobName          string     `json:"job_name"`
	ErrorMessage     string     `json:"error_message"`
	CreatedAt        time.Time  `json:"created_at"`
	ReadyAt          *time.Time `json:"ready_at"`
	DestroyedAt      *time.Time `json:"destroyed_at"`
	TTLSeconds       int        `json:"ttl_seconds"`
	Warm             bool       `json:"warm"`
}

// CopyStore wraps a *sql.DB and exposes Copy CRUD operations.
type CopyStore struct {
	db *sql.DB
}

// NewCopyStore returns a CopyStore backed by db.
func NewCopyStore(db *sql.DB) *CopyStore {
	return &CopyStore{db: db}
}

// Create inserts a new copy record. c.ID must be set by the caller (ULID).
func (s *CopyStore) Create(c *Copy) error {
	_, err := s.db.Exec(`
		INSERT INTO copies
			(id, status, port, container_id, connection_string,
			 owner_subject, run_id, job_name, error_message, ttl_seconds, warm)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Status, c.Port, c.ContainerID, c.ConnectionString,
		c.OwnerSubject, c.RunID, c.JobName, c.ErrorMessage, ttlOrDefault(c.TTLSeconds),
		boolToInt(c.Warm),
	)
	if err != nil {
		return fmt.Errorf("copy.Create %s: %w", c.ID, err)
	}
	return nil
}

// Get returns the copy with the given id, or sql.ErrNoRows if not found.
func (s *CopyStore) Get(id string) (*Copy, error) {
	row := s.db.QueryRow(`
		SELECT id, status, port, container_id, connection_string, owner_subject,
		       run_id, job_name, error_message,
		       created_at, ready_at, destroyed_at, ttl_seconds, warm
		FROM copies WHERE id = ?`, id)
	return scanCopy(row)
}

// UpdateStatus updates the status and optionally other fields for a copy.
func (s *CopyStore) UpdateStatus(id string, status CopyStatus, opts ...UpdateOption) error {
	u := &updateArgs{status: status}
	for _, o := range opts {
		o(u)
	}

	args := []any{string(status)}
	set := "status = ?"

	if u.containerID != nil {
		set += ", container_id = ?"
		args = append(args, *u.containerID)
	}
	if u.connectionString != nil {
		set += ", connection_string = ?"
		args = append(args, *u.connectionString)
	}
	if u.readyAt != nil {
		set += ", ready_at = ?"
		args = append(args, u.readyAt.UTC().Format(time.RFC3339Nano))
	}
	if u.destroyedAt != nil {
		set += ", destroyed_at = ?"
		args = append(args, u.destroyedAt.UTC().Format(time.RFC3339Nano))
	}
	if u.errorMessage != nil {
		set += ", error_message = ?"
		args = append(args, *u.errorMessage)
	}
	if u.runID != nil {
		set += ", run_id = ?"
		args = append(args, *u.runID)
	}
	if u.ownerSubject != nil {
		set += ", owner_subject = ?"
		args = append(args, *u.ownerSubject)
	}
	if u.jobName != nil {
		set += ", job_name = ?"
		args = append(args, *u.jobName)
	}

	args = append(args, id)
	_, err := s.db.Exec(`UPDATE copies SET `+set+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("copy.UpdateStatus %s: %w", id, err)
	}
	return nil
}

// List returns all copies matching filter. Ordered by created_at DESC.
func (s *CopyStore) List(filter ListFilter) ([]*Copy, error) {
	query := `
		SELECT id, status, port, container_id, connection_string, owner_subject,
		       run_id, job_name, error_message,
		       created_at, ready_at, destroyed_at, ttl_seconds, warm
		FROM copies`

	var (
		args       []any
		conditions []string
	)
	if len(filter.Statuses) > 0 {
		placeholders := "?"
		args = append(args, string(filter.Statuses[0]))
		for _, s := range filter.Statuses[1:] {
			placeholders += ", ?"
			args = append(args, string(s))
		}
		conditions = append(conditions, "status IN ("+placeholders+")")
	}
	if filter.OwnerSubject != "" {
		conditions = append(conditions, "owner_subject = ?")
		args = append(args, filter.OwnerSubject)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY created_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("copy.List: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var copies []*Copy
	for rows.Next() {
		c, err := scanCopyRow(rows)
		if err != nil {
			return nil, err
		}
		copies = append(copies, c)
	}
	return copies, rows.Err()
}

// ListExpired returns all READY or IN_USE non-warm copies whose TTL has elapsed.
func (s *CopyStore) ListExpired() ([]*Copy, error) {
	rows, err := s.db.Query(`
			SELECT id, status, port, container_id, connection_string,
			       owner_subject, run_id, job_name, error_message,
		       created_at, ready_at, destroyed_at, ttl_seconds, warm
		FROM copies
		WHERE status IN (?, ?)
		  AND warm = 0
		  AND datetime(COALESCE(ready_at, created_at), '+' || ttl_seconds || ' seconds') < datetime('now')
		ORDER BY created_at ASC`,
		string(StatusReady), string(StatusInUse))
	if err != nil {
		return nil, fmt.Errorf("copy.ListExpired: %w", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	var copies []*Copy
	for rows.Next() {
		c, err := scanCopyRow(rows)
		if err != nil {
			return nil, err
		}
		copies = append(copies, c)
	}
	return copies, rows.Err()
}

// ListStuck returns copies in CREATING or DESTROYING state (possible mid-crash
// remnants).
func (s *CopyStore) ListStuck() ([]*Copy, error) {
	return s.List(ListFilter{
		Statuses: []CopyStatus{StatusPending, StatusCreating, StatusDestroying},
	})
}

// ClaimWarm atomically finds one StatusReady warm copy, resets its created_at
// to NOW (restarting the TTL clock from claim time), marks warm=0, and returns
// it. Returns sql.ErrNoRows if the pool is empty.
func (s *CopyStore) ClaimWarm(ttlSeconds int, ownerSubject string) (*Copy, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("copy.ClaimWarm begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id string
	err = tx.QueryRow(
		`SELECT id FROM copies WHERE warm=1 AND status=? LIMIT 1`,
		string(StatusReady),
	).Scan(&id)
	if err != nil {
		return nil, err // sql.ErrNoRows if pool empty
	}

	_, err = tx.Exec(`
			UPDATE copies
			SET warm=0,
			    ttl_seconds=?,
			    owner_subject=?,
			    created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
			    ready_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE id=?`,
		ttlOrDefault(ttlSeconds), ownerSubject, id,
	)
	if err != nil {
		return nil, fmt.Errorf("copy.ClaimWarm update %s: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("copy.ClaimWarm commit: %w", err)
	}

	return s.Get(id)
}

// CountWarm returns the number of ready warm copies currently in the pool.
func (s *CopyStore) CountWarm() (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM copies WHERE warm=1 AND status=?`,
		string(StatusReady),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("copy.CountWarm: %w", err)
	}
	return n, nil
}

// ListFilter controls which copies are returned by List.
type ListFilter struct {
	Statuses     []CopyStatus
	OwnerSubject string
}

// UpdateOption is a functional option for UpdateStatus.
type UpdateOption func(*updateArgs)

type updateArgs struct {
	status           CopyStatus
	containerID      *string
	connectionString *string
	readyAt          *time.Time
	destroyedAt      *time.Time
	errorMessage     *string
	ownerSubject     *string
	runID            *string
	jobName          *string
}

func WithContainerID(id string) UpdateOption {
	return func(u *updateArgs) { u.containerID = &id }
}

func WithConnectionString(cs string) UpdateOption {
	return func(u *updateArgs) { u.connectionString = &cs }
}

func WithReadyAt(t time.Time) UpdateOption {
	return func(u *updateArgs) { u.readyAt = &t }
}

func WithDestroyedAt(t time.Time) UpdateOption {
	return func(u *updateArgs) { u.destroyedAt = &t }
}

func WithErrorMessage(msg string) UpdateOption {
	return func(u *updateArgs) { u.errorMessage = &msg }
}

func WithRunID(s string) UpdateOption {
	return func(u *updateArgs) { u.runID = &s }
}

func WithOwnerSubject(s string) UpdateOption {
	return func(u *updateArgs) { u.ownerSubject = &s }
}

func WithJobName(s string) UpdateOption {
	return func(u *updateArgs) { u.jobName = &s }
}

func ttlOrDefault(ttl int) int {
	if ttl == 0 {
		return 7200
	}
	return ttl
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// scanRow scans the standard 13-column copy result into a Copy struct.
// All text columns that allow NULL are scanned into sql.NullString.
func scanRow(scan func(...any) error) (*Copy, error) {
	var c Copy
	var status string
	var containerID, connectionString, ownerSubject, runID, jobName, errorMessage sql.NullString
	var createdAt string
	var readyAt, destroyedAt sql.NullString
	var warm int

	err := scan(
		&c.ID, &status, &c.Port,
		&containerID, &connectionString, &ownerSubject,
		&runID, &jobName, &errorMessage,
		&createdAt, &readyAt, &destroyedAt, &c.TTLSeconds,
		&warm,
	)
	if err != nil {
		return nil, err
	}
	c.Status = CopyStatus(status)
	c.ContainerID = containerID.String
	c.ConnectionString = connectionString.String
	c.OwnerSubject = ownerSubject.String
	c.RunID = runID.String
	c.JobName = jobName.String
	c.ErrorMessage = errorMessage.String
	c.CreatedAt = parseTime(createdAt)
	c.Warm = warm == 1
	if readyAt.Valid {
		t := parseTime(readyAt.String)
		c.ReadyAt = &t
	}
	if destroyedAt.Valid {
		t := parseTime(destroyedAt.String)
		c.DestroyedAt = &t
	}
	return &c, nil
}

// scanCopy scans a single *sql.Row.
func scanCopy(row *sql.Row) (*Copy, error) {
	return scanRow(row.Scan)
}

// scanCopyRow scans a row from *sql.Rows.
func scanCopyRow(rows *sql.Rows) (*Copy, error) {
	return scanRow(rows.Scan)
}

func parseTime(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
