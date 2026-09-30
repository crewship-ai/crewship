//go:build linux && !clionly

package main

import (
	"context"
	"errors"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"reflect"
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

type proofRestrictedRunner struct {
	selectedRestrictedRunner
	request  restricteddispatch.DelegatedRunRequest
	versions []string
}

func (r *proofRestrictedRunner) ExecuteWorkflowRun(_ context.Context, req restricteddispatch.DelegatedRunRequest, _ func(string, string) error) (restricteddispatch.RunProof, error) {
	r.calls = append(r.calls, "workflow")
	r.request = req
	return restricteddispatch.NewRunProof("host-proof", req.SourceEntryIDs), nil
}
func (r *proofRestrictedRunner) ExecuteWithProjectFiles(_ context.Context, _, _, _, _ string, versions []string, _ func(string, string) error) error {
	r.calls = append(r.calls, "project-chat")
	r.versions = append([]string(nil), versions...)
	return nil
}
func (r *proofRestrictedRunner) ExecuteRunWithProjectFiles(_ context.Context, _, _, _, _ string, versions []string, _ func(string, string) error) error {
	r.calls = append(r.calls, "project-run")
	r.versions = append([]string(nil), versions...)
	return nil
}
func TestRestrictedRegistryPreservesDelegationAndExplicitProjectInputs(t *testing.T) {
	db := testutil.MigratedSQLDB(t)
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('u','registry-proof@fixture.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('w','Registry','registry-proof')`,
		`INSERT INTO agents(id,workspace_id,name,slug,agent_role,restricted_execution_profile) VALUES('a','w','A','a','AGENT','responses_text'),('b','w','B','b','AGENT','native_api_key')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('c','w','a','u','private'),('n','w','b','u','private')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	text, native := &proofRestrictedRunner{}, &proofRestrictedRunner{}
	r := &restrictedExecutor{db: db, text: text, native: native}
	emit := func(string, string) error { return nil }
	req := restricteddispatch.DelegatedRunRequest{User: "u", Workspace: "w", Agent: "b", Chat: "c", ParentHandle: "parent-capability", Input: "frozen input", Rights: []access.Right{{Kind: "project", ID: "project-source", Operation: "read"}}, SourceEntryIDs: []string{"source-entry"}}
	proof, err := r.ExecuteWorkflowRun(t.Context(), req, emit)
	if err != nil || !reflect.DeepEqual(native.request, req) || !reflect.DeepEqual(proof.ContextIDs(), req.SourceEntryIDs) || len(text.calls) != 0 {
		t.Fatalf("lost target or authority: %v %+v %+v", err, native, text)
	}
	for _, tc := range []struct {
		name   string
		modify func(*restricteddispatch.DelegatedRunRequest)
	}{
		{"cross-agent without parent", func(q *restricteddispatch.DelegatedRunRequest) { q.ParentHandle = "" }},
		{"foreign workspace", func(q *restricteddispatch.DelegatedRunRequest) { q.Workspace = "foreign" }},
		{"unknown target", func(q *restricteddispatch.DelegatedRunRequest) { q.Agent = "missing" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := req
			tc.modify(&q)
			if _, err := r.ExecuteWorkflowRun(t.Context(), q, emit); !errors.Is(err, access.ErrDenied) {
				t.Fatalf("accepted invalid target: %v", err)
			}
		})
	}
	r.native = &selectedRestrictedRunner{}
	if _, err := r.ExecuteWorkflowRun(t.Context(), req, emit); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("accepted untyped workflow runner: %v", err)
	}
	r.native = native
	versions := []string{"exact-version-2", "exact-version-1"}
	if err := r.ExecuteRunWithProjectFiles(t.Context(), "u", "w", "n", "input", versions, emit); err != nil || !reflect.DeepEqual(native.versions, versions) {
		t.Fatalf("lost explicit versions: %v %+v", err, native.versions)
	}
	if err := r.ExecuteWithProjectFiles(t.Context(), "u", "w", "c", "input", versions, emit); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("text accepted project mount: %v", err)
	}
	if err := r.ExecuteWithProjectFiles(t.Context(), "u", "foreign", "n", "input", versions, emit); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("foreign project mount: %v", err)
	}
	if _, err := db.Exec(`UPDATE agents SET deleted_at='2026-09-30T00:00:00Z' WHERE id='b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ExecuteWorkflowRun(t.Context(), req, emit); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("deleted target executed: %v", err)
	}
	if len(text.calls) != 0 || len(native.calls) != 2 {
		t.Fatalf("denials fell back or executed: %+v %+v", text, native)
	}
}

type advertisedRestrictedRunner struct {
	proofRestrictedRunner
	profile string
}

func (r *advertisedRestrictedRunner) SupportsWorkflowProfile(profile string) bool {
	return profile == r.profile
}

func TestRestrictedRegistryWorkflowCapabilitiesRequireExactInstalledProfile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runner  *restrictedExecutor
		profile string
		want    bool
	}{
		{"absent registry", nil, "responses_text", false},
		{"untyped text", &restrictedExecutor{text: &selectedRestrictedRunner{}}, "responses_text", false},
		{"typed text compatibility", &restrictedExecutor{text: &proofRestrictedRunner{}}, "responses_text", true},
		{"native needs advertisement", &restrictedExecutor{native: &proofRestrictedRunner{}}, "native_api_key", false},
		{"native installed", &restrictedExecutor{native: &advertisedRestrictedRunner{profile: "native_api_key"}}, "native_api_key", true},
		{"wrong native advertisement", &restrictedExecutor{native: &advertisedRestrictedRunner{profile: "responses_text"}}, "native_api_key", false},
		{"no cross-profile fallback", &restrictedExecutor{text: &advertisedRestrictedRunner{profile: "native_api_key"}}, "native_api_key", false},
		{"login unavailable", &restrictedExecutor{native: &advertisedRestrictedRunner{profile: "native_api_key"}}, "native_account_login", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.runner.SupportsWorkflowProfile(tc.profile); got != tc.want {
				t.Fatalf("capability for %q = %v, want %v", tc.profile, got, tc.want)
			}
		})
	}
}
