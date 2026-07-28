package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// Ledger repair — the escape hatch for a renumbered migration.
//
// Migrate refuses to start when the database has a version applied under a
// different name than the binary declares for it. That guard is correct: two
// branches claiming one version number and silently continuing is a forked
// schema. But it leaves no way back. The pre-migration snapshot does not
// help — it carries the same ledger — so the only recovery was deleting the
// database, which is why dev3 had to be rebuilt on 2026-07-27.
//
// There is exactly one shape of this that is safe to repair automatically:
// the migration is present in the binary under the SAME NAME at a DIFFERENT
// version. That is renumbering, and renumbering does not change what the
// migration did — the schema on disk is already what the binary expects, only
// the number recording it differs. Moving the ledger row to the declared
// version makes the two agree without touching a single table.
//
// Anything else is refused. A name this binary has never heard of means the
// database was migrated by a different (probably newer) Crewship, and no
// amount of renumbering makes this binary's assumptions true.
//
// The timestamp scheme in migrate_version_scheme.go is what stops this being
// needed again; this exists for the databases that predate it.

// LedgerEntry is one row of _migrations.
type LedgerEntry struct {
	Version   int
	Name      string
	AppliedAt string
}

// Renumber moves one applied migration from the version it was recorded
// under to the version this binary declares for it.
type Renumber struct {
	Name string
	From int
	To   int
}

// RepairPlan is what ApplyLedgerRepair will do. An empty plan means the
// ledger already agrees with the binary.
type RepairPlan struct {
	Renumbers []Renumber
	// PendingAfter lists versions the binary declares that are not applied
	// and will be applied normally on the next start. Purely informational —
	// it is what makes "and then 168 runs for real" visible to the operator.
	PendingAfter []LedgerEntry
}

// Empty reports whether there is nothing to do.
func (p RepairPlan) Empty() bool { return len(p.Renumbers) == 0 }

// declaredLedger renders the binary's migration table in ledger shape.
func declaredLedger() []LedgerEntry {
	out := make([]LedgerEntry, 0, len(migrations))
	for _, m := range migrations {
		out = append(out, LedgerEntry{Version: m.version, Name: m.name})
	}
	return out
}

// PlanLedgerRepair computes what would have to change for the database's
// ledger to agree with this binary. It reads nothing and writes nothing.
func PlanLedgerRepair(applied []LedgerEntry) (RepairPlan, error) {
	return planLedgerRepair(applied, declaredLedger())
}

func planLedgerRepair(applied, declared []LedgerEntry) (RepairPlan, error) {
	declaredByName := make(map[string]int, len(declared))
	for _, d := range declared {
		declaredByName[d.Name] = d.Version
	}

	var plan RepairPlan
	// Final version for every applied migration, so collisions introduced by
	// the plan itself are caught before anything is written.
	final := make(map[int]string, len(applied))
	claim := func(version int, name string) error {
		if other, taken := final[version]; taken {
			return fmt.Errorf(
				"cannot repair: %q and %q would both end up at version %d",
				other, name, version)
		}
		final[version] = name
		return nil
	}

	for _, a := range applied {
		switch declaredVersion, known := declaredByName[a.Name]; {
		case !known:
			// The binary has never heard of this migration. Renumbering
			// cannot help: there is no version here that means what the
			// database did.
			return RepairPlan{}, fmt.Errorf(
				"cannot repair: the database has migration %q applied (at version %d) and this "+
					"binary does not declare it at all. That means the database was migrated by a "+
					"different Crewship, not merely a renumbered one. Upgrade to a build that "+
					"includes %q, or restore a pre-migration snapshot (crewship db restore-snapshot)",
				a.Name, a.Version, a.Name)

		case declaredVersion == a.Version:
			// Already agrees.
			if err := claim(a.Version, a.Name); err != nil {
				return RepairPlan{}, err
			}

		default:
			plan.Renumbers = append(plan.Renumbers, Renumber{Name: a.Name, From: a.Version, To: declaredVersion})
			if err := claim(declaredVersion, a.Name); err != nil {
				return RepairPlan{}, err
			}
		}
	}

	// Sorting keeps the printed plan and the applied UPDATEs deterministic,
	// which matters for a command an operator reads before confirming.
	sort.Slice(plan.Renumbers, func(i, j int) bool { return plan.Renumbers[i].From < plan.Renumbers[j].From })

	if len(plan.Renumbers) > 0 {
		for _, d := range declared {
			if _, applied := final[d.Version]; !applied {
				plan.PendingAfter = append(plan.PendingAfter, d)
			}
		}
		sort.Slice(plan.PendingAfter, func(i, j int) bool { return plan.PendingAfter[i].Version < plan.PendingAfter[j].Version })
	}

	return plan, nil
}

