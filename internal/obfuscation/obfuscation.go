// Package obfuscation applies post-restore PII scrubbing rules to a database
// copy. It connects directly to the copy container using the engine's DSN and
// runs engine-specific UPDATE statements for each rule.
//
// Supported strategies:
//   - nullify  — SET col = NULL
//   - redact   — SET col = '[redacted]' (configurable via Rule.With)
//   - mask     — replace characters with '*' (configurable mask_char / keep_last)
//   - hash     — one-way SHA-256 hex digest; preserves uniqueness for JOINs
//   - replace  — deterministic format-preserving substitution; Rule.Type selects
//                the data shape: email, name, phone, ip, url, uuid
package obfuscation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/attaradev/ditto/internal/config"
)

// Obfuscator applies a set of rules to a single database copy.
type Obfuscator struct {
	engineName string
	dsn        string
	rules      []config.ObfuscationRule
}

// New creates an Obfuscator. engineName must be "postgres" or "mysql".
// dsn is the connection string returned by engine.ConnectionString("localhost", port).
func New(engineName, dsn string, rules []config.ObfuscationRule) *Obfuscator {
	return &Obfuscator{engineName: engineName, dsn: dsn, rules: rules}
}

// Apply opens a short-lived connection to the copy and runs all UPDATE
// statements in sequence. Returns immediately if rules is empty.
//
// The drivers (pgx and go-sql-driver/mysql) are registered by the engine
// package init() functions via blank imports in the main binary; no import of
// those packages is needed here.
func (o *Obfuscator) Apply(ctx context.Context) error {
	if len(o.rules) == 0 {
		return nil
	}

	driver, err := driverName(o.engineName)
	if err != nil {
		return err
	}

	db, err := sql.Open(driver, o.dsn)
	if err != nil {
		return fmt.Errorf("obfuscation: open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	for _, rule := range o.rules {
		stmt, args, err := buildSQL(o.engineName, rule)
		if err != nil {
			return fmt.Errorf("obfuscation: build SQL for %s.%s: %w", rule.Table, rule.Column, err)
		}
		if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
			return fmt.Errorf("obfuscation: apply %s on %s.%s: %w",
				rule.Strategy, rule.Table, rule.Column, err)
		}
	}
	return nil
}

// BuildSQL is exported for testing. It returns the engine-specific UPDATE
// statement and any bind-parameter arguments for the given rule.
func BuildSQL(engineName string, rule config.ObfuscationRule) (stmt string, args []any, err error) {
	return buildSQL(engineName, rule)
}

// --- internal helpers ---

func driverName(engineName string) (string, error) {
	switch engineName {
	case "postgres":
		return "pgx", nil
	case "mysql":
		return "mysql", nil
	default:
		return "", fmt.Errorf("obfuscation: unsupported engine %q", engineName)
	}
}

// buildSQL generates an engine-specific UPDATE statement for a single rule.
func buildSQL(eng string, r config.ObfuscationRule) (string, []any, error) {
	t := quoteIdent(eng, r.Table)
	c := quoteIdent(eng, r.Column)

	switch r.Strategy {
	case "nullify":
		return fmt.Sprintf("UPDATE %s SET %s = NULL", t, c), nil, nil

	case "redact":
		replacement := r.With
		if replacement == "" {
			replacement = "[redacted]"
		}
		return fmt.Sprintf("UPDATE %s SET %s = %s", t, c, ph(eng, 1)),
			[]any{replacement}, nil

	case "mask":
		return maskSQL(eng, t, c, r), nil, nil

	case "hash":
		return hashSQL(eng, t, c), nil, nil

	case "replace":
		stmt, err := replaceSQL(eng, t, c, r.Type)
		return stmt, nil, err

	default:
		return "", nil, fmt.Errorf("unknown strategy %q", r.Strategy)
	}
}

// maskSQL generates a REPEAT-based mask expression. Uses GREATEST to guard
// against negative lengths when KeepLast exceeds the actual string length.
func maskSQL(eng, t, c string, r config.ObfuscationRule) string {
	maskChar := r.MaskChar
	if maskChar == "" {
		maskChar = "*"
	}
	lit := sqlStringLiteral(maskChar)

	switch eng {
	case "mysql":
		if r.KeepLast > 0 {
			return fmt.Sprintf(
				"UPDATE %s SET %s = CONCAT(REPEAT(%s, GREATEST(0, CHAR_LENGTH(%s) - %d)), RIGHT(%s, %d))",
				t, c, lit, c, r.KeepLast, c, r.KeepLast,
			)
		}
		return fmt.Sprintf("UPDATE %s SET %s = REPEAT(%s, CHAR_LENGTH(%s))", t, c, lit, c)

	default: // postgres
		if r.KeepLast > 0 {
			return fmt.Sprintf(
				"UPDATE %s SET %s = REPEAT(%s, GREATEST(0, length(%s::text) - %d)) || right(%s::text, %d)",
				t, c, lit, c, r.KeepLast, c, r.KeepLast,
			)
		}
		return fmt.Sprintf("UPDATE %s SET %s = REPEAT(%s, length(%s::text))", t, c, lit, c)
	}
}

