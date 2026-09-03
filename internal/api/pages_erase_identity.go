package api

// Art. 17 erasure — unnaming the subject on the Pages tables (issue #1976,
// defect 2; docs/prd/pages.md §7.1b, §7.3.2, §10b.5c).
//
// # The contract this file implements
//
// A workspace-scoped erasure promises that the subject is UNNAMED IN THAT
// WORKSPACE. That is the sentence #1976 asked for and did not have: the
// cascade in admin_gdpr.go transferred the subject's pages (§7.1 rule 1b) and
// stopped, so four columns kept naming a person whose SAR ticket had been
// closed as "erased":
//
//	page_versions.author_user_id       ON DELETE SET NULL
//	page_grants.granted_by_user_id     ON DELETE CASCADE
//	page_public_tokens.created_by_user_id  ON DELETE CASCADE
//	page_webhooks.created_by_user_id   ON DELETE CASCADE
//
// Every one of those columns already carries the right answer in its FK
// action — and none of them ever fires, because a workspace-scoped erasure
// deliberately never deletes the `users` row (a person may be a member of
// several workspaces, and one workspace's SAR cannot speak for another's).
// The schema had decided what should happen to these rows when the human
// behind them goes away; nothing was executing that decision.
//
// So this file does not invent a policy. It RUNS THE POLICY THE SCHEMA
// ALREADY DECLARES, restricted to one workspace:
//
//   - SET NULL for page_versions: history stays, identity goes. A version is
//     worth keeping without its author ("a version whose author was erased is
//     still a version worth keeping" — the migration's own words), and the
//     rollback that version exists for still works.
//   - CASCADE (delete) for the other three: each is an AUTHORITY, not a
//     record. §7.1b rule 1 is that only a human issues a grant, and its
//     second half is that a grant whose issuer no longer exists is authority
//     delegated by nobody; page_webhooks' migration says the same of a token
//     ("a capability nobody is accountable for, and it dies with them"). The
//     NOT NULL on all three issuer columns is that rule in the schema, which
//     is also why nulling the issuer is not an option here without relaxing a
//     constraint that exists to prevent exactly the row we would be creating.
//
// # Why the two capability tables are deleted rather than marked revoked
//
// Both page_public_tokens and page_webhooks carry `revoked_at`, and the
// ordinary revoke path (pages_public_tokens.go, pages_webhooks.go) marks
// rather than deletes — "was it used after we pulled it" is a question a
// deleted row cannot answer. That reasoning is about an INCIDENT, and it is
// not the reasoning that applies here: a marked-revoked row still carries
// `created_by_user_id NOT NULL`, i.e. it still names the subject, which is
// the one thing an erasure may not leave behind. Nulling the column instead
// would need a migration relaxing a NOT NULL whose whole purpose is "issued
// only by a human" — permanently weakening an invariant on every row, for
// every workspace, to serve the rare erasure. The schema's own answer for a
// vanished issuer is the CASCADE, and the erasure's accountability record is
// the gdpr_actions row this cascade writes (which is itself excluded from
// erasure), not the capability row. So: delete, and count it in the audit.
//
// # Why grants NAMING the subject go too, not only grants they issued
//
// #1976 tabulates granted_by_user_id. page_grants also names a user in
// (subject_type='user', subject_id) — the same table, the same workspace, the
// same person — and a contract that says "unnamed in that workspace" cannot
// be half-true for one table. A grant TO an erased subject is also a live
// capability pointing at them, so it is removed by the same statement.
//
// # Scoping
//
// None of the four tables has a workspace_id column, so every statement joins
// through pages.workspace_id (page_webhooks through page_panels first). That
// is what stops an erasure in one workspace from touching the subject's rows
// in another — the guarantee the whole workspace-scoped design rests on, and
// it is asserted directly in admin_gdpr_pages_identity_test.go.
//
// # Append-only tables are NOT reached from here
//
// journal_entries, audit_logs and gdpr_actions still name the subject after
// this runs, by design — see the "Tables intentionally excluded" section of
// admin_gdpr.go. A SAR does not erase the record of the SAR.

import (
	"context"
	"database/sql"
	"fmt"
)

