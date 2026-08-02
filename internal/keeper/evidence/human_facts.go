package evidence

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Two facts an operator asked for by name while ruling on a corpus of
// escalations: "is there a backup?" and "would a narrower credential do?".
//
// They belong here, with the rest, because of what this package refuses to do.
// Every line it produces is a query against a real table; a line that cannot be
// one is left out. That is what separates a fact from advice — and advice is
// what makes the model decide instead of the person, which is the failure the
// whole ground-truth exercise exists to avoid.
//
// # The one that is not here
//
// "Is this reversible?" was asked for and is deliberately absent. An access
// request carries only a free-text intent, so answering it means deciding from
// agent prose that "drop the deprecated sessions_old table" is a DROP. That is
// the confident wrong answer the package header warns about, and it fails in the
// direction that costs: an intent phrased as cleanup reads as reversible, and
// the line saying so is repeated back as justification and acted on.
//
// The /execute path carries a real command and could answer it structurally.
// /access cannot, and a heuristic wearing a fact's clothes is worse than a
// missing line.

const (
	// FactLastBackup is the age of this WORKSPACE's most recent backup. Not the
	// credential's, and not the table's — backup_catalog is scoped to a workspace
	// or a crew, so a per-table claim would be an invention. Phrased as what it
	// is so nobody reads "backup 6h ago" as "this table can be restored".
	FactLastBackup = "workspace_last_backup"
	// FactNarrowerCredential names another usable credential from the same
	// provider at a lower tier, when one exists. It answers "does a smaller key
	// already exist for this job" with a name the operator can check, rather than
	// with a recommendation.
	FactNarrowerCredential = "narrower_credential_available"
)

// LastBackup is the workspace's most recent backup.
//
// Exists distinguishes the two answers that must never be confused: a nil
// *LastBackup means the query failed and we do not know, while Exists=false
// means we looked and there is none. Collapsing them would turn an outage into
// "no backup", which reads as an argument against approving — the mirror of the
// failure the package header describes, and just as wrong.
type LastBackup struct {
	Exists    bool
	AgeHours  int
	CreatedAt string
}

// NarrowerCredential is a usable, lower-tier credential from the same provider.
type NarrowerCredential struct {
	Exists        bool
	Name          string
	SecurityLevel int
}

// queryLastBackup reads backup_catalog for the workspace, newest first.
//
// Scoped by workspace_id and nothing else: a neighbouring workspace's backup
// answering this one's question would be an argument for approving, manufactured
// by a missing predicate. idx_backup_catalog_workspace makes it a single probe.
func queryLastBackup(ctx context.Context, db Querier, workspaceID string, now time.Time) (*LastBackup, error) {
	if workspaceID == "" {
		return nil, errors.New("evidence: last backup: workspace id is required")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT `+sqlBackupInstant+`
		  FROM backup_catalog
		 WHERE workspace_id = ?
		   AND `+sqlBackupInstant+` IS NOT NULL
		 ORDER BY `+sqlBackupInstant+` DESC
		 LIMIT 1`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("evidence: last backup: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("evidence: last backup: %w", err)
		}
		return &LastBackup{Exists: false}, nil
	}
	var at sql.NullString
	if err := rows.Scan(&at); err != nil {
		return nil, fmt.Errorf("evidence: last backup scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evidence: last backup: %w", err)
	}
	if !at.Valid || at.String == "" {
		return &LastBackup{Exists: false}, nil
	}
	ts, err := time.Parse(time.RFC3339, at.String)
	if err != nil {
		// A row exists but its timestamp is unreadable. Reporting Exists=false
		// would claim there is no backup, which is a different and stronger claim
		// than "we could not read when it was taken".
		return nil, fmt.Errorf("evidence: last backup: unparseable created_at %q: %w", at.String, err)
	}
	age := now.UTC().Sub(ts.UTC())
	if age < 0 {
		age = 0 // a clock skew must not render as a negative age
	}
	return &LastBackup{Exists: true, AgeHours: int(age.Hours()), CreatedAt: at.String}, nil
}

// sqlBackupInstant normalises backup_catalog.created_at the same way sqlInstant
// does for keeper_requests, and for the same reason: the column carries both
// RFC3339 and SQLite's legacy space-separated form, and a plain text compare
// sorts every legacy row before every RFC3339 one. Here the error runs toward
// reporting an OLDER backup than exists, which is the safe direction — but only
// by luck, and relying on luck is how the denial-count bug happened.
const sqlBackupInstant = `strftime('%Y-%m-%dT%H:%M:%SZ', created_at)`

// queryNarrowerCredential looks for a usable credential from the same provider
// at a strictly lower tier, in the same workspace.
//
// Four predicates, each earning its place:
//
//   - same provider, and 'NONE' is not a provider: it is the schema DEFAULT for
//     credentials.provider, the sentinel for "none recorded". Treating it as a
//     value makes every credential in a workspace a same-provider match for
//     every other — dev2 offered a docs API key as a narrower substitute for a
//     production database admin credential, which is precisely the "sends the
//     operator somewhere useless" failure this predicate exists to prevent.
//     An AWS key is not a narrower GitHub key either.
//   - strictly lower security_level: equal is not narrower.
//   - status ACTIVE: a revoked or pending credential cannot do the job, and
//     offering one costs the operator the time it takes to find that out.
//   - deleted_at IS NULL: the repo-wide soft-delete convention.
//
// A credential with no provider recorded matches nothing, on EITHER side,
// because "same provider" cannot be established — omission again, rather than
// pairing every provider-less credential with every other.
func queryNarrowerCredential(ctx context.Context, db Querier, credentialID string) (*NarrowerCredential, error) {
	if credentialID == "" {
		return nil, errors.New("evidence: narrower credential: credential id is required")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT alt.name, alt.security_level
		  FROM credentials AS this
		  JOIN credentials AS alt
		    ON alt.workspace_id = this.workspace_id
		   AND alt.provider = this.provider
		   AND alt.id != this.id
		 WHERE this.id = ?
		   AND UPPER(COALESCE(this.provider, '')) NOT IN ('', 'NONE')
		   AND UPPER(COALESCE(alt.provider, '')) NOT IN ('', 'NONE')
		   AND alt.security_level < this.security_level
		   AND UPPER(alt.status) = 'ACTIVE'
		   AND alt.deleted_at IS NULL
		 ORDER BY alt.security_level ASC, alt.name ASC
		 LIMIT 1`, credentialID)
	if err != nil {
		return nil, fmt.Errorf("evidence: narrower credential: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("evidence: narrower credential: %w", err)
		}
		return &NarrowerCredential{Exists: false}, nil
	}
	var name string
	var level int
	if err := rows.Scan(&name, &level); err != nil {
		return nil, fmt.Errorf("evidence: narrower credential scan: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("evidence: narrower credential: %w", err)
	}
	return &NarrowerCredential{Exists: true, Name: name, SecurityLevel: level}, nil
}
