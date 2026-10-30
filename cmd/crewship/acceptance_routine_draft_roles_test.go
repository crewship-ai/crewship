package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func routineStoredDraftDefinition(t *testing.T, db *sql.DB, slug string) string {
	t.Helper()
	var document string
	if err := db.QueryRow(`SELECT document_json FROM pipeline_drafts WHERE workspace_id=? AND slug=?`, routineTriggerAcceptanceWorkspaceID, slug).Scan(&document); err != nil {
		t.Fatal(err)
	}
	var doc struct{ Definition json.RawMessage }
	if err := json.Unmarshal([]byte(document), &doc); err != nil {
		t.Fatal(err)
	}
	return string(doc.Definition)
}

// Pretty CLI envelopes may add whitespace, but key order and character
// escaping must retain the saved definition's publication-proof identity.
func assertRoutineDraftDefinition(t *testing.T, document json.RawMessage, want string) {
	t.Helper()
	var doc struct{ Definition json.RawMessage }
	if err := json.Unmarshal(document, &doc); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc.Definition); err != nil {
		t.Fatal(err)
	}
	if compact.String() != want {
		t.Fatalf("get changed saved definition bytes:\ngot  %s\nwant %s", compact.String(), want)
	}
}

func assertRoutinePublishedDefinition(t *testing.T, db *sql.DB, slug, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT definition_json FROM pipelines WHERE workspace_id=? AND slug=?`, routineTriggerAcceptanceWorkspaceID, slug).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("publish changed saved definition bytes:\ngot  %s\nwant %s", got, want)
	}
}

func TestAcceptance_RoutineDraftMemberDenied(t *testing.T) {
	owner, _, db, serverURL := startRoutineTriggerAcceptanceServerDB(t)
	member := addRoutineAcceptanceMember(t, db, serverURL, "draft-member", "MEMBER")
	file := writeRoutineDraftEnvelope(t, "member-boundary")
	out, stderr, code := runRoutineCLISplit(t, owner, "routine", "draft", "save", file, "-f", "json")
	if code != 0 {
		t.Fatalf("owner save: %d %s%s", code, out, stderr)
	}
	var saved routineDraftEnvelope
	decodeRoutineJSON(t, "owner save", out, &saved)
	// Publish reads an already saved revision, rather than failing local file
	// validation before it ever reaches the permission boundary.
	if err := os.WriteFile(file, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"routine", "draft", "list"},
		{"routine", "draft", "get", "member-boundary"},
		{"routine", "draft", "save", file},
		{"routine", "draft", "discard", file},
		{"routine", "draft", "publish", file},
	} {
		t.Run(strings.Join(args[1:3], "/"), func(t *testing.T) {
			out, stderr, code := runRoutineCLISplit(t, member, args...)
			if code != 4 || !strings.Contains(stderr, "403") {
				t.Fatalf("MEMBER %v: exit %d, want forbidden exit 4: %s%s", args, code, out, stderr)
			}
		})
	}
	// CLI publish stops at test_run's 403. Also check the final publish route
	// directly so a missing authorization check there cannot hide behind it.
	raw, err := os.ReadFile(member)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "token: "); ok {
			token = v
		}
	}
	if token == "" {
		t.Fatal("missing fixture token")
	}
	publication, err := json.Marshal(map[string]any{"id": saved.ID, "revision": saved.Revision})
	if err != nil {
		t.Fatal(err)
	}
	base := serverURL + "/api/v1/workspaces/" + routineTriggerAcceptanceWorkspaceID + "/pipelines"
	for _, tc := range []struct{ path, body string }{
		{"/test_run", `{"author_crew_id":"trg-crew","definition":` + routineDraftDefinition("member-boundary") + `}`},
		{"/member-boundary/publish", string(publication)},
	} {
		req, err := http.NewRequest(http.MethodPost, base+tc.path, strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("MEMBER POST %s: %d, want 403; body=%s err=%v", tc.path, resp.StatusCode, body, err)
		}
	}
	if revision := listedDraftRevision(t, owner, "member-boundary"); revision != saved.Revision {
		t.Fatalf("denied operations changed draft revision to %d", revision)
	}
	var published int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE workspace_id=? AND slug=?`, routineTriggerAcceptanceWorkspaceID, "member-boundary").Scan(&published); err != nil || published != 0 {
		t.Fatalf("denied publication created live routine: count=%d err=%v", published, err)
	}
}
