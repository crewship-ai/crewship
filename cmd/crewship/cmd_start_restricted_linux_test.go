//go:build linux && !clionly

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/testutil"
)

type selectedRestrictedRunner struct{ calls []string }

func (r *selectedRestrictedRunner) Execute(context.Context, string, string, string, string, func(string, string) error) error {
	r.calls = append(r.calls, "chat")
	return nil
}
func (r *selectedRestrictedRunner) ExecuteRun(context.Context, string, string, string, string, func(string, string) error) error {
	r.calls = append(r.calls, "run")
	return nil
}

func TestRestrictedRegistrySelectsExactProfileWithoutFallback(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('registry-user','registry@fixture.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('registry-w','Registry','registry-w')`,
		`INSERT INTO agents(id,workspace_id,name,slug,agent_role,restricted_execution_profile) VALUES('registry-agent','registry-w','Registry','registry-agent','AGENT','responses_text')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('registry-chat','registry-w','registry-agent','registry-user','private')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	text, native := &selectedRestrictedRunner{}, &selectedRestrictedRunner{}
	r := &restrictedExecutor{db: db, text: text, native: native}
	emit := func(string, string) error { return nil }
	if err := r.Execute(t.Context(), "registry-user", "registry-w", "registry-chat", "hello", emit); err != nil {
		t.Fatal(err)
	}
	if len(text.calls) != 1 || text.calls[0] != "chat" || len(native.calls) != 0 {
		t.Fatalf("wrong conversational selection: %+v %+v", text, native)
	}
	if _, err := db.Exec(`UPDATE agents SET restricted_execution_profile='native_api_key' WHERE id='registry-agent'`); err != nil {
		t.Fatal(err)
	}
	if err := r.ExecuteRun(t.Context(), "registry-user", "registry-w", "registry-chat", "hello", emit); err != nil {
		t.Fatal(err)
	}
	if len(native.calls) != 1 || native.calls[0] != "run" {
		t.Fatalf("wrong native run selection: %+v", native)
	}
	if err := r.ExecuteRunWithRights(t.Context(), "registry-user", "registry-w", "registry-chat", "hello", []access.Right{{Kind: "project", ID: "source", Operation: "read"}}, emit); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("dropped project authority: %v", err)
	}
	r.native = nil
	if err := r.ExecuteRun(t.Context(), "registry-user", "registry-w", "registry-chat", "hello", emit); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("missing native fallback: %v", err)
	}
	if len(text.calls) != 1 || len(native.calls) != 1 {
		t.Fatal("denied request reached another profile")
	}
	if err := r.Execute(t.Context(), "registry-user", "another-workspace", "registry-chat", "hello", emit); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("foreign workspace selection: %v", err)
	}
}
