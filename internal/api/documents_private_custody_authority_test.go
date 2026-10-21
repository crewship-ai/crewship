package api

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/documents"
)

func TestDocumentStorePrivateCustodyRestrictedElsewhere(t *testing.T) {
	for _, credential := range []string{"session", "cli"} {
		t.Run(credential, func(t *testing.T) {
			rig := pagesTransferSetup(t)
			const page = "engine-private-custody"
			rig.seedPage(t, page, page, "Private document", rig.adminID, "")
			execOrFatal(t, rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
			request := ownershipRequest(t, rig, "POST", "/", "")
			if credential == "cli" {
				const token = "crewship_cli_engine_private_custody"
				execOrFatal(t, rig.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('engine-cli',?,'fixture',?,'STANDARD','["pages:read","pages:write"]')`, rig.adminID, hashStandard(token))
				request.Header.Set("Authorization", "Bearer "+token)
				request = request.WithContext(context.WithValue(request.Context(), ctxAuthKind, AuthKindCLIToken))
			}
			ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
			defer cancel()
			request = request.WithContext(ctx)
			store := documents.New(rig.db, documents.Options{})
			key := documents.Key{WorkspaceID: rig.wsID, PageID: page}
			allow := func(context.Context, *sql.Tx) (string, error) { return "user:" + rig.adminID, nil }
			if _, err := store.Init(ctx, key, 2, []byte("private-snapshot"), []byte(`[]`), allow); err != nil {
				t.Fatal(err)
			}
			lease, err := store.Acquire(ctx, key, "fixture", allow)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Checkpoint(ctx, key, lease, "private-pin", []byte("private-snapshot"), 0, allow); err != nil {
				t.Fatal(err)
			}
			authority := &documentAuthority{pages: NewPageHandler(rig.db, nil, newTestLogger()), store: store}
			rig.db.SetMaxOpenConns(1)
			read := func() {
				t.Helper()
				state, err := store.Read(ctx, key, authority.authorize(request, key, false, false))
				if err != nil || string(state.Snapshot) != "private-snapshot" {
					t.Errorf("native read: %v", err)
				}
				pins, err := store.Checkpoints(ctx, key, authority.authorize(request, key, false, false))
				// Init retains revision zero alongside the explicit checkpoint.
				if err != nil || len(pins) != 2 {
					t.Fatalf("native checkpoints count=%d: %v", len(pins), err)
				}
				for _, listed := range pins {
					id := listed["id"].(string)
					pin, err := store.CheckpointRead(ctx, key, id, authority.authorize(request, key, false, false))
					if err != nil || string(pin.Snapshot) != "private-snapshot" {
						t.Errorf("native checkpoint %q read: %v", id, err)
					}
				}
			}
			read() // Healthy actual Store calibration, before any restriction.
			if t.Failed() {
				t.FailNow()
			}
			execOrFatal(t, rig.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, rig.wsID, rig.adminID)
			execOrFatal(t, rig.db, `INSERT INTO workspaces(id,name,slug) VALUES('engine-other','Other','engine-other')`)
			execOrFatal(t, rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('engine-restricted','engine-other',?,'MEMBER','restricted')`, rig.adminID)
			read()
			if _, err := store.Acquire(ctx, key, "forbidden-writer", authority.authorize(request, key, true, false)); !errors.Is(err, errDocumentAccess) {
				t.Errorf("restricted write error=%v", err)
			}
			if _, err := store.Init(ctx, key, 2, []byte("forbidden"), []byte(`[]`), authority.authorize(request, key, true, true)); !errors.Is(err, errDocumentAccess) {
				t.Errorf("restricted initialize error=%v", err)
			}
			if credential == "cli" {
				execOrFatal(t, rig.db, `UPDATE cli_tokens SET revoked_at=CURRENT_TIMESTAMP WHERE id='engine-cli'`)
			} else {
				execOrFatal(t, rig.db, `UPDATE user_sessions SET revoked_at=CURRENT_TIMESTAMP WHERE id=?`, UserFromContext(request.Context()).SessionID)
			}
			if _, err := store.Read(ctx, key, authority.authorize(request, key, false, false)); !errors.Is(err, errDocumentAccess) {
				t.Errorf("revoked exact credential read error=%v", err)
			}
		})
	}
}
