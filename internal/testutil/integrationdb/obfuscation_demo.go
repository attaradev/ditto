//go:build integration

package integrationdb

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/attaradev/ditto/internal/config"
)

type Snapshot struct {
	Users          []UserRow
	PaymentMethods []PaymentMethodRow
	AuditLogs      []AuditLogRow
}

type UserRow struct {
	ID          int64
	Role        string
	Email       string
	FullName    string
	Phone       string
	SSN         *string
	Notes       string
	APIKey      string
	AccountUUID string
}

type PaymentMethodRow struct {
	ID           int64
	UserID       int64
	Brand        string
	CardNumber   string
	BillingEmail string
}

type AuditLogRow struct {
	ID        int64
	UserID    int64
	Action    string
	IPAddress string
	TargetURL string
	ActorUUID string
}

var (
	emailPattern = regexp.MustCompile(`^user\d+@example\.com$`)
	namePattern  = regexp.MustCompile(`^User\d+$`)
	phonePattern = regexp.MustCompile(`^\+1-555-01\d{2}-\d{4}$`)
	ipPattern    = regexp.MustCompile(`^10\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$`)
	urlPattern   = regexp.MustCompile(`^https://example\.com/r/[0-9a-f]{12}$`)
	uuidPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hashPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ObfuscationRules returns the canonical end-to-end demo rule set.
func ObfuscationRules() []config.ObfuscationRule {
	return []config.ObfuscationRule{
		{Table: "users", Column: "email", Strategy: "replace", Type: "email"},
		{Table: "users", Column: "full_name", Strategy: "replace", Type: "name"},
		{Table: "users", Column: "phone", Strategy: "replace", Type: "phone"},
		{Table: "users", Column: "ssn", Strategy: "nullify"},
		{Table: "users", Column: "notes", Strategy: "redact"},
		{Table: "users", Column: "api_key", Strategy: "hash"},
		{Table: "users", Column: "account_uuid", Strategy: "replace", Type: "uuid"},
		{Table: "payment_methods", Column: "card_number", Strategy: "mask", KeepLast: 4, MaskChar: "*"},
		{Table: "payment_methods", Column: "billing_email", Strategy: "replace", Type: "email"},
		{Table: "audit_logs", Column: "ip_address", Strategy: "replace", Type: "ip"},
		{Table: "audit_logs", Column: "target_url", Strategy: "replace", Type: "url"},
		{Table: "audit_logs", Column: "actor_uuid", Strategy: "replace", Type: "uuid"},
	}
}

// ObfuscationRulesWithWarnOnlyProbe appends an empty-table rule used to verify
// warn_only behavior without changing the main fixture schema.
func ObfuscationRulesWithWarnOnlyProbe(warnOnly bool) []config.ObfuscationRule {
	rules := append([]config.ObfuscationRule{}, ObfuscationRules()...)
	rules = append(rules, config.ObfuscationRule{
		Table:    "archived_customers",
		Column:   "email",
		Strategy: "redact",
		WarnOnly: warnOnly,
	})
	return rules
}

// SeedObfuscationDemo creates the canonical schema and inserts synthetic PII.
func SeedObfuscationDemo(t *testing.T, engineName, dsn string) Snapshot {
	t.Helper()

	db := OpenDB(t, engineName, dsn)
	for _, stmt := range schemaStatements(engineName) {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("seed schema (%s): %v", engineName, err)
		}
	}
	for _, stmt := range seedStatements() {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("seed data (%s): %v", engineName, err)
		}
	}
	return SnapshotObfuscationDemo(t, engineName, dsn)
}

// SnapshotObfuscationDemo reads the canonical fixture tables in stable order.
func SnapshotObfuscationDemo(t *testing.T, engineName, dsn string) Snapshot {
	t.Helper()

	db := OpenDB(t, engineName, dsn)
	return Snapshot{
		Users:          queryUsers(t, db),
		PaymentMethods: queryPaymentMethods(t, db),
		AuditLogs:      queryAuditLogs(t, db),
	}
}

