package store

import (
	"database/sql"
	"fmt"
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
	ID               string
	Status           CopyStatus
	Port             int
	ContainerID      string
	ConnectionString string
	GHARunID         string
	GHAJobName       string
	ErrorMessage     string
	CreatedAt        time.Time
	ReadyAt          *time.Time
	DestroyedAt      *time.Time
	TTLSeconds       int
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
			 gha_run_id, gha_job_name, error_message, ttl_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Status, c.Port, c.ContainerID, c.ConnectionString,
		c.GHARunID, c.GHAJobName, c.ErrorMessage, ttlOrDefault(c.TTLSeconds),
	)
	if err != nil {
		return fmt.Errorf("copy.Create %s: %w", c.ID, err)
	}
	return nil
}

// Get returns the copy with the given id, or sql.ErrNoRows if not found.
func (s *CopyStore) Get(id string) (*Copy, error) {
	row := s.db.QueryRow(`
		SELECT id, status, port, container_id, connection_string,
		       gha_run_id, gha_job_name, error_message,
		       created_at, ready_at, destroyed_at, ttl_seconds
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
		SELECT id, status, port, container_id, connection_string,
		       gha_run_id, gha_job_name, error_message,
		       created_at, ready_at, destroyed_at, ttl_seconds
		FROM copies`

	var args []any
	if len(filter.Statuses) > 0 {
		placeholders := "?"
		args = append(args, string(filter.Statuses[0]))
		for _, s := range filter.Statuses[1:] {
			placeholders += ", ?"
			args = append(args, string(s))
		}
		query += " WHERE status IN (" + placeholders + ")"
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

// ListExpired returns all READY or IN_USE copies whose TTL has elapsed.
func (s *CopyStore) ListExpired() ([]*Copy, error) {
	rows, err := s.db.Query(`
		SELECT id, status, port, container_id, connection_string,
		       gha_run_id, gha_job_name, error_message,
		       created_at, ready_at, destroyed_at, ttl_seconds
		FROM copies
		WHERE status IN ('ready', 'in_use')
		  AND datetime(created_at, '+' || ttl_seconds || ' seconds') < datetime('now')
		ORDER BY created_at ASC`)
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
		Statuses: []CopyStatus{StatusCreating, StatusDestroying},
	})
}

// ListFilter controls which copies are returned by List.
type ListFilter struct {
	Statuses []CopyStatus
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

func ttlOrDefault(ttl int) int {
	if ttl == 0 {
		return 7200
	}
	return ttl
}

// scanRow scans the standard 12-column copy result into a Copy struct.
// All text columns that allow NULL are scanned into sql.NullString.
func scanRow(scan func(...any) error) (*Copy, error) {
	var c Copy
	var status string
	var containerID, connectionString, ghaRunID, ghaJobName, errorMessage sql.NullString
	var createdAt string
	var readyAt, destroyedAt sql.NullString

	err := scan(
		&c.ID, &status, &c.Port,
		&containerID, &connectionString,
		&ghaRunID, &ghaJobName, &errorMessage,
		&createdAt, &readyAt, &destroyedAt, &c.TTLSeconds,
	)
	if err != nil {
		return nil, err
	}
	c.Status = CopyStatus(status)
	c.ContainerID = containerID.String
	c.ConnectionString = connectionString.String
	c.GHARunID = ghaRunID.String
	c.GHAJobName = ghaJobName.String
	c.ErrorMessage = errorMessage.String
	c.CreatedAt = parseTime(createdAt)
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
