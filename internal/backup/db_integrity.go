package backup

// SQLite integrity of the database snapshot inside an instance bundle.
//
// A matching ciphertext checksum and matching row counts say the bytes are
// the bytes that were written, not that SQLite can still read them. This
// file runs SQLite's own checks against a PRIVATE copy of the database (the
// in-memory snapshot at create time, the extracted file at check and drill
// time) and never against the live database.
//
// Two details matter:
//
//   - The copy is opened read-write with query_only on every connection.
//     A read-only open skips the CHECK constraint expressions, so PRAGMA
//     integrity_check would report "ok" for rows that violate them.
//   - Findings are limited to names (tables, indexes, constraints) and
//     rowids. The manifest is not encrypted, so a finding must never carry a
//     column value.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Database check statuses recorded in the manifest.
const (
	// DatabaseCheckOK: integrity_check and foreign_key_check found nothing.
	DatabaseCheckOK = "ok"
	// DatabaseCheckDamaged: SQLite reported at least one problem.
	DatabaseCheckDamaged = "damaged"
	// DatabaseCheckError: the check could not finish (time limit, open
	// error). The database is NOT known to be healthy.
	DatabaseCheckError = "error"
	// DatabaseCheckUnverified is not stored: it is how a bundle without a
	// recorded check reads. Older bundles are unverified, not healthy.
	DatabaseCheckUnverified = "unverified"
)

const (
	// DefaultDatabaseCheckTimeout bounds one database check. A check that
	// runs out is recorded as an error and never fails the backup.
	DefaultDatabaseCheckTimeout = 10 * time.Minute
	maxDatabaseFindings         = 20
	maxFindingLen               = 200
)

// beforeDatabaseCheck is a test seam: called just before the create-time
// check so a test can observe whether the quiet window is still open.
var beforeDatabaseCheck func()

// IncompleteDatabaseDamaged: SQLite's own checks found damage in the
// database snapshot. The bundle is kept; it just is not a trusted copy.
const IncompleteDatabaseDamaged = "database_damaged"

// IncompleteDatabaseUnchecked: the check could not finish (time limit, open
// error), so nobody knows whether the database is healthy.
const IncompleteDatabaseUnchecked = "database_unchecked"

// DatabaseCheck is the recorded result of the SQLite checks on a bundle's
// database snapshot (manifest contents.instance.database_check).
type DatabaseCheck struct {
	Status string   `json:"status" yaml:"status"`
	Checks []string `json:"checks,omitempty" yaml:"checks,omitempty"`
	// Findings holds up to 20 findings: names and rowids only, never values.
	Findings      []string `json:"findings,omitempty" yaml:"findings,omitempty"`
	FindingsTotal int      `json:"findings_total,omitempty" yaml:"findings_total,omitempty"`
	DurationMS    int64    `json:"duration_ms" yaml:"duration_ms"`
	// Error says why a check ended in status "error".
	Error string `json:"error,omitempty" yaml:"error,omitempty"`
}

// Verified reports whether the check ran to the end and found nothing.
func (c *DatabaseCheck) Verified() bool { return c != nil && c.Status == DatabaseCheckOK }

// DatabaseCheckState names how a bundle's database stands: ok, damaged,
// error, or unverified when no check was recorded.
func (i *InstanceContents) DatabaseCheckState() string {
	if i == nil || i.DatabaseCheck == nil || i.DatabaseCheck.Status == "" {
		return DatabaseCheckUnverified
	}
	return i.DatabaseCheck.Status
}

// Summary is one sentence for operators.
func (c *DatabaseCheck) Summary() string {
	switch {
	case c == nil:
		return "database not checked"
	case c.Status == DatabaseCheckOK:
		return "SQLite integrity and foreign key checks passed"
	case c.Status == DatabaseCheckDamaged:
		return fmt.Sprintf("SQLite reports %d problem(s) in the database: %s", c.FindingsTotal, strings.Join(c.Findings, "; "))
	default:
		return "the database could not be checked: " + c.Error
	}
}

// CheckDatabaseIntegrity runs integrity_check and foreign_key_check on db,
// which must be a private copy (never the live database). It sets query_only
// on the connection it uses. It always returns a result: a check that could
// not finish has status error.
func CheckDatabaseIntegrity(ctx context.Context, db *sql.DB, timeout time.Duration) DatabaseCheck {
	start := time.Now()
	res := DatabaseCheck{Checks: []string{"integrity_check", "foreign_key_check"}}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()
	fail := func(err error) DatabaseCheck {
		res.Status = DatabaseCheckError
		res.Findings, res.FindingsTotal = nil, 0
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			res.Error = "time limit reached"
		case errors.Is(err, context.Canceled):
			res.Error = "canceled"
		default:
			res.Error = truncateFinding(err.Error())
		}
		return res
	}
	if timeout <= 0 {
		timeout = DefaultDatabaseCheckTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := db.Conn(cctx)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(cctx, `PRAGMA query_only = 1`); err != nil {
		return fail(err)
	}

	add := func(s string) {
		res.FindingsTotal++
		if len(res.Findings) < maxDatabaseFindings {
			res.Findings = append(res.Findings, truncateFinding(s))
		}
	}
	rows, err := conn.QueryContext(cctx, fmt.Sprintf(`PRAGMA integrity_check(%d)`, maxDatabaseFindings+1))
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			_ = rows.Close()
			return fail(err)
		}
		if s != "ok" {
			add(s)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fail(err)
	}
	_ = rows.Close()

	fk, err := conn.QueryContext(cctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return fail(err)
	}
	for fk.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int64
		if err := fk.Scan(&table, &rowid, &parent, &fkid); err != nil {
			_ = fk.Close()
			return fail(err)
		}
		add(fmt.Sprintf("foreign key: table %s row %d has no matching row in %s", table, rowid.Int64, parent))
	}
	if err := fk.Err(); err != nil {
		_ = fk.Close()
		return fail(err)
	}
	_ = fk.Close()

	res.Status = DatabaseCheckOK
	if res.FindingsTotal > 0 {
		res.Status = DatabaseCheckDamaged
	}
	return res
}

// CheckDatabaseFileIntegrity opens the private database file at path with
// mode=rw and query_only on every connection, and checks it.
func CheckDatabaseFileIntegrity(ctx context.Context, path string, timeout time.Duration) DatabaseCheck {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=rw&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return DatabaseCheck{Status: DatabaseCheckError, Error: truncateFinding(err.Error())}
	}
	defer func() { _ = db.Close() }()
	return CheckDatabaseIntegrity(ctx, db, timeout)
}

func truncateFinding(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxFindingLen {
		s = s[:maxFindingLen] + "…"
	}
	return s
}

// databaseCheckIncomplete turns a damaged result into the manifest's gap
// list entry, so every surface that lists gaps shows it.
func databaseCheckIncomplete(c DatabaseCheck) []IncompleteItem {
	if c.Status == DatabaseCheckError {
		return []IncompleteItem{{
			Kind: IncompleteDatabaseUnchecked, Count: 1,
			Detail: "SQLite's checks could not finish on the database snapshot (" + c.Error + "), so this bundle's database is unverified",
		}}
	}
	if c.Status != DatabaseCheckDamaged {
		return nil
	}
	return []IncompleteItem{{
		Kind: IncompleteDatabaseDamaged, Count: c.FindingsTotal,
		Detail: "SQLite reports damage in the database snapshot, so this bundle is not a trusted copy and does not count as checked: " + strings.Join(c.Findings, "; "),
	}}
}