// ReadLedger returns the _migrations rows from the database at dbPath,
// oldest first. Opens the file directly so it works with the server down —
// which is the only situation this is ever used in.
func ReadLedger(ctx context.Context, db *sql.DB) ([]LedgerEntry, error) {
	var present int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = '_migrations'`,
	).Scan(&present); err != nil {
		return nil, fmt.Errorf("look for _migrations: %w", err)
	}
	if present == 0 {
		return nil, ErrNoLedger
	}

	rows, err := db.QueryContext(ctx,
		`SELECT version, name, COALESCE(applied_at, '') FROM _migrations ORDER BY version ASC`)
	if err != nil {
		return nil, fmt.Errorf("read _migrations: %w", err)
	}
	defer rows.Close()

	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.Version, &e.Name, &e.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan _migrations row: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ErrLedgerChanged reports that the ledger moved between planning and
// applying — someone started the server, or another repair ran.
var ErrLedgerChanged = errors.New("ledger changed since the plan was computed")

// ErrNoLedger reports that the file has no _migrations table: it is not a
// Crewship database, or not one that has ever been migrated. Callers turn
// this into "you are probably pointing at the wrong directory" rather than
// leaking the driver's "no such table".
var ErrNoLedger = errors.New("database has no _migrations table")

// ApplyLedgerRepair writes the plan in one transaction, re-reading the
// ledger inside it and refusing if it no longer matches what was planned.
func ApplyLedgerRepair(ctx context.Context, db *sql.DB, plan RepairPlan) error {
	if plan.Empty() {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin repair: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, r := range plan.Renumbers {
		var name string
		err := tx.QueryRowContext(ctx, `SELECT name FROM _migrations WHERE version = ?`, r.From).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && name != r.Name) {
			return fmt.Errorf("%w: expected %q at version %d", ErrLedgerChanged, r.Name, r.From)
		}
		if err != nil {
			return fmt.Errorf("re-check version %d: %w", r.From, err)
		}
	}

	// version is INTEGER PRIMARY KEY, so a direct move can collide with a row
	// that has not been moved yet (two migrations swapping numbers is the
	// obvious case). Park everything negative first — no real version is
	// negative — then place each row at its target.
	// Both loops assert they moved exactly one row. Without that, a plan
	// containing two moves off the same source — which the planner cannot
	// produce but an external caller can, since this is exported — parks the
	// row once, silently matches nothing the second time, and still commits
	// while reporting "Ledger repaired". Rewriting the ledger is not a place
	// to trust that a statement did what was intended.
	moveOne := func(what string, to, from int, name string) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE _migrations SET version = ? WHERE version = ?`, to, from)
		if err != nil {
			return fmt.Errorf("%s %q (v%d): %w", what, name, r0(from), err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("%s %q (v%d): affected-row count unavailable: %w", what, name, r0(from), err)
		}
		if n != 1 {
			return fmt.Errorf("%w: %s of %q expected exactly one row at v%d, changed %d",
				ErrLedgerChanged, what, name, r0(from), n)
		}
		return nil
	}

	for _, r := range plan.Renumbers {
		if err := moveOne("park", -r.From, r.From, r.Name); err != nil {
			return err
		}
	}
	for _, r := range plan.Renumbers {
		if err := moveOne("renumber", r.To, -r.From, r.Name); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit repair: %w", err)
	}
	return nil
}

// r0 reports a parked (negated) version as the real one, so error messages
// name the version an operator recognises rather than the negative placeholder
// the move pass uses internally.
func r0(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
