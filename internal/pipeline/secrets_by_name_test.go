package pipeline

import (
	"context"
	"testing"
)

// Multiple SECRET credentials in one crew: selection must be explicit, and a
// crew_delivery='granted' key must never be the answer to "a SECRET".
// Canary values only.
func TestVaultCredentialResolver_ByNameAndGranted(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncryptionKey)
	db := openPolicyTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedCredential(t, db, "GITLAB_TOKEN", "ws_test", "crew_ops", "SECRET", "ACTIVE", "canary-gitlab", "2026-01-01T00:00:00Z")
	seedCredential(t, db, "HEARTBEAT_URL", "ws_test", "crew_ops", "SECRET", "ACTIVE", "canary-heartbeat", "2026-01-02T00:00:00Z")
	seedCredential(t, db, "DEVICE_SSH_KEY", "ws_test", "crew_ops", "SECRET", "ACTIVE", "canary-ssh", "2026-01-03T00:00:00Z")
	if _, err := db.Exec(`UPDATE credentials SET crew_delivery = 'granted' WHERE id = 'DEVICE_SSH_KEY'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_credentials (id, agent_id, credential_id) VALUES ('g1', 'agent_specialist', 'DEVICE_SSH_KEY')`); err != nil {
		t.Fatal(err)
	}
	resolve, probe := NewVaultCredentialResolver(db), NewVaultCredentialProbe(db)
	human := RunScope{WorkspaceID: "ws_test", AuthorCrewID: "crew_ops"}
	lead := RunScope{WorkspaceID: "ws_test", AuthorCrewID: "crew_ops", AuthorAgentID: "agent_lead"}
	specialist := RunScope{WorkspaceID: "ws_test", AuthorCrewID: "crew_ops", AuthorAgentID: "agent_specialist"}
	otherCrew := RunScope{WorkspaceID: "ws_test", AuthorCrewID: "crew_devops"}

	t.Run("by type never selects the granted key", func(t *testing.T) {
		// The granted key is the NEWEST SECRET; before, it would have won.
		got, err := resolve(ctx, human, "SECRET")
		if err != nil || got != "canary-heartbeat" {
			t.Fatalf("got %q,%v; want the newest non-granted SECRET", got, err)
		}
	})
	for _, tc := range []struct {
		name  string
		scope RunScope
		ref   string
		want  string
	}{
		{"by name picks exactly that credential", human, "name.GITLAB_TOKEN", "canary-gitlab"},
		{"by name, older credential still selectable", lead, "name.GITLAB_TOKEN", "canary-gitlab"},
		{"granted key, human-authored routine", human, "name.DEVICE_SSH_KEY", "canary-ssh"},
		{"granted key, routine authored by the granted specialist", specialist, "name.DEVICE_SSH_KEY", "canary-ssh"},
		{"granted key, routine authored by the lead", lead, "name.DEVICE_SSH_KEY", ""},
		{"another crew cannot name it", otherCrew, "name.GITLAB_TOKEN", ""},
		{"unknown name resolves nothing", human, "name.NOPE", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := resolve(ctx, tc.scope, tc.ref)
			if got != tc.want {
				t.Fatalf("resolve(%s) = %q, want %q", tc.ref, got, tc.want)
			}
			ok, err := probe(ctx, tc.scope, tc.ref)
			if err != nil || ok != (tc.want != "") {
				t.Fatalf("probe(%s) = %v,%v; must agree with the resolver", tc.ref, ok, err)
			}
		})
	}
}

func TestSecretRefKey(t *testing.T) {
	for _, tc := range []struct {
		parts []string
		key   string
		ok    bool
	}{
		{[]string{"secrets", "API_KEY"}, "API_KEY", true},
		{[]string{"secrets", "name", "DEVICE_SSH_KEY"}, "name.DEVICE_SSH_KEY", true},
		{[]string{"secrets", "name"}, "", false},
		{[]string{"secrets", ""}, "", false},
	} {
		key, ok := secretRefKey(tc.parts)
		if key != tc.key || ok != tc.ok {
			t.Errorf("secretRefKey(%v) = %q,%v want %q,%v", tc.parts, key, ok, tc.key, tc.ok)
		}
	}
	types := secretTypesInStep(Step{Script: &ScriptStep{Env: map[string]string{"K": "{{ secrets.name.DEVICE_SSH_KEY }}", "T": "{{ secrets.SECRET }}"}}})
	if _, ok := types["name.DEVICE_SSH_KEY"]; !ok {
		t.Fatalf("by-name ref not scanned: %v", types)
	}
	if _, ok := types["SECRET"]; !ok {
		t.Fatalf("by-type ref not scanned: %v", types)
	}
	out := Render("{{ secrets.name.DEVICE_SSH_KEY }}", RenderContext{Secrets: map[string]string{"name.DEVICE_SSH_KEY": "canary"}})
	if out != "canary" {
		t.Fatalf("Render by-name = %q", out)
	}
}
