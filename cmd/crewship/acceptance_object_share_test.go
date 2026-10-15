package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestAcceptanceObjectShareCLI(t *testing.T) {
	for _, tc := range []struct {
		name           string
		args           []string
		mode           string
		status         int
		wantError      bool
		wantOperations []string
		wantReissue    bool
	}{
		{"share project by email", []string{"share", "project", "project-1", "--with", "CLIENT@example.test", "--operation", "list,read", "--format", "yaml"}, "restricted", 200, false, []string{"list", "read"}, false},
		{"share project management", []string{"share", "project", "project-1", "--with", "member-1", "--operation", "read,manage", "--format", "json"}, "restricted", 200, false, []string{"read", "manage"}, false},
		{"unshare routine by user", []string{"unshare", "routine", "routine-1", "--with", "user-1", "--operation", "run"}, "restricted", 200, false, []string{}, false},
		{"explicit reissue", []string{"share", "routine", "routine-1", "--with", "member-1", "--operation", "run,run", "--reissue"}, "restricted", 200, false, []string{"run"}, true},
		{"conflict no retry", []string{"unshare", "routine", "routine-1", "--with", "member-1", "--operation", "run"}, "restricted", 409, true, []string{}, false},
		{"trusted mode unchanged", []string{"share", "routine", "routine-1", "--with", "member-1", "--operation", "run"}, "trusted", 200, true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := access.Policy{Membership: access.Membership{ID: "member-1", Mode: tc.mode, Revision: 7}, Rights: []access.Right{
				{Kind: "agent", ID: "agent-1", Operation: "run"},
				{Kind: "routine", ID: "routine-1", Operation: "run"},
				{Kind: "routine", ID: "routine-1", Operation: "future-operation"},
			}, GrantDetails: []access.GrantDetail{{Right: access.Right{Kind: "routine", ID: "routine-1", Operation: "run"}, IssuerID: "old-admin", Live: false}}}
			var mu sync.Mutex
			var submitted access.Policy
			puts, policyReads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/workspaces":
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
				case "/api/v1/workspaces/ws_test/members":
					_, _ = io.WriteString(w, `[{"id":"member-1","access_mode":"restricted","user":{"id":"user-1","email":"client@example.test","full_name":"Client"}}]`)
				case "/api/v1/workspaces/ws_test/members/member-1/access":
					if r.Method == "GET" {
						policyReads++
						_ = json.NewEncoder(w).Encode(original)
						return
					}
					if r.Method != "PUT" {
						w.WriteHeader(405)
						return
					}
					puts++
					if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
						t.Error(err)
					}
					w.WriteHeader(tc.status)
					if tc.status == 409 {
						_, _ = io.WriteString(w, `{"error":"policy changed"}`)
						return
					}
					updated := submitted
					updated.Revision++
					_ = json.NewEncoder(w).Encode(updated)
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, "", tc.args...)
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v output=%s", err, out)
			}
			if tc.name == "share project by email" {
				var document map[string]any
				if err := yaml.Unmarshal([]byte(out), &document); err != nil {
					t.Fatal(err, out)
				}
				if document["membership_id"] != "member-1" || document["revision"] != 8 || document["mode"] != "restricted" {
					t.Fatalf("YAML snapshot keys differ: %s", out)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.mode == "trusted" {
				if puts != 0 || !strings.Contains(out, "never changes account mode") {
					t.Fatalf("trusted conversion: puts=%d %s", puts, out)
				}
				return
			}
			if puts != 1 || policyReads != 1 {
				t.Fatalf("unexpected retry/read: puts=%d reads=%d %s", puts, policyReads, out)
			}
			if submitted.Membership != original.Membership || len(submitted.GrantDetails) != 0 {
				t.Fatalf("lost snapshot or sent provenance: %+v", submitted)
			}
			if !slices.Contains(submitted.Rights, original.Rights[0]) || !slices.Contains(submitted.Rights, original.Rights[2]) {
				t.Fatalf("unrelated/future rights lost: %+v", submitted)
			}
			kind, id := tc.args[1], tc.args[2]
			for _, operation := range []string{"run", "view", "list", "read", "contribute", "manage"} {
				right := access.Right{Kind: kind, ID: id, Operation: operation}
				if slices.Contains(submitted.Rights, right) != slices.Contains(tc.wantOperations, operation) {
					t.Fatalf("wrong operation %s: %+v", operation, submitted)
				}
			}
			if tc.wantReissue {
				if len(submitted.Reissue) != 1 || submitted.Reissue[0] != original.Rights[1] {
					t.Fatalf("wrong reissue: %+v", submitted.Reissue)
				}
			} else if len(submitted.Reissue) != 0 {
				t.Fatalf("silent reissue: %+v", submitted.Reissue)
			}
		})
	}
}

