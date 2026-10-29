package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestInvitationAuditFinalBusinessReceipt(t *testing.T) {
	for _, operation := range []string{"revoked", "reissued", "accepted"} {
		faults := []string{"healthy", "proof_undo", "policy_undo"}
		if operation == "accepted" {
			faults = append(faults, "membership_undo")
		}
		for _, fault := range faults {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				db := setupTestDB(t)
				owner, workspace := seedInviter(t, db)
				const subject = "receipt-invitee"
				execOrFatal(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, subject, "receipt-invitee@example.test")
				key := []byte("invitation-final-receipt-configured-key")
				writer := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{ChainKey: key, FlushInterval: time.Hour})
				defer writer.Close()
				observed := 0
				writer.AddCommitObserver(func(entries []journal.Entry) { observed += len(entries) })
				h := NewWorkspaceHandler(db, newTestLogger())
				h.SetJournal(writer)
				created := httptest.NewRecorder()
				h.CreateInvitation(created, withWorkspaceUser(httptest.NewRequest("POST", "/invitations", strings.NewReader(`{"email":"receipt-invitee@example.test","role":"MEMBER"}`)), owner, workspace, "OWNER"))
				if created.Code != 201 {
					t.Fatalf("healthy invitation creation HTTP%d", created.Code)
				}
				var inv invitationResponse
				if err := json.Unmarshal(created.Body.Bytes(), &inv); err != nil {
					t.Fatal("invalid native invitation response")
				}
				if observed != 1 {
					t.Fatalf("configured creation observer count%d", observed)
				}
				chain, err := journal.VerifyChainWithKey(t.Context(), db, workspace, key)
				if err != nil || !chain.OK {
					t.Fatal("configured creation chain calibration failed")
				}
				type storedInvitation struct {
					Proof, Expires, Mode, Rights string
					Accepted                     sql.NullString
				}
				read := func() storedInvitation {
					t.Helper()
					var s storedInvitation
					if err := db.QueryRow(`SELECT token,expires_at,access_mode,rights_json,accepted_at FROM workspace_invitations WHERE id=?`, inv.ID).Scan(&s.Proof, &s.Expires, &s.Mode, &s.Rights, &s.Accepted); err != nil {
						t.Fatal(err)
					}
					return s
				}
				before := read()
				if fault != "healthy" {
					execOrFatal(t, db, `CREATE TABLE invitation_before_receipt AS SELECT * FROM workspace_invitations WHERE id=?`, inv.ID)
					action := `UPDATE workspace_invitations SET token=(SELECT token FROM invitation_before_receipt),expires_at=(SELECT expires_at FROM invitation_before_receipt),accepted_at=NULL WHERE id=json_extract(NEW.payload,'$.invitation_id');`
					if fault == "policy_undo" {
						action = `UPDATE workspace_invitations SET access_mode='restricted',rights_json='[]' WHERE id=json_extract(NEW.payload,'$.invitation_id');`
					}
					if fault == "membership_undo" {
						action = `DELETE FROM workspace_members WHERE workspace_id=NEW.workspace_id AND user_id=NEW.actor_id;`
					}
					execOrFatal(t, db, `CREATE TRIGGER invitation_final_receipt AFTER INSERT ON journal_entries WHEN NEW.entry_type='invitation.`+operation+`' BEGIN `+action+` END`)
				}
				response := httptest.NewRecorder()
				if operation == "accepted" {
					authHandler := NewAuthHandler(db, newTestLogger(), nil, nil, false)
					authHandler.invitationJournal = writer
					authHandler.AcceptInvitation(response, withWorkspaceUser(httptest.NewRequest("POST", "/accept", strings.NewReader(`{"token":"`+inv.Token+`"}`)), subject, workspace, "MEMBER"))
				} else {
					req := withWorkspaceUser(httptest.NewRequest("POST", "/invitation", nil), owner, workspace, "OWNER")
					req.SetPathValue("invitationId", inv.ID)
					if operation == "revoked" {
						h.RevokeInvitation(response, req)
					} else {
						h.ReissueInvitation(response, req)
					}
				}
				after := read()
				var members, events int
				if err := db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE workspace_id=? AND user_id=? AND role='MEMBER' AND access_mode='trusted'`, workspace, subject).Scan(&members); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND entry_type=?`, workspace, "invitation."+operation).Scan(&events); err != nil {
					t.Fatal(err)
				}
				if fault != "healthy" {
					if response.Code != 500 || before != after || members != 0 || events != 0 || observed != 1 {
						t.Errorf("post-audit business undo committed: HTTP%d restored%t members%d events%d notifications%d", response.Code, before == after, members, events, observed)
					}
				} else {
					if response.Code != 200 || events != 1 || observed != 2 {
						t.Fatalf("healthy mutation calibration HTTP%d events%d notifications%d", response.Code, events, observed)
					}
					switch operation {
					case "revoked":
						if !strings.HasPrefix(after.Proof, "revoked:") || after.Expires != "1970-01-01T00:00:00Z" || members != 0 {
							t.Error("healthy revoke did not invalidate proof")
						}
					case "reissued":
						if after.Proof == before.Proof || after.Accepted.Valid || members != 0 {
							t.Error("healthy reissue did not replace proof")
						}
					case "accepted":
						if !after.Accepted.Valid || members != 1 {
							t.Error("healthy accept did not retain membership")
						}
					}
				}
				chain, err = journal.VerifyChainWithKey(t.Context(), db, workspace, key)
				if err != nil || !chain.OK {
					t.Error("final configured chain invalid")
				}
			})
		}
	}
}
