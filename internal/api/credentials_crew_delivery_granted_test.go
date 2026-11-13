package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// crew_delivery='granted': a crew owns a specialist's key without every crew
// member receiving it. A device-monitoring deployment hit exactly this — a
// CREW-scoped SSH key linked to the crew was delivered to the crew lead
// as source=crew_link, and `credential unassign` could not stop it because
// there was no row to remove. Values here are canaries, never real keys.

const grantedCanary = "canary-granted-not-a-real-key"

func TestCrewDelivery_GrantedReachesOnlyTheGrantedMember(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	lead, specialist := e.agentA, "cd-specialist"
	execOrFatal(t, db, `INSERT INTO agents (id, crew_id, workspace_id, name, slug) VALUES (?, ?, ?, 'Spec', 'spec')`, specialist, e.crewA, e.wsID)

	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-ssh", "DEVICE_SSH_KEY", grantedCanary)
	execOrFatal(t, db, `UPDATE credentials SET scope = 'CREW', crew_delivery = 'granted' WHERE id = 'cd-ssh'`)
	linkCredToCrew(t, db, "cd-ssh", e.crewA)
	assignCredToAgent(t, db, "cd-ssh", specialist, "DEVICE_SSH_KEY", 0)

	for _, path := range []struct {
		name string
		get  func(agent string) map[string]string
	}{
		{"boot", func(a string) map[string]string { return bootEnvValues(bootCreds(t, db, a)) }},
		{"delegation", func(a string) map[string]string { return delegationEnvValues(delegationCreds(t, db, a)) }},
		{"peer-query", func(a string) map[string]string { return delegationEnvValues(peerQueryCreds(t, db, a)) }},
	} {
		t.Run(path.name, func(t *testing.T) {
			if got := path.get(specialist); got["DEVICE_SSH_KEY"] != grantedCanary {
				t.Fatalf("specialist payload = %v, want its explicit grant delivered", credEnvNames(got))
			}
			if got := path.get(lead); got["DEVICE_SSH_KEY"] != "" {
				t.Fatalf("lead received the specialist's granted credential through the crew link")
			}
			if got := path.get(e.agentB); got["DEVICE_SSH_KEY"] != "" {
				t.Fatalf("a member of another crew received it")
			}
		})
	}

	t.Run("listing agrees with delivery", func(t *testing.T) {
		if rows := listAgentCreds(t, db, e.wsID, lead); hasCred(rows, "DEVICE_SSH_KEY") {
			t.Fatalf("lead's listing shows a credential it is not delivered: %+v", rows)
		}
		rows := listAgentCreds(t, db, e.wsID, specialist)
		if !hasCred(rows, "DEVICE_SSH_KEY") {
			t.Fatalf("specialist's listing misses its grant: %+v", rows)
		}
	})

	t.Run("sidecar grantees are narrow", func(t *testing.T) {
		g, err := loadCrewCredentialGrantees(context.Background(), db, specialist)
		if err != nil {
			t.Fatal(err)
		}
		got := g.grantedTo("cd-ssh", specialist)
		if len(got) != 1 || got[0] != specialist {
			t.Fatalf("grantees = %v, want only the specialist (nil would mean crew-wide)", got)
		}
	})

	t.Run("revoking the grant stops delivery", func(t *testing.T) {
		// Revoke the grant: with no explicit claim the credential must not be
		// reachable by the crew at all, even though the crew owns it.
		execOrFatal(t, db, `DELETE FROM agent_credentials WHERE credential_id = 'cd-ssh'`)
		if got := bootEnvValues(bootCreds(t, db, specialist)); got["DEVICE_SSH_KEY"] != "" {
			t.Fatalf("revoked granted credential still delivered via the crew link")
		}
	})
}

// The default is the historical behaviour: existing crews keep working.
func TestCrewDelivery_DefaultIsAllForExistingRows(t *testing.T) {
	db := setupTestDB(t)
	e := seedCrewDeliveryEnv(t, db)
	seedCredentialEnc(t, db, e.wsID, e.userID, "cd-legacy", "LEGACY_TOKEN", "canary-legacy")
	linkCredToCrew(t, db, "cd-legacy", e.crewA)
	var mode string
	if err := db.QueryRow(`SELECT crew_delivery FROM credentials WHERE id = 'cd-legacy'`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "all" {
		t.Fatalf("default crew_delivery = %q, want all", mode)
	}
	if got := bootEnvValues(bootCreds(t, db, e.agentA)); got["LEGACY_TOKEN"] != "canary-legacy" {
		t.Fatal("a default credential stopped reaching its crew")
	}
}

func TestCredentialCreate_CrewDeliveryValidation(t *testing.T) {
	for _, tc := range []struct {
		in   string
		ok   bool
		want string
	}{{"", true, "all"}, {"all", true, "all"}, {"GRANTED", true, "granted"}, {"agent", false, ""}} {
		got, ok := normalizeCrewDelivery(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("normalizeCrewDelivery(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if !strings.Contains(crewDeliveryHint, "granted") {
		t.Fatal("hint must name the accepted values")
	}
}

type listedCred struct {
	ID          string `json:"id"`
	CredName    string `json:"credential_name"`
	GrantSource string `json:"grant_source"`
}

func listAgentCreds(t *testing.T, db *sql.DB, wsID, agentID string) []listedCred {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/agents/"+agentID+"/credentials", nil)
	req.SetPathValue("agentId", agentID)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	NewAgentHandler(db, newTestLogger()).ListCredentials(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rr.Code, rr.Body.String())
	}
	var rows []listedCred
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return rows
}

func hasCred(rows []listedCred, name string) bool {
	for _, r := range rows {
		if r.CredName == name {
			return true
		}
	}
	return false
}

func credEnvNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
