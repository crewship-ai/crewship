package backup_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCraftNativeProposalBackupRoundTrip(t *testing.T) {
	for _, app := range []string{"wordcraft", "gridcraft"} {
		for _, mode := range []string{"same", "legacy-same", "fork-empty", "fork-beside-source"} {
			t.Run(app+"/"+mode, func(t *testing.T) {
				fork := mode != "same" && mode != "legacy-same"
				source := openDurableArtifactBackupDB(t)
				ws := seedWorkspace(t, source)
				scope := mooring.EditLeaseScope{WorkspaceID: ws, ArtifactID: "proposal-backup"}
				if _, err := source.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,'Native','u_admin','{}')`, scope.ArtifactID, ws, scope.ArtifactID); err != nil {
					t.Fatal(err)
				}
				principal := mooring.EditLeasePrincipal{ActorID: "u_admin", SessionID: "proposal-connection"}
				restoredStoreLeases := func(db *sql.DB) *mooring.EditLeaseStore {
					return mooring.NewEditLeaseStore(db, mooring.EditLeaseConfig{Enabled: true, Clock: recoveredOwnerClock{}, TTL: time.Minute, Authority: func(context.Context, *sql.Tx, mooring.EditLeaseAccess) error { return nil }})
				}
				store := func(db *sql.DB) *mooring.NativeStore {
					leases := restoredStoreLeases(db)
					return mooring.NewNativeStore(db, mooring.NativeStoreConfig{Enabled: true, Application: app, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Leases: leases, Authorize: func(context.Context, *sql.Tx, mooring.EditLeaseScope, string) (mooring.EditLeasePrincipal, error) {
						return principal, nil
					}, Validate: func(context.Context, []byte) error { return nil }})
				}
				lease, _, err := store(source).Initialize(t.Context(), scope, principal, []byte(`{"v":1}`))
				if err != nil {
					t.Fatal(err)
				}
				// Keep opaque native bytes byte-for-byte, including non-UTF-8 that
				// encoding/json accepts in strings but a text dump would replace.
				raw := []byte{'{', '"', 'v', '"', ':', '"', 0xff, '"', '}'}
				if mode == "legacy-same" {
					raw = []byte(`{"v":"legacy UTF-8 ž"}`)
				}
				hash := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
				if _, err := store(source).Save(t.Context(), lease, 1, []byte(`{"v":2}`)); err != nil {
					t.Fatal(err)
				}
				states := []string{"open", "rejected", "stale", "accepted"}
				for i, state := range states {
					id := fmt.Sprintf("ncp_%024x", i+1)
					baseVersion := int64(1)
					if state == "open" {
						baseVersion = 2
					}
					var decidedBy, decidedAt, resultVersion any
					if state == "accepted" {
						resultVersion = int64(2)
					}
					if state != "open" {
						decidedBy, decidedAt = "u_admin", "2026-10-30T00:00:00Z"
					}
					if _, err := source.Exec(`INSERT INTO craft_native_proposals(id,page_id,workspace_id,application_id,engine_pin,author_actor_id,request_id,base_version,summary,content_hash,content,state,decided_by,decided_at,result_version) VALUES(?,?,?,?,?,'agent:source',?,?,'Review this',?,?,?,?,?,?)`, id, scope.ArtifactID, ws, app, store(source).EnginePin(), state, baseVersion, hash, raw, state, decidedBy, decidedAt, resultVersion); err != nil {
						t.Fatal(err)
					}
				}
				actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
				created, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "proposal-roundtrip-passphrase", Actor: actor})
				if err != nil {
					t.Fatal(err)
				}
				target := openDurableArtifactBackupDB(t)
				opts := backup.RestoreOptions{Path: created.Path, Passphrase: "proposal-roundtrip-passphrase", Actor: actor}
				if mode == "fork-beside-source" {
					// First restore the original without transporting live capabilities.
					if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
						t.Fatal(err)
					}
				}
				if fork {
					opts.AsWorkspace = "proposal-fork"
				}
				if mode == "legacy-same" {
					dump, err := backup.DumpWorkspace(t.Context(), source, ws)
					if err != nil {
						t.Fatal(err)
					}
					for _, row := range dump.Tables["craft_native_proposals"] {
						row["content"] = string(raw)
					}
					if err := backup.RestoreDump(t.Context(), target, dump); err != nil {
						t.Fatal(err)
					}
				} else if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
					t.Fatal(err)
				}
				restored := scope
				if fork {
					if err := target.QueryRow(`SELECT workspace_id,page_id FROM craft_native_documents WHERE workspace_id<>?`, ws).Scan(&restored.WorkspaceID, &restored.ArtifactID); err != nil {
						t.Fatal(err)
					}
				}
				proposals, err := store(target).Proposals(t.Context(), restored)
				if err != nil || len(proposals) != len(states) {
					t.Fatalf("restored proposals: %+v, %v", proposals, err)
				}
				seen := map[string]bool{}
				for _, p := range proposals {
					if !regexp.MustCompile(`^ncp_[0-9a-f]{24}$`).MatchString(p.ID) || p.PageID != restored.ArtifactID {
						t.Errorf("proposal cannot be opened on restored Page: %+v", p)
					}
					seen[p.State] = true
					wantResult, wantBase := int64(0), int64(1)
					if p.State == "open" {
						wantBase = 2
					}
					if p.State == "accepted" {
						wantResult = 2
					}
					if p.AuthorActorID != "agent:source" || p.RequestID != p.State || p.Summary != "Review this" || p.BaseVersion != wantBase || p.ResultVersion != wantResult {
						t.Errorf("proposal metadata changed: %+v", p)
					}
					content, err := store(target).ProposalContent(t.Context(), restored, p.ID)
					if err != nil || !bytes.Equal(content.Content, raw) || content.ContentHash != hash {
						t.Errorf("proposal bytes/hash changed: %+v, %v", content, err)
					}
					var originalID string
					for i, state := range states {
						if state == p.State {
							originalID = fmt.Sprintf("ncp_%024x", i+1)
						}
					}
					if (p.ID == originalID) != (!fork) {
						t.Errorf("proposal identity fork policy: %s -> %s", originalID, p.ID)
					}
				}
				for _, p := range proposals {
					if p.State != "open" {
						continue
					}
					restoredStore := store(target)
					lease, err := restoredStoreLeases(target).Acquire(t.Context(), restored, principal)
					if err != nil {
						t.Fatal(err)
					}
					accepted, receipt, err := restoredStore.AcceptProposal(t.Context(), lease, p.ID)
					if err != nil || accepted.State != "accepted" || receipt.Version != "3" {
						t.Fatalf("restored proposal cannot be accepted: %+v %+v %v", accepted, receipt, err)
					}
				}
				for _, state := range states {
					if !seen[state] {
						t.Errorf("missing %s proposal", state)
					}
				}
				if mode == "fork-beside-source" {
					original, err := store(target).Proposals(t.Context(), scope)
					if err != nil || len(original) != len(states) {
						t.Fatalf("source proposals changed: %+v, %v", original, err)
					}
				}
			})
		}
	}
}
