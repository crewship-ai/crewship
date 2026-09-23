package pipeline

import (
	"context"
	"reflect"
	"testing"
)

func TestReferencedCredentialTypesNestedAndDeduplicated(t *testing.T) {
	d := &DSL{
		CredsRequired: []CredReq{{Type: " API_KEY "}},
		Steps: []Step{{HTTP: &HTTPStep{
			CredentialRef: &CredentialRef{Type: "api_key"},
			Headers:       map[string]string{"Authorization": "{{ secrets.GENERIC_SECRET }}"},
		}, Hooks: &StepHooks{Before: &Step{Code: &CodeStep{Code: "{{ secrets.DB_PASSWORD }}"}}}},
			{Foreach: &ForeachStep{Steps: []Step{{HTTP: &HTTPStep{Body: "{{ secrets.OTHER }}"}}}}}},
		Hooks: &RoutineHooks{BeforeAll: &Step{Notify: &NotifyStep{Body: "{{ secrets.NOTIFY }}"}}},
	}
	want := []string{"api_key", "db_password", "generic_secret", "notify", "other"}
	if got := ReferencedCredentialTypes(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := ReferencedCredentialTypes(nil); got != nil {
		t.Fatalf("nil DSL should have no references, got %v", got)
	}
}

func TestVaultCredentialIDPreviewMatchesRuntimeSelection(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", testEncryptionKey)
	db := openPolicyTestDB(t)
	defer db.Close()
	seedCredential(t, db, "workspace", "ws_test", "", "API_KEY", "ACTIVE", "workspace-secret", "2026-01-03T00:00:00Z")
	seedCredential(t, db, "crew", "ws_test", "crew-a", "API_KEY", "ACTIVE", "crew-secret", "2026-01-01T00:00:00Z")
	seedCredential(t, db, "pending", "ws_test", "crew-a", "API_KEY", "PENDING", "placeholder", "2026-01-04T00:00:00Z")
	selectID := NewVaultCredentialIDResolver(db)
	resolve := NewVaultCredentialResolver(db)
	for _, tc := range []struct{ crew, id, value string }{
		{"crew-a", "crew", "crew-secret"}, {"crew-b", "workspace", "workspace-secret"},
	} {
		scope := RunScope{WorkspaceID: "ws_test", AuthorCrewID: tc.crew}
		id, err := selectID(context.Background(), scope, "api_key")
		if err != nil || id != tc.id {
			t.Fatalf("crew=%s id=%s err=%v", tc.crew, id, err)
		}
		value, err := resolve(context.Background(), scope, "api_key")
		if err != nil || value != tc.value {
			t.Fatalf("crew=%s value=%s err=%v", tc.crew, value, err)
		}
	}
}
