package erd

import (
	"strings"
	"testing"
)

// testSchema is a hand-crafted schema used across all renderer tests.
var testSchema = &Schema{
	Tables: []Table{
		{
			Name: "users",
			Columns: []Column{
				{Name: "id", DataType: "bigint", Nullable: false, IsPK: true},
				{Name: "email", DataType: "character varying", Nullable: false, IsPK: false},
				{Name: "created_at", DataType: "timestamp with time zone", Nullable: true, IsPK: false},
			},
		},
		{
			Name: "orders",
			Columns: []Column{
				{Name: "id", DataType: "bigint", Nullable: false, IsPK: true},
				{Name: "user_id", DataType: "bigint", Nullable: false, IsPK: false},
				{Name: "total", DataType: "numeric", Nullable: true, IsPK: false},
			},
		},
	},
	ForeignKeys: []ForeignKey{
		{FromTable: "orders", FromColumn: "user_id", ToTable: "users", ToColumn: "id"},
	},
}

func TestRenderMermaid(t *testing.T) {
	var buf strings.Builder
	if err := RenderMermaid(testSchema, &buf); err != nil {
		t.Fatalf("RenderMermaid: %v", err)
	}
	got := buf.String()

	checks := []string{
		"erDiagram",
		"users {",
		"bigint id PK",
		"character_varying email",
		"orders {",
		"bigint user_id",
		"orders ||--o{ users : \"user_id -> id\"",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("RenderMermaid output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRenderMermaidNoFKs(t *testing.T) {
	s := &Schema{
		Tables: []Table{
			{Name: "standalone", Columns: []Column{
				{Name: "id", DataType: "integer", Nullable: false, IsPK: true},
			}},
		},
	}
	var buf strings.Builder
	if err := RenderMermaid(s, &buf); err != nil {
		t.Fatalf("RenderMermaid: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "erDiagram") {
		t.Error("missing erDiagram header")
	}
	if !strings.Contains(got, "standalone {") {
		t.Error("missing table block")
	}
	// No relationship lines should be present.
	if strings.Contains(got, "||--o{") {
		t.Error("unexpected relationship line in schema with no FKs")
	}
}

func TestRenderMermaidHyphenInName(t *testing.T) {
	s := &Schema{
		Tables: []Table{
			{Name: "my-table", Columns: []Column{
				{Name: "my-col", DataType: "text", Nullable: true, IsPK: false},
			}},
		},
	}
	var buf strings.Builder
	if err := RenderMermaid(s, &buf); err != nil {
		t.Fatalf("RenderMermaid: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "my-table") {
		t.Error("hyphen in table name should be replaced with underscore")
	}
	if !strings.Contains(got, "my_table") {
		t.Error("expected underscore-normalised table name")
	}
}

func TestRenderDBML(t *testing.T) {
	var buf strings.Builder
	if err := RenderDBML(testSchema, &buf); err != nil {
		t.Fatalf("RenderDBML: %v", err)
	}
	got := buf.String()

	checks := []string{
		"Table users {",
		"id bigint [pk, not null]",
		"email character varying [not null]",
		"created_at timestamp with time zone",
		"Table orders {",
		"Ref: orders.user_id > users.id",
	}
	for _, want := range checks {
		if !strings.Contains(got, want) {
			t.Errorf("RenderDBML output missing %q\nfull output:\n%s", want, got)
		}
	}
}

func TestRenderDBMLNullableColumn(t *testing.T) {
	s := &Schema{
		Tables: []Table{
			{Name: "t", Columns: []Column{
				{Name: "note", DataType: "text", Nullable: true, IsPK: false},
			}},
		},
	}
	var buf strings.Builder
	if err := RenderDBML(s, &buf); err != nil {
		t.Fatalf("RenderDBML: %v", err)
	}
	got := buf.String()
	// Nullable columns should have no attrs at all.
	if strings.Contains(got, "not null") {
		t.Error("nullable column should not have 'not null' attr")
	}
	if strings.Contains(got, "[") {
		t.Error("nullable non-PK column should have no attribute brackets")
	}
}