// pageIdentityErasure is the per-table receipt this step hands back to the
// cascade, so the audit row and the operator's terminal both say what
// happened rather than reporting one opaque total.
type pageIdentityErasure struct {
	VersionsAnonymised  int
	GrantsRemoved       int
	PublicTokensRevoked int
	WebhooksRevoked     int
}

// erasePagesIdentity unnames userID on the four Pages tables, inside wsID
// only. All four statements run in one transaction: they are one promise
// ("the subject is unnamed on Pages in this workspace"), and a half-applied
// promise is worse than a refused one — an operator reading a receipt that
// says the grants went would have no reason to suspect the webhook token the
// same person minted is still live.
//
// The rest of the cascade in admin_gdpr.go is deliberately NOT wrapped in
// this transaction. It is statement-at-a-time with a firstErr accumulator and
// a 207 for partial success, because several of its steps have on-disk side
// effects (peer card files, user model files) that no database transaction
// can roll back; making the whole cascade atomic would be a promise the
// filesystem cannot keep. These four steps touch nothing but rows, so they
// can keep the stronger guarantee among themselves.
func erasePagesIdentity(ctx context.Context, db *sql.DB, wsID, userID string) (pageIdentityErasure, error) {
	var out pageIdentityErasure

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("begin pages identity erasure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1) page_versions — anonymise. The column is nullable precisely so an
	// erased author leaves a version behind rather than a hole in the
	// history, and the SET NULL on its FK is the same decision.
	res, err := tx.ExecContext(ctx, `
		UPDATE page_versions
		   SET author_user_id = NULL
		 WHERE author_user_id = ?
		   AND page_id IN (SELECT id FROM pages WHERE workspace_id = ?)`,
		userID, wsID)
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("anonymise page_versions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("anonymise page_versions: rows affected: %w", err)
	}
	out.VersionsAnonymised = int(n)

	// 2) page_grants — remove. Both arcs in one statement so a row where the
	// subject is both issuer and grantee is counted once, not twice.
	res, err = tx.ExecContext(ctx, `
		DELETE FROM page_grants
		 WHERE page_id IN (SELECT id FROM pages WHERE workspace_id = ?)
		   AND (granted_by_user_id = ? OR (subject_type = 'user' AND subject_id = ?))`,
		wsID, userID, userID)
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("remove page_grants: %w", err)
	}
	if n, err = res.RowsAffected(); err != nil {
		return pageIdentityErasure{}, fmt.Errorf("remove page_grants: rows affected: %w", err)
	}
	out.GrantsRemoved = int(n)

	// 3) page_public_tokens — revoke by removal. Every live /p/{token} link
	// the subject published stops resolving at once.
	res, err = tx.ExecContext(ctx, `
		DELETE FROM page_public_tokens
		 WHERE created_by_user_id = ?
		   AND page_id IN (SELECT id FROM pages WHERE workspace_id = ?)`,
		userID, wsID)
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("revoke page_public_tokens: %w", err)
	}
	if n, err = res.RowsAffected(); err != nil {
		return pageIdentityErasure{}, fmt.Errorf("revoke page_public_tokens: rows affected: %w", err)
	}
	out.PublicTokensRevoked = int(n)

	// 4) page_webhooks — revoke by removal. Two hops to the workspace: the
	// token names only a panel (deliberately — see the migration's "WHY THERE
	// IS NO page_id COLUMN"), and the panel names the page.
	res, err = tx.ExecContext(ctx, `
		DELETE FROM page_webhooks
		 WHERE created_by_user_id = ?
		   AND panel_id IN (
		       SELECT pl.id FROM page_panels pl
		       JOIN pages p ON p.id = pl.page_id
		       WHERE p.workspace_id = ?)`,
		userID, wsID)
	if err != nil {
		return pageIdentityErasure{}, fmt.Errorf("revoke page_webhooks: %w", err)
	}
	if n, err = res.RowsAffected(); err != nil {
		return pageIdentityErasure{}, fmt.Errorf("revoke page_webhooks: rows affected: %w", err)
	}
	out.WebhooksRevoked = int(n)

	if err := tx.Commit(); err != nil {
		return pageIdentityErasure{}, fmt.Errorf("commit pages identity erasure: %w", err)
	}
	return out, nil
}
