package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func TestInstallerCleanupRequiresOwnedRemovalAndConfirmedAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, owner        string
		removal, after     int
		wantOK, wantDelete bool
	}{
		{"confirmed", "fixture", 204, 404, true, true},
		{"foreign ownership", "peer", 204, 404, false, false},
		{"remove refused", "fixture", 403, 404, false, true},
		{"absence unknown", "fixture", 204, 403, false, true},
		{"container remains", "fixture", 204, 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deleted := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/owned") {
					deleted = true
					w.WriteHeader(tc.removal)
					if tc.removal != 204 {
						_, _ = w.Write([]byte(`{"message":"fixture denied"}`))
					}
					return
				}
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/owned/json") {
					if deleted && tc.after != 200 {
						w.WriteHeader(tc.after)
						_, _ = w.Write([]byte(`{"message":"fixture absence response"}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "owned", "Config": map[string]any{"Labels": map[string]string{"native.installer.fixture": tc.owner}}})
					return
				}
				t.Errorf("unexpected Docker route %s %s", r.Method, r.URL.Path)
				http.Error(w, "unexpected", 500)
			}))
			defer server.Close()
			daemon, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			defer daemon.Close()
			err = removeOwnedFixture(t.Context(), daemon, "owned", "fixture")
			if (err == nil) != tc.wantOK || deleted != tc.wantDelete {
				t.Fatalf("cleanup err=%v deleted=%v; want success=%v deleted=%v", err, deleted, tc.wantOK, tc.wantDelete)
			}
		})
	}
}