// replaceSQL generates a deterministic format-preserving substitution expression.
// The output looks like real data of the given type but is derived from a hash
// of the original value, so the same input always maps to the same output and
// referential integrity across tables is preserved.
//
// All generated values use clearly fictional domains/ranges (example.com,
// +1-555-01xx, 10.x.x.x) so they cannot be mistaken for real PII.
func replaceSQL(eng, t, c, dataType string) (string, error) {
	switch eng {
	case "mysql":
		return replaceSQLMySQL(t, c, dataType)
	default:
		return replaceSQLPostgres(t, c, dataType)
	}
}

func replaceSQLPostgres(t, c, dataType string) (string, error) {
	// h32: abs(hashtext(col::text)) — cheap 32-bit hash, good for short values
	h := fmt.Sprintf("abs(hashtext(%s::text))", c)
	// h64: 64-bit hex from sha256 — used for uuid / url where more bits matter
	h64 := fmt.Sprintf("encode(sha256(%s::bytea),'hex')", c)

	var expr string
	switch dataType {
	case "email":
		// user123456@example.com — N is 0–999999, always unique enough in practice
		expr = fmt.Sprintf("'user' || (%s %% 1000000) || '@example.com'", h)
	case "name":
		// User12345 — simple, obviously synthetic
		expr = fmt.Sprintf("'User' || (%s %% 100000)", h)
	case "phone":
		// +1-555-01xx-xxxx — NANP 555-01xx range is reserved for fiction
		expr = fmt.Sprintf(
			"'+1-555-01' || lpad((%s %% 99)::text, 2, '0') || '-' || lpad(((%s >> 8) %% 9000 + 1000)::text, 4, '0')",
			h, h,
		)
	case "ip":
		// 10.x.x.x — RFC 1918 private range, never routes on the public internet
		expr = fmt.Sprintf(
			"'10.' || (%s %% 256)::text || '.' || ((%s >> 8) %% 256)::text || '.' || ((%s >> 16) %% 256)::text",
			h, h, h,
		)
	case "url":
		// https://example.com/r/<12-char hex> — example.com is IANA reserved
		expr = fmt.Sprintf("'https://example.com/r/' || left(%s, 12)", h64)
	case "uuid":
		// UUID v4-shaped value derived from md5 of the original
		expr = fmt.Sprintf("md5(%s::text)::uuid", c)
	default:
		return "", fmt.Errorf("unknown replace type %q", dataType)
	}
	return fmt.Sprintf("UPDATE %s SET %s = %s", t, c, expr), nil
}

func replaceSQLMySQL(t, c, dataType string) (string, error) {
	// h: ABS(CRC32(col)) — fast 32-bit hash
	h := fmt.Sprintf("ABS(CRC32(%s))", c)

	var expr string
	switch dataType {
	case "email":
		expr = fmt.Sprintf("CONCAT('user', %s %% 1000000, '@example.com')", h)
	case "name":
		expr = fmt.Sprintf("CONCAT('User', %s %% 100000)", h)
	case "phone":
		// +1-555-01xx-xxxx
		expr = fmt.Sprintf(
			"CONCAT('+1-555-01', LPAD(%s %% 99, 2, '0'), '-', LPAD(%s >> 8 %% 9000 + 1000, 4, '0'))",
			h, h,
		)
	case "ip":
		expr = fmt.Sprintf(
			"CONCAT('10.', %s %% 256, '.', %s >> 8 %% 256, '.', %s >> 16 %% 256)",
			h, h, h,
		)
	case "url":
		expr = fmt.Sprintf("CONCAT('https://example.com/r/', LEFT(MD5(%s), 12))", c)
	case "uuid":
		// UUID v4-shaped hex from MD5
		expr = fmt.Sprintf(
			"LOWER(CONCAT(LEFT(MD5(%s),8),'-',MID(MD5(%s),9,4),'-4',MID(MD5(%s),13,3),'-',MID(MD5(%s),17,4),'-',RIGHT(MD5(%s),12)))",
			c, c, c, c, c,
		)
	default:
		return "", fmt.Errorf("unknown replace type %q", dataType)
	}
	return fmt.Sprintf("UPDATE %s SET %s = %s", t, c, expr), nil
}

// hashSQL generates a one-way SHA-256 hex digest expression.
func hashSQL(eng, t, c string) string {
	switch eng {
	case "mysql":
		return fmt.Sprintf("UPDATE %s SET %s = SHA2(%s, 256)", t, c, c)
	default: // postgres
		return fmt.Sprintf("UPDATE %s SET %s = encode(sha256(%s::bytea), 'hex')", t, c, c)
	}
}

// quoteIdent double-quotes (postgres) or backtick-quotes (mysql) an identifier,
// escaping any embedded quote characters.
func quoteIdent(eng, name string) string {
	switch eng {
	case "mysql":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

// ph returns a positional placeholder: $N for postgres, ? for mysql.
func ph(eng string, pos int) string {
	if eng == "postgres" {
		return fmt.Sprintf("$%d", pos)
	}
	return "?"
}

// sqlStringLiteral wraps s in single quotes, escaping any embedded single quotes.
func sqlStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
