package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
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
				puts := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				if puts != 1 || submitted.Membership != original.Membership || !slices.Contains(submitted.Rights, other) || slices.Contains(submitted.Rights, right) != (verb == "share") || len(submitted.Reissue) != 0 {
					t.Fatalf("mode/provenance/replacement: puts=%d %+v", puts, submitted)
				}
			})
		}
	}
}