// AssertRawSnapshot verifies the un-obfuscated fixture matches the canonical
// seed values exactly.
func AssertRawSnapshot(t *testing.T, got Snapshot) {
	t.Helper()

	want := CanonicalRawSnapshot()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raw snapshot mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// AssertObfuscatedSnapshot verifies that all protected columns changed while
// the safe columns and relationships remained intact.
func AssertObfuscatedSnapshot(t *testing.T, raw, got Snapshot) {
	t.Helper()

	if len(got.Users) != len(raw.Users) {
		t.Fatalf("users length: got %d, want %d", len(got.Users), len(raw.Users))
	}
	if len(got.PaymentMethods) != len(raw.PaymentMethods) {
		t.Fatalf("payment_methods length: got %d, want %d", len(got.PaymentMethods), len(raw.PaymentMethods))
	}
	if len(got.AuditLogs) != len(raw.AuditLogs) {
		t.Fatalf("audit_logs length: got %d, want %d", len(got.AuditLogs), len(raw.AuditLogs))
	}

	emailMap := make(map[string]string, len(raw.Users))
	uuidMap := make(map[string]string, len(raw.Users))
	hashMap := make(map[string]string, len(raw.Users))

	for i := range raw.Users {
		want := raw.Users[i]
		gotUser := got.Users[i]

		if gotUser.ID != want.ID {
			t.Errorf("users[%d].id: got %d, want %d", i, gotUser.ID, want.ID)
		}
		if gotUser.Role != want.Role {
			t.Errorf("users[%d].role: got %q, want %q", i, gotUser.Role, want.Role)
		}
		assertChangedMatch(t, fmt.Sprintf("users[%d].email", i), gotUser.Email, want.Email, emailPattern)
		assertChangedMatch(t, fmt.Sprintf("users[%d].full_name", i), gotUser.FullName, want.FullName, namePattern)
		assertChangedMatch(t, fmt.Sprintf("users[%d].phone", i), gotUser.Phone, want.Phone, phonePattern)
		if gotUser.SSN != nil {
			t.Errorf("users[%d].ssn: got %q, want NULL", i, *gotUser.SSN)
		}
		if gotUser.Notes != "[redacted]" {
			t.Errorf("users[%d].notes: got %q, want %q", i, gotUser.Notes, "[redacted]")
		}
		assertChangedMatch(t, fmt.Sprintf("users[%d].api_key", i), gotUser.APIKey, want.APIKey, hashPattern)
		assertChangedMatch(t, fmt.Sprintf("users[%d].account_uuid", i), gotUser.AccountUUID, want.AccountUUID, uuidPattern)

		assertDeterministic(t, "users.api_key", hashMap, want.APIKey, gotUser.APIKey)
		assertDeterministic(t, "users.email", emailMap, want.Email, gotUser.Email)
		assertDeterministic(t, "users.account_uuid", uuidMap, want.AccountUUID, gotUser.AccountUUID)
	}

	for i := range raw.PaymentMethods {
		want := raw.PaymentMethods[i]
		gotMethod := got.PaymentMethods[i]

		if gotMethod.ID != want.ID {
			t.Errorf("payment_methods[%d].id: got %d, want %d", i, gotMethod.ID, want.ID)
		}
		if gotMethod.UserID != want.UserID {
			t.Errorf("payment_methods[%d].user_id: got %d, want %d", i, gotMethod.UserID, want.UserID)
		}
		if gotMethod.Brand != want.Brand {
			t.Errorf("payment_methods[%d].brand: got %q, want %q", i, gotMethod.Brand, want.Brand)
		}
		assertMaskedCard(t, fmt.Sprintf("payment_methods[%d].card_number", i), gotMethod.CardNumber, want.CardNumber)
		assertChangedMatch(t, fmt.Sprintf("payment_methods[%d].billing_email", i), gotMethod.BillingEmail, want.BillingEmail, emailPattern)
		if mapped := emailMap[want.BillingEmail]; mapped != "" && gotMethod.BillingEmail != mapped {
			t.Errorf("payment_methods[%d].billing_email: got %q, want %q to match users.email mapping", i, gotMethod.BillingEmail, mapped)
		}
	}

	for i := range raw.AuditLogs {
		want := raw.AuditLogs[i]
		gotLog := got.AuditLogs[i]

		if gotLog.ID != want.ID {
			t.Errorf("audit_logs[%d].id: got %d, want %d", i, gotLog.ID, want.ID)
		}
		if gotLog.UserID != want.UserID {
			t.Errorf("audit_logs[%d].user_id: got %d, want %d", i, gotLog.UserID, want.UserID)
		}
		if gotLog.Action != want.Action {
			t.Errorf("audit_logs[%d].action: got %q, want %q", i, gotLog.Action, want.Action)
		}
		assertChangedMatch(t, fmt.Sprintf("audit_logs[%d].ip_address", i), gotLog.IPAddress, want.IPAddress, ipPattern)
		assertChangedMatch(t, fmt.Sprintf("audit_logs[%d].target_url", i), gotLog.TargetURL, want.TargetURL, urlPattern)
		assertChangedMatch(t, fmt.Sprintf("audit_logs[%d].actor_uuid", i), gotLog.ActorUUID, want.ActorUUID, uuidPattern)
		if mapped := uuidMap[want.ActorUUID]; mapped != "" && gotLog.ActorUUID != mapped {
			t.Errorf("audit_logs[%d].actor_uuid: got %q, want %q to match users.account_uuid mapping", i, gotLog.ActorUUID, mapped)
		}
	}
}

// CanonicalRawSnapshot returns the exact seeded values used by the demo.
func CanonicalRawSnapshot() Snapshot {
	return Snapshot{
		Users: []UserRow{
			{
				ID:          1,
				Role:        "admin",
				Email:       "alice@example.org",
				FullName:    "Alice Example",
				Phone:       "+1-415-555-0101",
				SSN:         strPtr("111-22-3333"),
				Notes:       "Priority account",
				APIKey:      "shared-api-key",
				AccountUUID: "11111111-1111-1111-1111-111111111111",
			},
			{
				ID:          2,
				Role:        "analyst",
				Email:       "bob@example.org",
				FullName:    "Bob Example",
				Phone:       "+1-415-555-0102",
				SSN:         strPtr("222-33-4444"),
				Notes:       "Needs review",
				APIKey:      "shared-api-key",
				AccountUUID: "22222222-2222-2222-2222-222222222222",
			},
			{
				ID:          3,
				Role:        "viewer",
				Email:       "carol@example.org",
				FullName:    "Carol Example",
				Phone:       "+1-415-555-0103",
				SSN:         strPtr("333-44-5555"),
				Notes:       "Left voicemail",
				APIKey:      "unique-api-key",
				AccountUUID: "33333333-3333-3333-3333-333333333333",
			},
		},
		PaymentMethods: []PaymentMethodRow{
			{ID: 10, UserID: 1, Brand: "visa", CardNumber: "4111111111111111", BillingEmail: "alice@example.org"},
			{ID: 11, UserID: 2, Brand: "mastercard", CardNumber: "5555555555554444", BillingEmail: "bob@example.org"},
			{ID: 12, UserID: 3, Brand: "amex", CardNumber: "378282246310005", BillingEmail: "alice@example.org"},
		},
		AuditLogs: []AuditLogRow{
			{ID: 20, UserID: 1, Action: "login", IPAddress: "203.0.113.10", TargetURL: "https://app.example.org/account", ActorUUID: "11111111-1111-1111-1111-111111111111"},
			{ID: 21, UserID: 2, Action: "purchase", IPAddress: "198.51.100.24", TargetURL: "https://pay.example.org/checkout", ActorUUID: "22222222-2222-2222-2222-222222222222"},
			{ID: 22, UserID: 3, Action: "support", IPAddress: "192.0.2.42", TargetURL: "https://support.example.org/case/42", ActorUUID: "33333333-3333-3333-3333-333333333333"},
		},
	}
}

func schemaStatements(engineName string) []string {
	uuidType := "UUID"
	if engineName == EngineMySQL {
		uuidType = "CHAR(36)"
	}

	return []string{
		fmt.Sprintf(`CREATE TABLE users (
			id BIGINT PRIMARY KEY,
			role VARCHAR(32) NOT NULL,
			email VARCHAR(255) NOT NULL,
			full_name VARCHAR(255) NOT NULL,
			phone VARCHAR(32) NOT NULL,
			ssn VARCHAR(32),
			notes TEXT NOT NULL,
			api_key VARCHAR(255) NOT NULL,
			account_uuid %s NOT NULL
		)`, uuidType),
		`CREATE TABLE payment_methods (
			id BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL,
			brand VARCHAR(32) NOT NULL,
			card_number VARCHAR(32) NOT NULL,
			billing_email VARCHAR(255) NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id)
		)`,
		fmt.Sprintf(`CREATE TABLE audit_logs (
			id BIGINT PRIMARY KEY,
			user_id BIGINT NOT NULL,
			action VARCHAR(64) NOT NULL,
			ip_address VARCHAR(64) NOT NULL,
			target_url TEXT NOT NULL,
			actor_uuid %s NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id)
		)`, uuidType),
		`CREATE TABLE archived_customers (
			id BIGINT PRIMARY KEY,
			email VARCHAR(255) NOT NULL
		)`,
	}
}

func seedStatements() []string {
	return []string{
		`INSERT INTO users (id, role, email, full_name, phone, ssn, notes, api_key, account_uuid) VALUES
			(1, 'admin', 'alice@example.org', 'Alice Example', '+1-415-555-0101', '111-22-3333', 'Priority account', 'shared-api-key', '11111111-1111-1111-1111-111111111111'),
			(2, 'analyst', 'bob@example.org', 'Bob Example', '+1-415-555-0102', '222-33-4444', 'Needs review', 'shared-api-key', '22222222-2222-2222-2222-222222222222'),
			(3, 'viewer', 'carol@example.org', 'Carol Example', '+1-415-555-0103', '333-44-5555', 'Left voicemail', 'unique-api-key', '33333333-3333-3333-3333-333333333333')`,
		`INSERT INTO payment_methods (id, user_id, brand, card_number, billing_email) VALUES
			(10, 1, 'visa', '4111111111111111', 'alice@example.org'),
			(11, 2, 'mastercard', '5555555555554444', 'bob@example.org'),
			(12, 3, 'amex', '378282246310005', 'alice@example.org')`,
		`INSERT INTO audit_logs (id, user_id, action, ip_address, target_url, actor_uuid) VALUES
			(20, 1, 'login', '203.0.113.10', 'https://app.example.org/account', '11111111-1111-1111-1111-111111111111'),
			(21, 2, 'purchase', '198.51.100.24', 'https://pay.example.org/checkout', '22222222-2222-2222-2222-222222222222'),
			(22, 3, 'support', '192.0.2.42', 'https://support.example.org/case/42', '33333333-3333-3333-3333-333333333333')`,
	}
}

func queryUsers(t *testing.T, db *sql.DB) []UserRow {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), `
		SELECT id, role, email, full_name, phone, ssn, notes, api_key, account_uuid
		FROM users
		ORDER BY id`)
	if err != nil {
		t.Fatalf("query users: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []UserRow
	for rows.Next() {
		var row UserRow
		var ssn sql.NullString
		if err := rows.Scan(&row.ID, &row.Role, &row.Email, &row.FullName, &row.Phone, &ssn, &row.Notes, &row.APIKey, &row.AccountUUID); err != nil {
			t.Fatalf("scan users: %v", err)
		}
		if ssn.Valid {
			row.SSN = strPtr(ssn.String)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate users: %v", err)
	}
	return out
}

func queryPaymentMethods(t *testing.T, db *sql.DB) []PaymentMethodRow {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), `
		SELECT id, user_id, brand, card_number, billing_email
		FROM payment_methods
		ORDER BY id`)
	if err != nil {
		t.Fatalf("query payment_methods: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []PaymentMethodRow
	for rows.Next() {
		var row PaymentMethodRow
		if err := rows.Scan(&row.ID, &row.UserID, &row.Brand, &row.CardNumber, &row.BillingEmail); err != nil {
			t.Fatalf("scan payment_methods: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate payment_methods: %v", err)
	}
	return out
}

func queryAuditLogs(t *testing.T, db *sql.DB) []AuditLogRow {
	t.Helper()

	rows, err := db.QueryContext(context.Background(), `
		SELECT id, user_id, action, ip_address, target_url, actor_uuid
		FROM audit_logs
		ORDER BY id`)
	if err != nil {
		t.Fatalf("query audit_logs: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var out []AuditLogRow
	for rows.Next() {
		var row AuditLogRow
		if err := rows.Scan(&row.ID, &row.UserID, &row.Action, &row.IPAddress, &row.TargetURL, &row.ActorUUID); err != nil {
			t.Fatalf("scan audit_logs: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_logs: %v", err)
	}
	return out
}

func assertChangedMatch(t *testing.T, field, got, raw string, pattern *regexp.Regexp) {
	t.Helper()

	if got == raw {
		t.Errorf("%s: value was not obfuscated (%q)", field, got)
	}
	if !pattern.MatchString(got) {
		t.Errorf("%s: got %q, pattern %q", field, got, pattern.String())
	}
}

func assertDeterministic(t *testing.T, field string, seen map[string]string, raw, got string) {
	t.Helper()

	if prev, ok := seen[raw]; ok && prev != got {
		t.Errorf("%s deterministic mapping: raw %q mapped to both %q and %q", field, raw, prev, got)
		return
	}
	seen[raw] = got
}

func assertMaskedCard(t *testing.T, field, got, raw string) {
	t.Helper()

	if got == raw {
		t.Errorf("%s: value was not masked (%q)", field, got)
		return
	}
	if len(raw) <= 4 {
		t.Errorf("%s: raw value %q too short for keep_last validation", field, raw)
		return
	}
	want := strings.Repeat("*", len(raw)-4) + raw[len(raw)-4:]
	if got != want {
		t.Errorf("%s: got %q, want %q", field, got, want)
	}
}

func strPtr(v string) *string { return &v }
