package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func resetIdentifier(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// ResetApplicationData replaces application rows with a freshly migrated
// template's defaults. The database inode, schema, migration history and
// installation nonce survive. Foreign keys are checked before commit.
// templatePath must be a trusted fresh DB built by this binary, with no live
// connection. This operation is offline and needs external lifetime leases.
func ResetApplicationData(ctx context.Context, db *sql.DB, templatePath string) error {
	return resetApplicationData(ctx, db, templatePath, false)
}

// ValidateResetTemplate checks schema compatibility without changing rows or
// creating a recovery marker. An incompatible DB can still start to upgrade.
func ValidateResetTemplate(ctx context.Context, db *sql.DB, templatePath string) error {
	return resetApplicationData(ctx, db, templatePath, true)
}

func resetApplicationData(ctx context.Context, db *sql.DB, templatePath string, validateOnly bool) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `ATTACH DATABASE ? AS reset_template`, templatePath); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `DETACH DATABASE reset_template`)
	tables := []string{}
	virtual := map[string]bool{}
	for _, schema := range []string{"main", "reset_template"} {
		rows, err := conn.QueryContext(ctx, `SELECT name,type FROM pragma_table_list WHERE schema=? AND type IN ('table','virtual') AND name NOT LIKE 'sqlite_%' AND name NOT IN ('_migrations','resource_cleanup_installation') ORDER BY name`, schema)
		if err != nil {
			return err
		}
		names := []string{}
		for rows.Next() {
			var name, kind string
			if err := rows.Scan(&name, &kind); err != nil {
				rows.Close()
				return err
			}
			names = append(names, name)
			if schema == "main" && kind == "virtual" {
				virtual[name] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if schema == "main" {
			tables = names
		} else if strings.Join(tables, "\x00") != strings.Join(names, "\x00") {
			return fmt.Errorf("reset requires the current complete schema; start this version and finish migrations before resetting")
		}
	}
	for table := range virtual {
		var definition string
		if err := conn.QueryRowContext(ctx, `SELECT sql FROM main.sqlite_schema WHERE name=?`, table).Scan(&definition); err != nil {
			return err
		}
		if !strings.Contains(strings.ToLower(definition), "fts5") {
			return fmt.Errorf("reset does not support virtual table %s", table)
		}
	}
	var hasSequence int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM main.sqlite_schema WHERE name='sqlite_sequence'`).Scan(&hasSequence); err != nil {
		return err
	}
	columns := map[string][]string{}
	for _, table := range tables {
		var mainColumns string
		for _, schema := range []string{"main", "reset_template"} {
			rows, err := conn.QueryContext(ctx, `SELECT name FROM pragma_table_xinfo(?,?) WHERE hidden=0 ORDER BY cid`, table, schema)
			if err != nil {
				return err
			}
			names := []string{}
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					rows.Close()
					return err
				}
				names = append(names, name)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if schema == "main" {
				mainColumns = strings.Join(names, "\x00")
				columns[table] = names
			} else if mainColumns != strings.Join(names, "\x00") {
				return fmt.Errorf("reset schema differs for table %s; finish migrations with this version first", table)
			}
		}
	}
	type trigger struct{ name, sql string }
	triggers := []trigger{}
	rows, err := conn.QueryContext(ctx, `SELECT name,sql FROM main.sqlite_schema WHERE type='trigger' ORDER BY name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var tr trigger
		if err := rows.Scan(&tr.name, &tr.sql); err != nil {
			rows.Close()
			return err
		}
		triggers = append(triggers, tr)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if validateOnly {
		return nil
	}
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, tr := range triggers {
		if _, err = tx.ExecContext(ctx, `DROP TRIGGER `+resetIdentifier(tr.name)); err != nil {
			return err
		}
	}
	for table := range virtual {
		if _, err = tx.ExecContext(ctx, `INSERT INTO main.`+resetIdentifier(table)+` (`+resetIdentifier(table)+`) VALUES('delete-all')`); err != nil {
			return fmt.Errorf("clear search index %s: %w", table, err)
		}
	}
	for _, table := range tables {
		if virtual[table] {
			continue
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM main.`+resetIdentifier(table)); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	for _, table := range tables {
		if virtual[table] {
			continue
		}
		names := make([]string, len(columns[table]))
		for i, name := range columns[table] {
			names[i] = resetIdentifier(name)
		}
		list := strings.Join(names, ",")
		if _, err = tx.ExecContext(ctx, `INSERT INTO main.`+resetIdentifier(table)+` (`+list+`) SELECT `+list+` FROM reset_template.`+resetIdentifier(table)); err != nil {
			return fmt.Errorf("restore clean defaults for %s: %w", table, err)
		}
	}
	if hasSequence != 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM main.sqlite_sequence; INSERT INTO main.sqlite_sequence(name,seq) SELECT name,seq FROM reset_template.sqlite_sequence`); err != nil {
			return err
		}
	}
	for table := range virtual {
		if _, err = tx.ExecContext(ctx, `INSERT INTO main.`+resetIdentifier(table)+` (`+resetIdentifier(table)+`) VALUES('rebuild')`); err != nil {
			return fmt.Errorf("rebuild search index %s: %w", table, err)
		}
	}
	for _, tr := range triggers {
		if _, err = tx.ExecContext(ctx, tr.sql); err != nil {
			return err
		}
	}
	violations, err := tx.QueryContext(ctx, `PRAGMA main.foreign_key_check`)
	if err != nil {
		return err
	}
	invalid := violations.Next()
	readErr := violations.Err()
	violations.Close()
	if readErr != nil {
		return readErr
	}
	if invalid {
		return fmt.Errorf("reset defaults fail foreign key validation; transaction rolled back")
	}
	return tx.Commit()
}
