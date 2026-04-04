package erd

import (
	"fmt"
	"io"
	"strings"
)

// RenderDBML writes a DBML representation to w.
// DBML (Database Markup Language) is the format used by dbdiagram.io.
func RenderDBML(s *Schema, w io.Writer) error {
	for _, t := range s.Tables {
		if _, err := fmt.Fprintf(w, "Table %s {\n", t.Name); err != nil {
			return err
		}
		for _, c := range t.Columns {
			attrs := buildDBMLAttrs(c)
			attrStr := ""
			if len(attrs) > 0 {
				attrStr = " [" + strings.Join(attrs, ", ") + "]"
			}
			if _, err := fmt.Fprintf(w, "  %s %s%s\n", c.Name, c.DataType, attrStr); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w, "}"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}

	for _, fk := range s.ForeignKeys {
		if _, err := fmt.Fprintf(w, "Ref: %s.%s > %s.%s\n",
			fk.FromTable, fk.FromColumn, fk.ToTable, fk.ToColumn); err != nil {
			return err
		}
	}

	return nil
}

func buildDBMLAttrs(c Column) []string {
	var attrs []string
	if c.IsPK {
		attrs = append(attrs, "pk")
	}
	if !c.Nullable {
		attrs = append(attrs, "not null")
	}
	return attrs
}
