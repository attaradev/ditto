// Package erd provides schema introspection and ERD rendering for Postgres and
// MySQL databases. It uses information_schema to remain engine-agnostic and
// avoids any engine-specific system catalogue queries.
package erd

import (
	"context"
	"database/sql"
	"fmt"
)

// Column describes a single column in a table.
type Column struct {
	Name     string
	DataType string
	Nullable bool
	IsPK     bool
}

// Table describes a database table and its columns.
type Table struct {
	Name    string
	Columns []Column
}

// ForeignKey describes a referential constraint between two tables.
type ForeignKey struct {
	FromTable  string
	FromColumn string
	ToTable    string
	ToColumn   string
}

// Schema is the result of introspecting a database.
type Schema struct {
	Tables      []Table
	ForeignKeys []ForeignKey
}

// Introspect queries information_schema for tables, columns, primary keys, and
// foreign keys. engineName must be "postgres" or "mysql". database is only
// used for MySQL (Postgres always targets the "public" schema).
func Introspect(ctx context.Context, db *sql.DB, engineName, database string) (*Schema, error) {
	switch engineName {
	case "postgres":
		return introspectPostgres(ctx, db)
	case "mysql":
		return introspectMySQL(ctx, db, database)
	default:
		return nil, fmt.Errorf("erd: unsupported engine %q", engineName)
	}
}

