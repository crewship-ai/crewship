package docker

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestContainerPathMetadataOnly(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
		fail             bool
	}{
		{"legacy", `{"Id":"cid","Config":{"Env":["UNRELATED=canary","PATH=/image/bin:/usr/bin"]}}`, "/image/bin:/usr/bin", 200, false},
		{"missing path", `{"Id":"cid","Config":{"Env":["UNRELATED=canary"]}}`, "", 200, false},
		{"missing config", `{"Id":"cid"}`, "", 200, true},
		{"unavailable", `{"message":"runtime busy"}`, "", 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newCovProviderTCP(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/containers/cid/json") {
					t.Errorf("unexpected operation %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			path, err := p.ContainerPath(context.Background(), "cid")
			if (err != nil) != tc.fail || path != tc.want {
				t.Fatalf("PATH=%q error=%v", path, err)
			}
		})
	}
}
