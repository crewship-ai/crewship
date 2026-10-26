package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestAcceptanceTrustedCrewManageCLI(t *testing.T) {
	for _, format := range []string{"table", "json", "yaml"} {
		for _, verb := range []string{"share", "unshare"} {
			t.Run(verb+"/"+format, func(t *testing.T) {
				other := access.Right{Kind: "crew", ID: "other-crew", Operation: "manage"}
				right := access.Right{Kind: "crew", ID: "crew-1", Operation: "manage"}
				original := access.Policy{Membership: access.Membership{ID: "member-1", Mode: "trusted", Revision: 7}, Rights: []access.Right{other}}
				if verb == "unshare" {
					original.Rights = append(original.Rights, right)
				}
				var submitted access.Policy
				var mu sync.Mutex
				puts := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/api/v1/workspaces":
						_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
					case "/api/v1/workspaces/ws_test/members":
						_, _ = io.WriteString(w, `[{"id":"member-1","role":"MEMBER","access_mode":"trusted","user":{"id":"user-1","email":"trusted@example.test"}}]`)
					case "/api/v1/workspaces/ws_test/members/member-1/access":
						if r.Method == "GET" {
							_ = json.NewEncoder(w).Encode(original)
							return
						}
						puts++
						if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil {
							t.Error(err)
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
				out, err := runGuestCLI(t, server.URL, "", verb, "crew", "crew-1", "--with", "member-1", "--operation", "manage", "--format", format)
				if err != nil {
					t.Fatal(err, out)
				}
				mu.Lock()
				defer mu.Unlock()
				if puts != 1 || submitted.Membership != original.Membership || !slices.Contains(submitted.Rights, other) || slices.Contains(submitted.Rights, right) != (verb == "share") || len(submitted.Reissue) != 0 {
					t.Fatalf("mode/provenance/replacement: puts=%d %+v", puts, submitted)
				}
			})
		}
	}
}

func TestAcceptanceTrustedCrewManageCLIListsInactiveRecipients(t *testing.T) {
	for _, role := range []string{"VIEWER", "ADMIN", "OWNER"} {
		t.Run(role, func(t *testing.T) {
			var reads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/workspaces":
					_, _ = io.WriteString(w, `[{"id":"ws_test","slug":"ws_test"}]`)
				case "/api/v1/workspaces/ws_test/members":
					_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "member-1", "role": role, "access_mode": "trusted"}})
				case "/api/v1/workspaces/ws_test/members/member-1/access":
					reads.Add(1)
					_, _ = io.WriteString(w, `{"membership_id":"member-1","revision":7,"mode":"trusted","rights":[{"kind":"crew","id":"crew-1","operation":"manage"}],"grant_details":[{"kind":"crew","id":"crew-1","operation":"manage","issuer_id":"original-admin","created_at":"original","live":false}]}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			out, err := runGuestCLI(t, server.URL, "", "share", "list", "crew", "crew-1", "--format", "json")
			var rows []objectShareRow
			if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0].Live || rows[0].IssuerID != "original-admin" || rows[0].CreatedAt != "original" || reads.Load() != 1 {
				t.Fatalf("retained inactive share hidden for %s: reads=%d output=%s err=%v", role, reads.Load(), out, err)
			}
		})
	}
}