func introspectPostgres(ctx context.Context, db *sql.DB) (*Schema, error) {
	// Fetch columns and PK membership in a single pass via a LEFT JOIN on
	// table_constraints + key_column_usage.
	colRows, err := db.QueryContext(ctx, `
		SELECT
			c.table_name,
			c.column_name,
			c.data_type,
			c.is_nullable,
			CASE WHEN kcu.column_name IS NOT NULL THEN true ELSE false END AS is_pk
		FROM information_schema.columns c
		LEFT JOIN information_schema.table_constraints tc
			ON  tc.table_schema = c.table_schema
			AND tc.table_name   = c.table_name
			AND tc.constraint_type = 'PRIMARY KEY'
		LEFT JOIN information_schema.key_column_usage kcu
			ON  kcu.constraint_name = tc.constraint_name
			AND kcu.table_schema    = c.table_schema
			AND kcu.table_name      = c.table_name
			AND kcu.column_name     = c.column_name
		WHERE c.table_schema = 'public'
		ORDER BY c.table_name, c.ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("erd: postgres columns query: %w", err)
	}
	defer func() { _ = colRows.Close() }()

	tableMap := map[string]*Table{}
	var tableOrder []string

	for colRows.Next() {
		var (
			tableName, colName, dataType, isNullable string
			isPK                                     bool
		)
		if err := colRows.Scan(&tableName, &colName, &dataType, &isNullable, &isPK); err != nil {
			return nil, fmt.Errorf("erd: postgres columns scan: %w", err)
		}
		if _, seen := tableMap[tableName]; !seen {
			tableMap[tableName] = &Table{Name: tableName}
			tableOrder = append(tableOrder, tableName)
		}
		tableMap[tableName].Columns = append(tableMap[tableName].Columns, Column{
			Name:     colName,
			DataType: dataType,
			Nullable: isNullable == "YES",
			IsPK:     isPK,
		})
	}
	if err := colRows.Err(); err != nil {
		return nil, fmt.Errorf("erd: postgres columns iterate: %w", err)
	}

	// Fetch foreign keys.
	fkRows, err := db.QueryContext(ctx, `
		SELECT
			kcu.table_name    AS from_table,
			kcu.column_name   AS from_column,
			ccu.table_name    AS to_table,
			ccu.column_name   AS to_column
		FROM information_schema.referential_constraints rc
		JOIN information_schema.key_column_usage kcu
			ON  kcu.constraint_name   = rc.constraint_name
			AND kcu.constraint_schema = rc.constraint_schema
		JOIN information_schema.constraint_column_usage ccu
			ON  ccu.constraint_name   = rc.unique_constraint_name
			AND ccu.constraint_schema = rc.unique_constraint_schema
		WHERE rc.constraint_schema = 'public'`)
	if err != nil {
		return nil, fmt.Errorf("erd: postgres fk query: %w", err)
	}
	defer func() { _ = fkRows.Close() }()

	fks, err := scanFKRows(fkRows, "postgres")
	if err != nil {
		return nil, err
	}

	schema := &Schema{ForeignKeys: fks}
	for _, name := range tableOrder {
		schema.Tables = append(schema.Tables, *tableMap[name])
	}
	return schema, nil
}

func introspectMySQL(ctx context.Context, db *sql.DB, database string) (*Schema, error) {
	colRows, err := db.QueryContext(ctx, `
		SELECT
			c.TABLE_NAME,
			c.COLUMN_NAME,
			c.DATA_TYPE,
			c.IS_NULLABLE,
			IF(kcu.COLUMN_NAME IS NOT NULL, 1, 0) AS is_pk
		FROM information_schema.COLUMNS c
		LEFT JOIN information_schema.KEY_COLUMN_USAGE kcu
			ON  kcu.TABLE_SCHEMA    = c.TABLE_SCHEMA
			AND kcu.TABLE_NAME      = c.TABLE_NAME
			AND kcu.COLUMN_NAME     = c.COLUMN_NAME
			AND kcu.CONSTRAINT_NAME = 'PRIMARY'
		WHERE c.TABLE_SCHEMA = ?
		ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`, database)
	if err != nil {
		return nil, fmt.Errorf("erd: mysql columns query: %w", err)
	}
	defer func() { _ = colRows.Close() }()

	tableMap := map[string]*Table{}
	var tableOrder []string

	for colRows.Next() {
		var (
			tableName, colName, dataType, isNullable string
			isPKInt                                  int
		)
		if err := colRows.Scan(&tableName, &colName, &dataType, &isNullable, &isPKInt); err != nil {
			return nil, fmt.Errorf("erd: mysql columns scan: %w", err)
		}
		if _, seen := tableMap[tableName]; !seen {
			tableMap[tableName] = &Table{Name: tableName}
			tableOrder = append(tableOrder, tableName)
		}
		tableMap[tableName].Columns = append(tableMap[tableName].Columns, Column{
			Name:     colName,
			DataType: dataType,
			Nullable: isNullable == "YES",
			IsPK:     isPKInt == 1,
		})
	}
	if err := colRows.Err(); err != nil {
		return nil, fmt.Errorf("erd: mysql columns iterate: %w", err)
	}

	fkRows, err := db.QueryContext(ctx, `
		SELECT
			kcu.TABLE_NAME             AS from_table,
			kcu.COLUMN_NAME            AS from_column,
			kcu.REFERENCED_TABLE_NAME  AS to_table,
			kcu.REFERENCED_COLUMN_NAME AS to_column
		FROM information_schema.KEY_COLUMN_USAGE kcu
		WHERE kcu.TABLE_SCHEMA = ?
		  AND kcu.REFERENCED_TABLE_NAME IS NOT NULL`, database)
	if err != nil {
		return nil, fmt.Errorf("erd: mysql fk query: %w", err)
	}
	defer func() { _ = fkRows.Close() }()

	fks, err := scanFKRows(fkRows, "mysql")
	if err != nil {
		return nil, err
	}

	schema := &Schema{ForeignKeys: fks}
	for _, name := range tableOrder {
		schema.Tables = append(schema.Tables, *tableMap[name])
	}
	return schema, nil
}

func scanFKRows(rows *sql.Rows, engineName string) ([]ForeignKey, error) {
	defer func() { _ = rows.Close() }()
	var fks []ForeignKey
	for rows.Next() {
		var fk ForeignKey
		if err := rows.Scan(&fk.FromTable, &fk.FromColumn, &fk.ToTable, &fk.ToColumn); err != nil {
			return nil, fmt.Errorf("erd: %s fk scan: %w", engineName, err)
		}
		fks = append(fks, fk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("erd: %s fk iterate: %w", engineName, err)
	}
	return fks, nil
}
