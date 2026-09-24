package backup_test

// #2274 regression: the five capability-token tables must survive a
// forked restore, and the fork must not inherit live capabilities.
//
// Before the fix, each of these tables lost every row on a fork: the
// source row still held the instance-unique token, INSERT OR IGNORE ate
// the fork's copy, and nothing said so. The fix re-mints the secret
// through the same digest primitive the auth layer verifies with
// (pipeline.HashCapabilityToken), so:
//
//   - the row LANDS (nothing is dropped),
//   - no source credential resolves against the fork,
//   - the operator is told which capabilities were re-keyed
//     (RestoreResult.CapabilityTokensReminted), because a webhook or
//     public link that arrives revoked is a fact, not a footnote.
//
// workspace_invitations is the one token stored in the clear, so its
// re-mint is FUNCTIONAL: the fork's admin re-reads the new link from
// the invitation list. The digest-only tables arrive with a capability
// nobody can present — deliberately; see remap_capability_tokens.go.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// mintTestToken produces nBytes of hex entropy — the invitation token
// shape internal/api mints.
func mintTestToken(t *testing.T, nBytes int) string {
	t.Helper()
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// forkCapabilityFixture seeds one row into each of the five
// capability-token tables with REALISTIC secrets (the shapes and digest
// scheme the production writers produce), on top of the every-table
// generator's scaffolding for the FK parents. Returns the cleartext
// tokens it minted, so the assertions can prove the fork's digests do
// not resolve them.
type forkCapabilityFixture struct {
	invitationToken string // cleartext, stored in the row (v01 schema)
	exposeToken     string // preimage of port_exposures.token_hash (#1888)
	webhookToken    string // preimage of pipeline_webhooks.token_hash
	pagePublicToken string // preimage of page_public_tokens.token_hash
	pageWebhookTok  string // preimage of page_webhooks.token_hash
}

func seedCapabilityRows(t *testing.T, db *sql.DB, workspaceID string) forkCapabilityFixture {
	t.Helper()
	ctx := context.Background()
	f := forkCapabilityFixture{
		invitationToken: mintTestToken(t, 32),
		exposeToken:     "exp_" + mintTestToken(t, 32),
		webhookToken:    "wh_" + mintTestToken(t, 32),
		pagePublicToken: "pub_" + mintTestToken(t, 32),
		pageWebhookTok:  "pw_" + mintTestToken(t, 32),
	}

	// workspace_invitations: cleartext token in the row, as the v01
	// schema and the invitation API still store it.
	if _, err := db.ExecContext(ctx,
		`UPDATE workspace_invitations SET token = ?, email = 'invitee@example.test', role = 'MEMBER' WHERE workspace_id = ?`,
		f.invitationToken, workspaceID); err != nil {
		t.Fatalf("seed invitation: %v", err)
	}
	// port_exposures / pipeline_webhooks: #1888 leaves the dead marker
	// in the cleartext column and the real secret only as a digest.
	if _, err := db.ExecContext(ctx,
		`UPDATE port_exposures SET token = 'redacted:' || id, token_hash = ? WHERE workspace_id = ?`,
		pipeline.HashCapabilityToken(f.exposeToken), workspaceID); err != nil {
		t.Fatalf("seed exposure: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE pipeline_webhooks SET token = 'redacted:' || id, token_hash = ? WHERE workspace_id = ?`,
		pipeline.HashCapabilityToken(f.webhookToken), workspaceID); err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	// page_*: hash-only by design.
	if _, err := db.ExecContext(ctx,
		`UPDATE page_public_tokens SET token_hash = ? WHERE id IN (SELECT pt.id FROM page_public_tokens pt JOIN pages p ON p.id = pt.page_id WHERE p.workspace_id = ?)`,
		pipeline.HashCapabilityToken(f.pagePublicToken), workspaceID); err != nil {
		t.Fatalf("seed page public token: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE page_webhooks SET token_hash = ? WHERE id IN (SELECT wh.id FROM page_webhooks wh JOIN page_panels pan ON pan.id = wh.panel_id JOIN pages p ON p.id = pan.page_id WHERE p.workspace_id = ?)`,
		pipeline.HashCapabilityToken(f.pageWebhookTok), workspaceID); err != nil {
		t.Fatalf("seed page webhook: %v", err)
	}
	return f
}

func queryStringValue(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	var v sql.NullString
	if err := db.QueryRowContext(context.Background(), q, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return v.String
}

func threeStrings(t *testing.T, db *sql.DB, q string, args ...any) (string, string, string) {
	t.Helper()
	var a, b, c sql.NullString
	if err := db.QueryRowContext(context.Background(), q, args...).Scan(&a, &b, &c); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return a.String, b.String, c.String
}

func TestForkedRestore_CapabilityTokens(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	seedLiveMission(t, source, workspaceID)
	seedRowPerBackupTable(t, source, workspaceID)
	fixture := seedCapabilityRows(t, source, workspaceID)

	const passphrase = "fork-capability-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   t.TempDir(),
		Actor:       actor,
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	res, err := backup.RestoreBackup(ctx, source, backup.RestoreOptions{
		Path:        created.Path,
		Passphrase:  passphrase,
		Actor:       actor,
		AsWorkspace: "e2e-ws-capability-fork",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}
	forkID := res.RestoredWorkspaceID
	if forkID == "" || forkID == workspaceID {
		t.Fatalf("--as-workspace did not fork (got %q)", forkID)
	}

	assertNoFKViolations(t, source, "after capability-token fork")

	// --- workspace_invitations: row lands, with a WORKING fresh token.
	forkInviteToken := queryStringValue(t, source,
		`SELECT token FROM workspace_invitations WHERE workspace_id = ?`, forkID)
	if forkInviteToken == "" {
		t.Fatal("fork lost its invitation row — the #2274 drop is back")
	}
	if forkInviteToken == fixture.invitationToken {
		t.Fatal("fork inherited the source's invitation token: the same invite link would join two workspaces")
	}
	if len(forkInviteToken) != 64 { // 32 bytes hex — the API's mint shape
		t.Errorf("fork invitation token is %d chars, want the 64-hex API shape so the link is re-displayable", len(forkInviteToken))
	}
	// The token is the redeem lookup key: the fork's token resolves to
	// the fork's row only, the source's to the source's only.
	if got := queryStringValue(t, source,
		`SELECT workspace_id FROM workspace_invitations WHERE token = ?`, forkInviteToken); got != forkID {
		t.Errorf("fork invitation token resolves to workspace %q, want the fork %q", got, forkID)
	}
	if got := queryStringValue(t, source,
		`SELECT workspace_id FROM workspace_invitations WHERE token = ?`, fixture.invitationToken); got != workspaceID {
		t.Errorf("source invitation token resolves to workspace %q, want the source %q — the fork must not touch the source row", got, workspaceID)
	}

	// --- port_exposures / pipeline_webhooks: row lands with the dead
	// redaction marker (self-consistent with its NEW row id) and a
	// digest that resolves no token the source ever published.
	for _, table := range []string{"port_exposures", "pipeline_webhooks"} {
		rowID, forkToken, forkHash := threeStrings(t, source,
			fmt.Sprintf(`SELECT id, token, COALESCE(token_hash, '') FROM %s WHERE workspace_id = ?`, table), forkID)
		if forkToken == "" {
			t.Fatalf("%s: fork lost its row — the #2274 drop is back", table)
		}
		// The marker embeds the row's OWN (new) id — the same convention
		// the #1888 write path uses — so it is unique and traceable.
		if want := "redacted:" + rowID; forkToken != want {
			t.Errorf("%s: fork token %q, want the redaction marker %q", table, forkToken, want)
		}
		if !pipeline.IsCapabilityTokenDigest(forkHash) {
			t.Errorf("%s: fork token_hash %q is not a capability digest", table, forkHash)
		}
		var sourcePreimage string
		if table == "port_exposures" {
			sourcePreimage = fixture.exposeToken
		} else {
			sourcePreimage = fixture.webhookToken
		}
		if forkHash == pipeline.HashCapabilityToken(sourcePreimage) {
			t.Errorf("%s: the source's live token still works against the fork — the fork inherited a live capability", table)
		}
		// The source row is untouched.
		srcHash := queryStringValue(t, source,
			fmt.Sprintf(`SELECT COALESCE(token_hash, '') FROM %s WHERE workspace_id = ?`, table), workspaceID)
		if srcHash != pipeline.HashCapabilityToken(sourcePreimage) {
			t.Errorf("%s: source row's digest changed during the fork", table)
		}
	}

	// --- page_public_tokens / page_webhooks: hash-only rows land with
	// digests that resolve nothing.
	pageChecks := []struct {
		table     string
		preimage  string
		forkQuery string
		srcQuery  string
	}{
		{
			table:     "page_public_tokens",
			preimage:  fixture.pagePublicToken,
			forkQuery: `SELECT pt.token_hash FROM page_public_tokens pt JOIN pages p ON p.id = pt.page_id WHERE p.workspace_id = ?`,
			srcQuery:  `SELECT pt.token_hash FROM page_public_tokens pt JOIN pages p ON p.id = pt.page_id WHERE p.workspace_id = ?`,
		},
		{
			table:     "page_webhooks",
			preimage:  fixture.pageWebhookTok,
			forkQuery: `SELECT wh.token_hash FROM page_webhooks wh JOIN page_panels pan ON pan.id = wh.panel_id JOIN pages p ON p.id = pan.page_id WHERE p.workspace_id = ?`,
			srcQuery:  `SELECT wh.token_hash FROM page_webhooks wh JOIN page_panels pan ON pan.id = wh.panel_id JOIN pages p ON p.id = pan.page_id WHERE p.workspace_id = ?`,
		},
	}
	for _, c := range pageChecks {
		forkHash := queryStringValue(t, source, c.forkQuery, forkID)
		if forkHash == "" {
			t.Fatalf("%s: fork lost its row — the #2274 drop is back", c.table)
		}
		if !pipeline.IsCapabilityTokenDigest(forkHash) {
			t.Errorf("%s: fork token_hash %q is not a capability digest", c.table, forkHash)
		}
		if forkHash == pipeline.HashCapabilityToken(c.preimage) {
			t.Errorf("%s: the source's live token still works against the fork", c.table)
		}
		if srcHash := queryStringValue(t, source, c.srcQuery, workspaceID); srcHash != pipeline.HashCapabilityToken(c.preimage) {
			t.Errorf("%s: source row's digest changed during the fork", c.table)
		}
	}

	// --- the restore says what it did: one re-keyed row per table,
	// reported by name so an API caller with no Logger still sees it.
	wantReminted := map[string]int{
		"workspace_invitations": 1,
		"port_exposures":        1,
		"pipeline_webhooks":     1,
		"page_public_tokens":    1,
		"page_webhooks":         1,
	}
	if !reflect.DeepEqual(res.CapabilityTokensReminted, wantReminted) {
		t.Errorf("CapabilityTokensReminted = %v, want %v", res.CapabilityTokensReminted, wantReminted)
	}
}
