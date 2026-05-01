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
//     the data shape: email, name, phone, ip, url, uuid
package obfuscation

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/attaradev/ditto/internal/config"
)

// Obfuscator applies a set of rules to a single database copy.
type Obfuscator struct {
	engineName string
	dsn        string
	rules      []config.ObfuscationRule
}

// sqlTarget bundles the engine and quoted table/column identifiers used by
// SQL-builder helpers.
type sqlTarget struct {
	engine string
	table  string
	column string
}

func newSQLTarget(engine string, rule config.ObfuscationRule) sqlTarget {
	return sqlTarget{
		engine: engine,
		table:  quoteIdent(engine, rule.Table),
		column: quoteIdent(engine, rule.Column),
	}
}

// replaceContext carries all values needed to build deterministic replace SQL
// expressions for a single target column.
type replaceContext struct {
	column   string
	dataType string
	h32      string
	h64      string
}

func newReplaceContext(target sqlTarget, dataType string) replaceContext {
	return replaceContext{
		column:   target.column,
		dataType: dataType,
		h32:      fmt.Sprintf("abs(hashtext(%s::text))", target.column),
		h64:      fmt.Sprintf("encode(sha256(%s::bytea),'hex')", target.column),
	}
}

func newMySQLReplaceContext(target sqlTarget, dataType string) replaceContext {
	return replaceContext{
		column:   target.column,
		dataType: dataType,
		h32:      fmt.Sprintf("ABS(CRC32(%s))", target.column),
	}
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

	db, err := o.openDB()
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	return o.applyRules(ctx, db)
}

// openDB opens a database connection using the correct driver for this obfuscator's engine.
func (o *Obfuscator) openDB() (*sql.DB, error) {
	driver, err := DriverName(o.engineName)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driver, o.dsn)
	if err != nil {
		return nil, fmt.Errorf("obfuscation: open db: %w", err)
	}
	return db, nil
}

// applyRules executes all rules in sequence, returning the first error encountered
// or a warn-only message if a rule matches zero rows.
func (o *Obfuscator) applyRules(ctx context.Context, db *sql.DB) error {
	for _, rule := range o.rules {
		if err := o.applyRule(ctx, db, rule); err != nil {
			return err
		}
	}
	return nil
}