func TestAcceptanceObjectShareValidationBeforeHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("invalid input reached HTTP: %s", r.URL.Path)
		w.WriteHeader(500)
	}))
	defer server.Close()
	for _, args := range [][]string{
		{"share", "project", "project-1", "--with", "member-1", "--operation", "write"},
		{"share", "routine", "..", "--with", "member-1", "--operation", "run"},
		{"unshare", "routine", "routine-1", "--with", "member-1"},
	} {
		out, err := runGuestCLI(t, server.URL, "", args...)
		if err == nil {
			t.Fatalf("accepted %v: %s", args, out)
		}
	}
}

func TestAcceptanceObjectShareListCLI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces":
			_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
		case "/api/v1/workspaces/ws_test/members":
			_, _ = io.WriteString(w, `[{"id":"member-1","access_mode":"restricted","email":"client@example.test"},{"id":"owner","access_mode":"trusted"}]`)
		case "/api/v1/workspaces/ws_test/members/member-1/access":
			_, _ = io.WriteString(w, `{"membership_id":"member-1","revision":7,"mode":"restricted","rights":[],"grant_details":[{"kind":"routine","id":"routine-1","operation":"run","issuer_id":"old-admin","created_at":"2026-10-14","live":false},{"kind":"agent","id":"foreign-agent","operation":"run","issuer_id":"owner","created_at":"2026-10-14","live":true}]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	out, err := runGuestCLI(t, server.URL, "", "share", "list", "routine", "routine-1")
	if err != nil {
		t.Fatalf("list: %v %s", err, out)
	}
	for _, text := range []string{"MEMBER", "EMAIL", "OPERATION", "ISSUER", "STATE", "old-admin", "inactive", "client@example.test"} {
		if !strings.Contains(out, text) {
			t.Fatalf("missing %s: %s", text, out)
		}
	}
	jsonOut, err := runGuestCLI(t, server.URL, "", "--format", "json", "share", "list", "routine", "routine-1")
	if err != nil {
		t.Fatal(err, jsonOut)
	}
	yamlOut, err := runGuestCLI(t, server.URL, "", "--format", "yaml", "share", "list", "routine", "routine-1")
	if err != nil {
		t.Fatal(err, yamlOut)
	}
	var jsonRows, yamlRows []map[string]any
	if err = json.Unmarshal([]byte(jsonOut), &jsonRows); err != nil {
		t.Fatal(err)
	}
	if err = yaml.Unmarshal([]byte(yamlOut), &yamlRows); err != nil {
		t.Fatal(err)
	}
	if len(jsonRows) != 1 || len(yamlRows) != 1 {
		t.Fatalf("wrong machine rows: %s / %s", jsonOut, yamlOut)
	}
	for _, key := range []string{"member_id", "email", "kind", "id", "operation", "issuer_id", "created_at", "live"} {
		if jsonRows[0][key] != yamlRows[0][key] {
			t.Fatalf("machine key %s mismatch: %s / %s", key, jsonOut, yamlOut)
		}
	}
	if strings.Contains(out, "foreign-agent") {
		t.Fatalf("listed unrelated grant: %s", out)
	}
}
