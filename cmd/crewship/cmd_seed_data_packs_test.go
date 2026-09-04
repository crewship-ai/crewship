package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func packCrewIDs() map[string]string {
	return map[string]string{"ops": "crew-ops", "quality": "crew-quality", "engineering": "crew-eng"}
}

// Every pack file lands on its crew through the crew-files save route, under
// shared/, with the embedded bytes — before provisioning, when the host can
// still write the tree.
func TestSeedPackFiles_DeliversEveryFileToItsCrew(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	saved := map[string][]byte{}
	s.SetFallback(func(r *http.Request, body []byte) (int, []byte, string) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/files/save") {
			saved[r.URL.Path+"?path="+r.URL.Query().Get("path")] = append([]byte{}, body...)
			return 200, []byte(`{"status":"saved"}`), "application/json"
		}
		return 404, nil, ""
	})
	if err := seedPackFiles(context.Background(), covStubClient(s), packCrewIDs()); err != nil {
		t.Fatalf("seedPackFiles: %v", err)
	}
	want := 0
	for _, p := range seeddata.Packs {
		for _, f := range p.Files {
			want++
			key := "/api/v1/crews/" + packCrewIDs()[p.CrewSlug] + "/files/save?path=" + f.Dest
			got, ok := saved[key]
			if !ok {
				t.Errorf("%s not delivered (have %v)", key, keysOf(saved))
				continue
			}
			embedded, _ := seeddata.PackFileContent(f.Src)
			if string(got) != string(embedded) {
				t.Errorf("%s: delivered %d bytes, embedded %d", key, len(got), len(embedded))
			}
		}
	}
	if len(saved) != want {
		t.Errorf("saved %d files, want %d", len(saved), want)
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A crew that did not seed loses its files with a line saying so; the other
// packs still get theirs.
func TestSeedPackFiles_MissingCrewIsReportedNotFatal(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	puts := 0
	s.SetFallback(func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Method == http.MethodPut {
			puts++
			return 200, []byte(`{}`), "application/json"
		}
		return 404, nil, ""
	})
	ids := packCrewIDs()
	delete(ids, "ops")
	if err := seedPackFiles(context.Background(), covStubClient(s), ids); err != nil {
		t.Fatalf("seedPackFiles: %v", err)
	}
	total := 0
	for _, p := range seeddata.Packs {
		if p.CrewSlug != "ops" {
			total += len(p.Files)
		}
	}
	if puts != total {
		t.Errorf("PUTs = %d, want %d (every non-ops file)", puts, total)
	}
}

// With SEED_GITHUB_TOKEN set, each GitHub-backed pack gets a CREW-scoped
// credential (so the by-type resolver picks it over the inert demo
// accounts) and a GH_TOKEN binding on its crew; the demo vault then skips
// that slot.
func TestSeedPackCredentials_CrewScopedAndBound(t *testing.T) {
	t.Setenv("SEED_GITHUB_TOKEN", "ghp_seed_token_000000000000000000000")
	s := clitest.NewStubServer()
	defer s.Close()
	var created []map[string]any
	var bindings []map[string]string
	s.OnGet("/api/v1/credentials", clitest.JSONResponse(200, []map[string]any{}))
	s.OnPost("/api/v1/credentials", func(_ *http.Request, body []byte) (int, []byte, string) {
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		created = append(created, m)
		return 201, []byte(`{"id":"cred-` + m["name"].(string) + `"}`), "application/json"
	})
	s.OnPost("/api/v1/credentials/bindings", func(_ *http.Request, body []byte) (int, []byte, string) {
		var m map[string]string
		_ = json.Unmarshal(body, &m)
		bindings = append(bindings, m)
		return 201, []byte(`{}`), "application/json"
	})
	for k := range packBoundSlots {
		delete(packBoundSlots, k)
	}
	if err := seedPackCredentials(context.Background(), covStubClient(s), packCrewIDs()); err != nil {
		t.Fatalf("seedPackCredentials: %v", err)
	}
	wantPacks := 0
	for _, p := range seeddata.Packs {
		if packRequires(p, packGitHubEnv) {
			wantPacks++
		}
	}
	if len(created) != wantPacks {
		t.Fatalf("created %d credentials, want %d (one per GitHub-backed pack)", len(created), wantPacks)
	}
	for _, c := range created {
		if c["scope"] != "CREW" || c["crew_id"] == "" || c["type"] != "CLI_TOKEN" || c["provider"] != "GITHUB" {
			t.Errorf("credential %v must be a CREW-scoped GITHUB CLI_TOKEN", c["name"])
		}
		if c["value"] != "ghp_seed_token_000000000000000000000" {
			t.Errorf("credential %v does not carry the seed token", c["name"])
		}
	}
	if len(bindings) != wantPacks {
		t.Fatalf("bindings = %d, want %d", len(bindings), wantPacks)
	}
	for _, b := range bindings {
		if b["slot"] != "GH_TOKEN" || b["scope"] != "CREW" {
			t.Errorf("binding %v must be a CREW binding of GH_TOKEN", b)
		}
	}
	if !packBoundSlots["ops/GH_TOKEN"] || !packBoundSlots["quality/GH_TOKEN"] {
		t.Errorf("packBoundSlots = %v; the demo vault consults it to skip those slots", packBoundSlots)
	}
}

// Without the token nothing is created — an inert credential that looked
// real is the silent failure the packs exist to catch.
func TestSeedPackCredentials_NoTokenCreatesNothing(t *testing.T) {
	t.Setenv("SEED_GITHUB_TOKEN", "")
	s := clitest.NewStubServer()
	defer s.Close()
	posts := 0
	s.SetFallback(func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Method == http.MethodPost {
			posts++
		}
		return 404, nil, ""
	})
	for k := range packBoundSlots {
		delete(packBoundSlots, k)
	}
	if err := seedPackCredentials(context.Background(), covStubClient(s), packCrewIDs()); err != nil {
		t.Fatalf("seedPackCredentials: %v", err)
	}
	if posts != 0 {
		t.Errorf("POSTs = %d, want 0", posts)
	}
	if len(packBoundSlots) != 0 {
		t.Errorf("packBoundSlots = %v, want empty", packBoundSlots)
	}
}

func TestPackRunnable(t *testing.T) {
	t.Setenv("SEED_GITHUB_TOKEN", "")
	for _, p := range seeddata.Packs {
		ok, reason := packRunnable(p)
		if packRequires(p, packGitHubEnv) {
			if ok || !strings.Contains(reason, "SEED_GITHUB_TOKEN") {
				t.Errorf("pack %s: runnable=%v reason=%q, want not runnable naming the variable", p.Slug, ok, reason)
			}
		} else if !ok {
			t.Errorf("pack %s: has no requirements but is reported not runnable: %s", p.Slug, reason)
		}
	}
	t.Setenv("SEED_GITHUB_TOKEN", "x")
	for _, p := range seeddata.Packs {
		if ok, reason := packRunnable(p); !ok {
			t.Errorf("pack %s: %s", p.Slug, reason)
		}
	}
}
