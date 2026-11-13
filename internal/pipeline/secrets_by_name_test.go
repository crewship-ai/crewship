package pipeline

import (
	"context"
	"strings"
	"testing"
)

// Several credentials of one type visible to a crew: selection by type takes
// the newest, so a step that needs a specific one must name it. Canary
// values only.
func TestVaultCredentialResolver_ByName(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncryptionKey)
	db := openPolicyTestDB(t)
	defer db.Close()
	ctx := context.Background()
	seedCredential(t, db, "GITLAB_TOKEN", "ws_test", "crew_ops", "SECRET", "ACTIVE", "canary-gitlab", "2026-01-01T00:00:00Z")
	seedCredential(t, db, "HEARTBEAT_URL", "ws_test", "crew_ops", "SECRET", "ACTIVE", "canary-heartbeat", "2026-01-02T00:00:00Z")
	seedCredential(t, db, "DEVICE_SSH_KEY", "ws_test", "crew_ops", "SECRET", "ACTIVE", "canary-ssh", "2026-01-03T00:00:00Z")
	seedCredential(t, db, "OLD_KEY", "ws_test", "crew_ops", "SECRET", "PENDING", "canary-pending", "2026-01-04T00:00:00Z")
	resolve, probe, selectID := NewVaultCredentialResolver(db), NewVaultCredentialProbe(db), NewVaultCredentialIDResolver(db)
	ops := RunScope{WorkspaceID: "ws_test", AuthorCrewID: "crew_ops"}
	otherCrew := RunScope{WorkspaceID: "ws_test", AuthorCrewID: "crew_devops"}
	otherWorkspace := RunScope{WorkspaceID: "ws_other", AuthorCrewID: "crew_ops"}

	if got, err := resolve(ctx, ops, "SECRET"); err != nil || got != "canary-ssh" {
		t.Fatalf("by type = %q,%v; want the newest ACTIVE SECRET (unchanged behaviour)", got, err)
	}
	for _, tc := range []struct {
		name  string
		scope RunScope
		ref   string
		want  string
	}{
		{"by name picks exactly that credential", ops, "name.GITLAB_TOKEN", "canary-gitlab"},
		{"prefix is case-insensitive", ops, "NAME.HEARTBEAT_URL", "canary-heartbeat"},
		{"the name itself is exact", ops, "name.gitlab_token", ""},
		{"PENDING is never selected by name", ops, "name.OLD_KEY", ""},
		{"another crew cannot name it", otherCrew, "name.GITLAB_TOKEN", ""},
		{"another workspace cannot name it", otherWorkspace, "name.GITLAB_TOKEN", ""},
		{"unknown name resolves nothing", ops, "name.NOPE", ""},
		{"a type literally called name is not a by-name ref", ops, "name", ""},
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
			id, _ := selectID(ctx, tc.scope, tc.ref)
			if (id != "") != (tc.want != "") {
				t.Fatalf("selectID(%s) = %q; must agree with the resolver", tc.ref, id)
			}
		})
	}
}

func TestSecretRefKeyAndRender(t *testing.T) {
	for _, tc := range []struct {
		parts []string
		key   string
		ok    bool
	}{
		{[]string{"secrets", "API_KEY"}, "API_KEY", true},
		{[]string{"secrets", "name", "DEVICE_SSH_KEY"}, "name.DEVICE_SSH_KEY", true},
		{[]string{"secrets", "name"}, "", false},
		{[]string{"secrets", ""}, "", false},
		{[]string{"inputs", "x"}, "", false},
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
	secrets := map[string]string{"name.DEVICE_SSH_KEY": "canary-ssh", "SECRET": "canary-secret"}
	if out := Render("{{ secrets.name.DEVICE_SSH_KEY }}|{{ secrets.SECRET }}", RenderContext{Secrets: secrets}); out != "canary-ssh|canary-secret" {
		t.Fatalf("Render = %q", out)
	}
	// Before, secrets.name.X rendered the (absent) type "name": empty.
	if out := Render("{{ secrets.name.OTHER }}", RenderContext{Secrets: secrets}); out != "" {
		t.Fatalf("unresolved by-name ref rendered %q", out)
	}
	code := codeSecrets(secrets)
	if code["SECRET"] != "canary-secret" || code["name"].(map[string]any)["DEVICE_SSH_KEY"] != "canary-ssh" {
		t.Fatalf("code secrets = %v", code)
	}
}

// Case is kept for by-name refs everywhere a reference is normalized:
// credentials_required and the referenced-credential listing feed the
// dispatch gate and the dependents view, which match names exactly.
func TestNormalizeCredentialRef(t *testing.T) {
	for in, want := range map[string]string{
		"API_KEY":             "api_key",
		" Secret ":            "secret",
		"name.DEVICE_SSH_KEY": "name.DEVICE_SSH_KEY",
		"NAME.DEVICE_SSH_KEY": "name.DEVICE_SSH_KEY",
		"name.":               "name.",
	} {
		if got := NormalizeCredentialRef(in); got != want {
			t.Errorf("NormalizeCredentialRef(%q) = %q, want %q", in, got, want)
		}
	}
	d := &DSL{CredsRequired: []CredReq{{Type: "name.DEVICE_SSH_KEY"}, {Type: "API_KEY"}}}
	got := strings.Join(RequiredCredentialTypes(d), ",")
	if got != "name.DEVICE_SSH_KEY,api_key" {
		t.Fatalf("RequiredCredentialTypes = %s", got)
	}
}

func TestCheckCredentialRefShape(t *testing.T) {
	for ref, ok := range map[string]bool{
		"API_KEY":             true,
		"name.DEVICE_SSH_KEY": true,
		"secrets.API_KEY":     false,
		"api.key":             false,
		"name.":               false,
	} {
		if err := checkCredentialRefShape(ref); (err == nil) != ok {
			t.Errorf("checkCredentialRefShape(%q) = %v, want ok=%v", ref, err, ok)
		}
	}
}
