package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestRestrictedCLIForwardsExplicitImmutableProjectVersions(t *testing.T) {
	want := []string{"version-second", "version-first"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/chats/private/restricted-cli-run" {
			t.Errorf("wrong execution route: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		var body struct {
			Content  string   `json:"content"`
			Versions []string `json:"project_file_versions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Content != "use selected inputs" || !reflect.DeepEqual(body.Versions, want) {
			t.Errorf("lost explicit input selection: %+v %v", body, err)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"done\"}\n\n")
	}))
	defer server.Close()
	client := &cli.Client{BaseURL: server.URL, WorkspaceID: "cworkspace00000000000001", HTTPClient: server.Client()}
	if err := runRestrictedText(client, "private", "use selected inputs", nil, nil, true, want); err != nil {
		t.Fatal(err)
	}
}