// applyRule executes a single obfuscation rule and validates that it matched at least one row.
func (o *Obfuscator) applyRule(ctx context.Context, db *sql.DB, rule config.ObfuscationRule) error {
	stmt, args, err := buildSQL(o.engineName, rule)
	if err != nil {
		return fmt.Errorf("obfuscation: build SQL for %s.%s: %w", rule.Table, rule.Column, err)
	}

	result, err := db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return fmt.Errorf("obfuscation: apply %s on %s.%s: %w",
			rule.Strategy, rule.Table, rule.Column, err)
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		msg := fmt.Sprintf("obfuscation: rule %s.%s matched 0 rows — verify table and column names exist in source schema", rule.Table, rule.Column)
		if rule.WarnOnly {
			slog.Warn(msg)
		} else {
			return fmt.Errorf("%s", msg)
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

// DriverName maps an engine name to its registered database/sql driver name.
func DriverName(engineName string) (string, error) {
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
	target := newSQLTarget(eng, r)

	switch r.Strategy {
	case "nullify":
		return fmt.Sprintf("UPDATE %s SET %s = NULL", target.table, target.column), nil, nil

	case "redact":
		replacement := r.With
		if replacement == "" {
			replacement = "[redacted]"
		}
		return fmt.Sprintf("UPDATE %s SET %s = %s", target.table, target.column, ph(target.engine, 1)),
			[]any{replacement}, nil

	case "mask":
		return maskSQL(target, r), nil, nil

	case "hash":
		return hashSQL(target), nil, nil

	case "replace":
		stmt, err := replaceSQL(target, r.Type)
		return stmt, nil, err

	default:
		return "", nil, fmt.Errorf("unknown strategy %q", r.Strategy)
	}
}

// maskSQL generates a REPEAT-based mask expression. Uses GREATEST to guard
// against negative lengths when KeepLast exceeds the actual string length.
func maskSQL(target sqlTarget, r config.ObfuscationRule) string {
	maskChar := r.MaskChar
	if maskChar == "" {
		maskChar = "*"
	}
	lit := sqlStringLiteral(maskChar)

	switch target.engine {
	case "mysql":
		if r.KeepLast > 0 {
			return fmt.Sprintf(
				"UPDATE %s SET %s = CONCAT(REPEAT(%s, GREATEST(0, CHAR_LENGTH(%s) - %d)), RIGHT(%s, %d))",
				target.table, target.column, lit, target.column, r.KeepLast, target.column, r.KeepLast,
			)
		}
		return fmt.Sprintf("UPDATE %s SET %s = REPEAT(%s, CHAR_LENGTH(%s))", target.table, target.column, lit, target.column)

	default: // postgres
		if r.KeepLast > 0 {
			return fmt.Sprintf(
				"UPDATE %s SET %s = REPEAT(%s, GREATEST(0, length(%s::text) - %d)) || right(%s::text, %d)",
				target.table, target.column, lit, target.column, r.KeepLast, target.column, r.KeepLast,
			)
		}
		return fmt.Sprintf("UPDATE %s SET %s = REPEAT(%s, length(%s::text))", target.table, target.column, lit, target.column)
	}
}

// replaceSQL generates a deterministic format-preserving substitution expression.
// The output looks like real data of the given type but is derived from a hash
// of the original value, so the same input always maps to the same output and
// referential integrity across tables is preserved.
//
// All generated values use clearly fictional domains/ranges (example.com,
// +1-555-01xx, 10.x.x.x) so they cannot be mistaken for real PII.
func replaceSQL(target sqlTarget, dataType string) (string, error) {
	switch target.engine {
	case "mysql":
		return replaceSQLMySQL(target, dataType)
	default:
		return replaceSQLPostgres(target, dataType)
	}
}

func replaceSQLPostgres(target sqlTarget, dataType string) (string, error) {
	rctx := newReplaceContext(target, dataType)

	expr, err := postgresExprForType(rctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("UPDATE %s SET %s = %s", target.table, target.column, expr), nil
}

func postgresExprForType(rctx replaceContext) (string, error) {
	switch rctx.dataType {
	case "email":
		return fmt.Sprintf("'user' || (%s %% 1000000) || '@example.com'", rctx.h32), nil
	case "name":
		return fmt.Sprintf("'User' || (%s %% 100000)", rctx.h32), nil
	case "phone":
		return fmt.Sprintf(
			"'+1-555-01' || lpad((%s %% 99)::text, 2, '0') || '-' || lpad(((%s >> 8) %% 9000 + 1000)::text, 4, '0')",
			rctx.h32, rctx.h32,
		), nil
	case "ip":
		return fmt.Sprintf(
			"'10.' || (%s %% 256)::text || '.' || ((%s >> 8) %% 256)::text || '.' || ((%s >> 16) %% 256)::text",
			rctx.h32, rctx.h32, rctx.h32,
		), nil
	case "url":
		return fmt.Sprintf("'https://example.com/r/' || left(%s, 12)", rctx.h64), nil
	case "uuid":
		return fmt.Sprintf("md5(%s::text)::uuid", rctx.column), nil
	default:
		return "", fmt.Errorf("unknown replace type %q", rctx.dataType)
	}
}

func replaceSQLMySQL(target sqlTarget, dataType string) (string, error) {
	rctx := newMySQLReplaceContext(target, dataType)

	expr, err := mysqlExprForType(rctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("UPDATE %s SET %s = %s", target.table, target.column, expr), nil
}

func mysqlExprForType(rctx replaceContext) (string, error) {
	switch rctx.dataType {
	case "email":
		return fmt.Sprintf("CONCAT('user', %s %% 1000000, '@example.com')", rctx.h32), nil
	case "name":
		return fmt.Sprintf("CONCAT('User', %s %% 100000)", rctx.h32), nil
	case "phone":
		return fmt.Sprintf(
			"CONCAT('+1-555-01', LPAD(%s %% 99, 2, '0'), '-', LPAD(((%s >> 8) %% 9000) + 1000, 4, '0'))",
			rctx.h32, rctx.h32,
		), nil
	case "ip":
		return fmt.Sprintf(
			"CONCAT('10.', %s %% 256, '.', (%s >> 8) %% 256, '.', (%s >> 16) %% 256)",
			rctx.h32, rctx.h32, rctx.h32,
		), nil
	case "url":
		return fmt.Sprintf("CONCAT('https://example.com/r/', LEFT(MD5(%s), 12))", rctx.column), nil
	case "uuid":
		return fmt.Sprintf(
			"LOWER(CONCAT(LEFT(MD5(%s),8),'-',MID(MD5(%s),9,4),'-4',MID(MD5(%s),13,3),'-',MID(MD5(%s),17,4),'-',RIGHT(MD5(%s),12)))",
			rctx.column, rctx.column, rctx.column, rctx.column, rctx.column,
		), nil
	default:
		return "", fmt.Errorf("unknown replace type %q", rctx.dataType)
	}
}

// hashSQL generates a one-way SHA-256 hex digest expression.
func hashSQL(target sqlTarget) string {
	switch target.engine {
	case "mysql":
		return fmt.Sprintf("UPDATE %s SET %s = SHA2(%s, 256)", target.table, target.column, target.column)
	default: // postgres
		return fmt.Sprintf("UPDATE %s SET %s = encode(sha256(%s::bytea), 'hex')", target.table, target.column, target.column)
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
