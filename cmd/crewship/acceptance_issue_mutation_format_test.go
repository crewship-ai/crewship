package main

// Acceptance for the `-f json` contract on the issue WRITE surface, driven
// through the BUILT BINARY with stdout and stderr kept apart.
//
// The static guard in cli_format_contract_test.go
// (TestMutatingCommandsReturnTheirResultMachineReadably) proves these
// commands RESOLVE the format. It cannot prove the bytes are right, and the
// two halves fail differently: a command that resolves the format and then
// renders the wrong value passes the guard and still breaks every caller.
//
// So this test asserts what an agent actually needs:
//
//   - STDOUT ALONE parses as JSON. CombinedOutput would hide the whole
//     defect class — the receipt on stderr is fine, the receipt mixed into
//     the document is not, and only separated streams tell them apart.
//   - the created issue's `id` and `identifier` survive. Those are the
//     load-bearing fields: `identifier` is what the next command addresses
//     the issue by, and before this it existed only inside the English
//     sentence "Created issue ENG-14: …".
//   - the default (human) rendering is unchanged, receipt still on stderr,
//     stdout still empty. A fix that silently moved the human receipt onto
//     stdout would break `crewship issue create | …` for everyone who has
//     one, so it is asserted rather than assumed.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const issueMutationFixture = `{
	"id": "iss_created_1",
	"crew_id": "crew_1",
	"crew_slug": "engineering",
	"identifier": "ENG-14",
	"title": "ZZ fmt probe",
	"status": "BACKLOG",
	"priority": "medium",
	"mission_type": "STANDARD",
	"created_at": "2026-09-05T09:00:00Z",
	"updated_at": "2026-09-05T09:00:00Z"
}`

// startIssueMutationStub answers the whole issue write surface: the crew
// lookup `--crew` needs, the issue lookup every <identifier> command starts
// with, and the five mutating endpoints.
func startIssueMutationStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/crews":
			_, _ = w.Write([]byte(`[{"id":"crew_1","slug":"engineering","name":"Engineering"}]`))

		// POST /crews/{id}/issues — create
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews/crew_1/issues":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(issueMutationFixture))

		// The <identifier> commands resolve the issue first.
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/issues/ENG-14":
			_, _ = w.Write([]byte(issueMutationFixture))

		// PATCH — update. Echoes the whole updated issue, as the server does.
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/crews/crew_1/issues/ENG-14":
			_, _ = w.Write([]byte(strings.Replace(issueMutationFixture,
				`"status": "BACKLOG"`, `"status": "IN_PROGRESS"`, 1)))

		// POST comments — the 201 is the created comment.
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews/crew_1/issues/ENG-14/comments":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"cmt_1","mission_id":"iss_created_1","author_type":"user",
				"author_id":"user_1","author_name":"Pavel","body":"hi",
				"created_at":"2026-09-05T09:01:00Z","updated_at":"2026-09-05T09:01:00Z"}`))

		// DELETE — 204, no body: the receipt is synthesised by the CLI.
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/crews/crew_1/issues/ENG-14":
			w.WriteHeader(http.StatusNoContent)

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews/crew_1/issues/ENG-14/start":
			_, _ = w.Write([]byte(`{"status":"IN_PROGRESS","identifier":"ENG-14"}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews/crew_1/issues/ENG-14/stop":
			_, _ = w.Write([]byte(`{"status":"CANCELLED","identifier":"ENG-14","runs_stopped":2,"hard":false}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/crews/crew_1/issues/ENG-14/review":
			_, _ = w.Write([]byte(`{"status":"ok","action":"approve"}`))

		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"no stub for ` + r.Method + " " + r.URL.Path + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runIssueMutationCLI runs the built binary and returns stdout and stderr
// SEPARATELY. That separation is the whole point of this file.
func runIssueMutationCLI(t *testing.T, serverURL string, args ...string) (string, string, error) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + serverURL + "\nworkspace: ws_test\ntoken: fake-token\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// decodeStdout fails the test with the offending bytes when stdout is not a
// single JSON document — the failure an agent sees is "unexpected token", so
// the test should report exactly what it was handed.
func decodeStdout(t *testing.T, stdout, stderr string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\nstdout: %q\nstderr: %q", err, stdout, stderr)
	}
	return got
}

func TestAcceptance_IssueCreate_JSON_CarriesIdentifierAndID(t *testing.T) {
	srv := startIssueMutationStub(t)

	stdout, stderr, err := runIssueMutationCLI(t, srv.URL,
		"issue", "create", "--title", "ZZ fmt probe", "--crew", "engineering", "-f", "json")
	if err != nil {
		t.Fatalf("issue create: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	got := decodeStdout(t, stdout, stderr)
	if got["identifier"] != "ENG-14" {
		t.Errorf("identifier = %v, want ENG-14 — this is the field the next command addresses the issue by", got["identifier"])
	}
	if got["id"] != "iss_created_1" {
		t.Errorf("id = %v, want iss_created_1", got["id"])
	}
	// Same shape as `issue get -f json`, so one parser handles both.
	if got["title"] != "ZZ fmt probe" || got["status"] != "BACKLOG" {
		t.Errorf("created issue not rendered as the full issue object: %v", got)
	}
}

func TestAcceptance_IssueCreate_HumanReceiptStaysOnStderr(t *testing.T) {
	srv := startIssueMutationStub(t)

	stdout, stderr, err := runIssueMutationCLI(t, srv.URL,
		"issue", "create", "--title", "ZZ fmt probe", "--crew", "engineering")
	if err != nil {
		t.Fatalf("issue create: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "Created issue ENG-14") {
		t.Errorf("human receipt missing from stderr; stderr = %q", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("the human path must leave stdout empty, got %q", stdout)
	}
}

func TestAcceptance_IssueUpdate_JSON_ReturnsTheUpdatedIssue(t *testing.T) {
	srv := startIssueMutationStub(t)

	stdout, stderr, err := runIssueMutationCLI(t, srv.URL,
		"issue", "update", "ENG-14", "--priority", "low", "-f", "json")
	if err != nil {
		t.Fatalf("issue update: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	got := decodeStdout(t, stdout, stderr)
	// The point of returning the body rather than a receipt: the caller sees
	// the state the server actually settled on, not the one they asked for.
	if got["status"] != "IN_PROGRESS" {
		t.Errorf("status = %v, want the server's post-update value IN_PROGRESS", got["status"])
	}
	if got["identifier"] != "ENG-14" {
		t.Errorf("identifier = %v, want ENG-14", got["identifier"])
	}
}

func TestAcceptance_IssueComment_JSON_ReturnsTheCreatedComment(t *testing.T) {
	srv := startIssueMutationStub(t)

	stdout, stderr, err := runIssueMutationCLI(t, srv.URL,
		"issue", "comment", "ENG-14", "--body", "hi", "-f", "json")
	if err != nil {
		t.Fatalf("issue comment: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	got := decodeStdout(t, stdout, stderr)
	if got["id"] != "cmt_1" {
		t.Errorf("id = %v, want cmt_1 — the comment id is what a caller edits or replies to", got["id"])
	}
	if got["body"] != "hi" {
		t.Errorf("body = %v, want hi", got["body"])
	}
}

func TestAcceptance_IssueDelete_JSON_SynthesisesAReceiptFrom204(t *testing.T) {
	srv := startIssueMutationStub(t)

	stdout, stderr, err := runIssueMutationCLI(t, srv.URL,
		"issue", "delete", "ENG-14", "--yes", "-f", "json")
	if err != nil {
		t.Fatalf("issue delete: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	got := decodeStdout(t, stdout, stderr)
	if got["identifier"] != "ENG-14" || got["deleted"] != true {
		t.Errorf("delete receipt = %v, want identifier ENG-14 and deleted true", got)
	}
}

func TestAcceptance_IssueTransitions_JSON_ReportTheResultingStatus(t *testing.T) {
	srv := startIssueMutationStub(t)

	cases := []struct {
		name       string
		args       []string
		wantStatus string
		check      func(t *testing.T, got map[string]any)
	}{
		{
			name:       "start",
			args:       []string{"issue", "start", "ENG-14", "-f", "json"},
			wantStatus: "IN_PROGRESS",
		},
		{
			name:       "stop",
			args:       []string{"issue", "stop", "ENG-14", "-f", "json"},
			wantStatus: "CANCELLED",
			check: func(t *testing.T, got map[string]any) {
				if got["runs_stopped"] != float64(2) {
					t.Errorf("runs_stopped = %v, want 2 — how many runs the stop actually reached", got["runs_stopped"])
				}
			},
		},
		{
			// /review does not echo an identifier; the CLI backfills the one
			// it resolved, so a result is never ambiguous about its subject.
			name:       "review",
			args:       []string{"issue", "review", "ENG-14", "--action", "approve", "-f", "json"},
			wantStatus: "ok",
			check: func(t *testing.T, got map[string]any) {
				if got["action"] != "approve" {
					t.Errorf("action = %v, want approve", got["action"])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, err := runIssueMutationCLI(t, srv.URL, tc.args...)
			if err != nil {
				t.Fatalf("%s: %v\nstdout: %s\nstderr: %s", tc.name, err, stdout, stderr)
			}
			got := decodeStdout(t, stdout, stderr)
			if got["status"] != tc.wantStatus {
				t.Errorf("status = %v, want %s", got["status"], tc.wantStatus)
			}
			if got["identifier"] != "ENG-14" {
				t.Errorf("identifier = %v, want ENG-14", got["identifier"])
			}
			if tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}
