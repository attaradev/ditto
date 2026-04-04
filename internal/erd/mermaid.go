package erd

import (
	"fmt"
	"io"
	"strings"
)

// RenderMermaid writes a Mermaid erDiagram block to w.
func RenderMermaid(s *Schema, w io.Writer) error {
	if _, err := fmt.Fprintln(w, "erDiagram"); err != nil {
		return err
	}

	for _, t := range s.Tables {
		if _, err := fmt.Fprintf(w, "    %s {\n", mermaidIdent(t.Name)); err != nil {
			return err
		}
		for _, c := range t.Columns {
			pk := ""
			if c.IsPK {
				pk = " PK"
			}
			if _, err := fmt.Fprintf(w, "        %s %s%s\n",
				mermaidIdent(c.DataType), mermaidIdent(c.Name), pk); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w, "    }"); err != nil {
			return err
		}
	}

	for _, fk := range s.ForeignKeys {
		if _, err := fmt.Fprintf(w, "    %s ||--o{ %s : \"%s -> %s\"\n",
			mermaidIdent(fk.FromTable), mermaidIdent(fk.ToTable),
			fk.FromColumn, fk.ToColumn); err != nil {
			return err
		}
	}

	return nil
}

// mermaidIdent replaces characters that are not safe in Mermaid identifiers
// (spaces and hyphens) with underscores.
func mermaidIdent(s string) string {
	r := strings.NewReplacer(" ", "_", "-", "_")
	return r.Replace(s)
}
