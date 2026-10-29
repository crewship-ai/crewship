package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestMemberRemovalAuditPreservesPreviousPolicy(t *testing.T) {
	r := wmRemoveMemberSetup(t)
	r.h.SetJournal(nil)
	wmSeedCrew(t, r.db, r.wsID, "removed-crew", "Removed", "removed-crew")
	wmSeedCrewMember(t, r.db, "removed-crew", r.memberUser)
	execOrFatal(t, r.db, `INSERT INTO projects(id,workspace_id,name,slug) VALUES('removed-project',?,'Removed','removed-project')`, r.wsID)
	store := access.Store{DB: r.db}
	m, err := store.Membership(t.Context(), r.memberUser, r.wsID)
	if err != nil {
		t.Fatal(err)
	}
	rights := []access.Right{{Kind: "project", ID: "removed-project", Operation: "read"}}
	if _, err = store.Replace(t.Context(), r.ownerID, r.memberUser, r.wsID, "restricted", m, rights); err != nil {
		t.Fatal(err)
	}
	response := r.removeReq(t)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var raw string
	if err = r.db.QueryRow(`SELECT payload FROM journal_entries WHERE workspace_id=? AND entry_type='member.removed' ORDER BY seq DESC LIMIT 1`, r.wsID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	if err = json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatal(err)
	}
	previous, ok := data["previous_membership"].(map[string]any)
	if !ok || previous["role"] != "MEMBER" || previous["access_mode"] != "restricted" {
		t.Fatalf("missing previous membership: %s", raw)
	}
	grants, ok := previous["rights"].([]any)
	if !ok || len(grants) != 1 {
		t.Fatalf("lost previous grants: %s", raw)
	}
	crews, ok := previous["crew_memberships"].([]any)
	if !ok || len(crews) != 1 {
		t.Fatalf("lost scoped crew membership: %s", raw)
	}
	public := journal.SerializeEntry(journal.Entry{Type: journal.EntryMemberRemoved, Payload: data})["payload"].(map[string]any)
	if _, exists := public["previous_membership"]; exists {
		t.Fatal("private previous policy escaped through serialization")
	}
}

func TestProvisionAuditRecordsNonSecretProvenance(t *testing.T) {
	for _, kind := range []string{"new", "unclaimed", "claimed"} {
		t.Run(kind, func(t *testing.T) {
			h, actor, workspace := provisionRig(t)
			if kind != "new" {
				seedOtherUser(t, h, "existing-audited-provision", "provenance@example.test")
			}
			if kind == "claimed" {
				execOrFatal(t, h.db, `UPDATE users SET email_verified='2026-01-01T00:00:00Z' WHERE id='existing-audited-provision'`)
			}
			response := provisionReq(t, h, actor, workspace, "OWNER", `{"email":"provenance@example.test","role":"MEMBER"}`)
			if response.Code != 201 {
				t.Fatal(response.Code, response.Body.String())
			}
			var raw string
			if err := h.db.QueryRow(`SELECT payload FROM journal_entries WHERE workspace_id=? AND entry_type='member.added' ORDER BY seq DESC LIMIT 1`, workspace).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			if err := json.Unmarshal([]byte(raw), &data); err != nil {
				t.Fatal(err)
			}
			if data["created_user"] != (kind == "new") || data["setup_link_issued"] != (kind != "claimed") {
				t.Fatalf("missing provenance flags: %s", raw)
			}
			if strings.Contains(raw, "@") || strings.Contains(raw, "token") || strings.Contains(raw, "http") {
				t.Fatal("setup secret or address escaped into audit")
			}
		})
	}
}
