package obfuscation

import (
	"strings"
	"testing"

	"github.com/attaradev/ditto/internal/config"
)

func rule(table, column, strategy string) config.ObfuscationRule {
	return config.ObfuscationRule{Table: table, Column: column, Strategy: strategy}
}

// --- BuildSQL unit tests (no DB required) ---

func TestBuildSQL_Nullify_Postgres(t *testing.T) {
	stmt, args, err := BuildSQL("postgres", rule("users", "ssn", "nullify"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `UPDATE "users" SET "ssn" = NULL`
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestBuildSQL_Nullify_MySQL(t *testing.T) {
	stmt, _, err := BuildSQL("mysql", rule("users", "ssn", "nullify"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "UPDATE `users` SET `ssn` = NULL"
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
}

func TestBuildSQL_Redact_DefaultText_Postgres(t *testing.T) {
	stmt, args, err := BuildSQL("postgres", rule("users", "email", "redact"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `UPDATE "users" SET "email" = $1`
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
	if len(args) != 1 || args[0] != "[redacted]" {
		t.Errorf("args = %v, want [[redacted]]", args)
	}
}

func TestBuildSQL_Redact_CustomText(t *testing.T) {
	r := config.ObfuscationRule{Table: "orders", Column: "notes", Strategy: "redact", With: "[removed]"}
	_, args, err := BuildSQL("postgres", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) != 1 || args[0] != "[removed]" {
		t.Errorf("args = %v, want [[removed]]", args)
	}
}

func TestBuildSQL_Redact_MySQL(t *testing.T) {
	stmt, args, err := BuildSQL("mysql", rule("users", "email", "redact"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "UPDATE `users` SET `email` = ?"
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
	if len(args) != 1 || args[0] != "[redacted]" {
		t.Errorf("args = %v, want [[redacted]]", args)
	}
}

func TestBuildSQL_Mask_NoKeepLast_Postgres(t *testing.T) {
	stmt, _, err := BuildSQL("postgres", rule("users", "phone", "mask"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `UPDATE "users" SET "phone" = REPEAT('*', length("phone"::text))`
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
}

func TestBuildSQL_Mask_KeepLast_Postgres(t *testing.T) {
	r := config.ObfuscationRule{Table: "users", Column: "phone", Strategy: "mask", KeepLast: 4}
	stmt, _, err := BuildSQL("postgres", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `UPDATE "users" SET "phone" = REPEAT('*', GREATEST(0, length("phone"::text) - 4)) || right("phone"::text, 4)`
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
}

func TestBuildSQL_Mask_KeepLast_MySQL(t *testing.T) {
	r := config.ObfuscationRule{Table: "users", Column: "phone", Strategy: "mask", KeepLast: 4}
	stmt, _, err := BuildSQL("mysql", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "UPDATE `users` SET `phone` = CONCAT(REPEAT('*', GREATEST(0, CHAR_LENGTH(`phone`) - 4)), RIGHT(`phone`, 4))"
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
}

func TestBuildSQL_Mask_CustomChar(t *testing.T) {
	r := config.ObfuscationRule{Table: "users", Column: "phone", Strategy: "mask", MaskChar: "X"}
	stmt, _, err := BuildSQL("postgres", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `UPDATE "users" SET "phone" = REPEAT('X', length("phone"::text))`
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
}

func TestBuildSQL_Hash_Postgres(t *testing.T) {
	stmt, args, err := BuildSQL("postgres", rule("users", "email", "hash"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `UPDATE "users" SET "email" = encode(sha256("email"::bytea), 'hex')`
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestBuildSQL_Hash_MySQL(t *testing.T) {
	stmt, _, err := BuildSQL("mysql", rule("users", "email", "hash"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "UPDATE `users` SET `email` = SHA2(`email`, 256)"
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
}

func TestBuildSQL_UnknownStrategy(t *testing.T) {
	_, _, err := BuildSQL("postgres", rule("users", "email", "scramble"))
	if err == nil {
		t.Fatal("expected error for unknown strategy, got nil")
	}
}

func TestBuildSQL_QuoteIdent_SpecialChars(t *testing.T) {
	r := config.ObfuscationRule{Table: `my"table`, Column: `my"col`, Strategy: "nullify"}
	stmt, _, err := BuildSQL("postgres", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `UPDATE "my""table" SET "my""col" = NULL`
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
}

func TestBuildSQL_QuoteIdent_MySQL_Backtick(t *testing.T) {
	r := config.ObfuscationRule{Table: "my`table", Column: "col", Strategy: "nullify"}
	stmt, _, err := BuildSQL("mysql", r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "UPDATE `my``table` SET `col` = NULL"
	if stmt != want {
		t.Errorf("stmt = %q, want %q", stmt, want)
	}
}

// --- replace strategy ---

func ruleReplace(table, column, typ string) config.ObfuscationRule {
	return config.ObfuscationRule{Table: table, Column: column, Strategy: "replace", Type: typ}
}

func TestBuildSQL_Replace_Email_Postgres(t *testing.T) {
	stmt, args, err := BuildSQL("postgres", ruleReplace("users", "email", "email"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
	for _, want := range []string{"UPDATE", `"users"`, `"email"`, "example.com", "hashtext"} {
		if !strings.Contains(stmt, want) {
			t.Errorf("stmt missing %q: %s", want, stmt)
		}
	}
}

func TestBuildSQL_Replace_Email_MySQL(t *testing.T) {
	stmt, _, err := BuildSQL("mysql", ruleReplace("users", "email", "email"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"UPDATE", "`users`", "`email`", "example.com", "CRC32"} {
		if !strings.Contains(stmt, want) {
			t.Errorf("stmt missing %q: %s", want, stmt)
		}
	}
}

func TestBuildSQL_Replace_Phone_Postgres(t *testing.T) {
	stmt, _, err := BuildSQL("postgres", ruleReplace("users", "phone", "phone"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 555-01xx is the NANP fictional range
	if !strings.Contains(stmt, "555-01") {
		t.Errorf("phone expression should use 555-01xx range: %s", stmt)
	}
}

func TestBuildSQL_Replace_Phone_MySQL(t *testing.T) {
	stmt, _, err := BuildSQL("mysql", ruleReplace("users", "phone", "phone"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"555-01", ">> 8", "% 9000"} {
		if !strings.Contains(stmt, want) {
			t.Errorf("stmt missing %q: %s", want, stmt)
		}
	}
}

func TestBuildSQL_Replace_IP_Postgres(t *testing.T) {
	stmt, _, err := BuildSQL("postgres", ruleReplace("logs", "ip_address", "ip"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stmt, "'10.'") {
		t.Errorf("ip expression should use RFC1918 10.x.x.x range: %s", stmt)
	}
}

func TestBuildSQL_Replace_IP_MySQL(t *testing.T) {
	stmt, _, err := BuildSQL("mysql", ruleReplace("logs", "ip_address", "ip"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"'10.'", ">> 8", ">> 16", "% 256"} {
		if !strings.Contains(stmt, want) {
			t.Errorf("stmt missing %q: %s", want, stmt)
		}
	}
}

func TestBuildSQL_Replace_URL_Postgres(t *testing.T) {
	stmt, _, err := BuildSQL("postgres", ruleReplace("links", "target_url", "url"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stmt, "example.com") {
		t.Errorf("url expression should use example.com: %s", stmt)
	}
}

func TestBuildSQL_Replace_UUID_Postgres(t *testing.T) {
	stmt, _, err := BuildSQL("postgres", ruleReplace("sessions", "token", "uuid"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stmt, "md5") {
		t.Errorf("uuid expression should use md5: %s", stmt)
	}
}

func TestBuildSQL_Replace_UUID_MySQL(t *testing.T) {
	stmt, _, err := BuildSQL("mysql", ruleReplace("sessions", "token", "uuid"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stmt, "MD5") {
		t.Errorf("uuid expression should use MD5: %s", stmt)
	}
}

func TestBuildSQL_Replace_UnknownType(t *testing.T) {
	_, _, err := BuildSQL("postgres", ruleReplace("t", "c", "ssn"))
	if err == nil {
		t.Fatal("expected error for unknown replace type, got nil")
	}
}

func TestApply_EmptyRules_NoDBNeeded(t *testing.T) {
	// With no rules, Apply must return nil without ever opening a DB connection.
	o := New("postgres", "postgres://nobody:nobody@localhost:9999/nodb", nil)
	if err := o.Apply(t.Context()); err != nil {
		t.Fatalf("Apply with empty rules returned error: %v", err)
	}
}

func TestApply_UnknownEngine(t *testing.T) {
	o := New("oracle", "oracle://...", []config.ObfuscationRule{rule("t", "c", "nullify")})
	if err := o.Apply(t.Context()); err == nil {
		t.Fatal("expected error for unknown engine, got nil")
	}
}
