package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

const parityManifest = `{"entrypoint":"index.html","files":[{"path":"styles.css","content":"h1{}","media_type":"text/css"},{"path":"index.html","content":"<h1>Draft</h1>","media_type":"text/html"}]}`

func runParityArtifact(t *testing.T, root func() *cobra.Command, args ...string) (string, error) {
	t.Helper()
	cmd := root()
	cmd.PersistentPreRun = rootCmd.PersistentPreRun
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String() + errOut.String(), err
}

// ─── 413 artifact_history_quota ─────────────────────────────────────────────

func TestPageArtifactPublishHistoryQuotaSaysNothingSaved(t *testing.T) {
	stub := pageStub(t)
	route := "/api/v1/pages/demo/artifact/versions"
	stub.OnPost(route, func(*http.Request, []byte) (int, []byte, string) {
		return 413, []byte(`{"error":"Project history quota is full. Existing versions are retained.","code":"artifact_history_quota"}`), "application/json"
	})
	_, err := runParityArtifact(t, newPageArtifactCommand, "publish", "demo", "--base-version", "7", "--file", pageBundleFile(t, parityManifest))
	if err == nil {
		t.Fatal("quota refusal accepted")
	}
	if got := cli.ExitCodeFor(err); got != cli.ExitValidation {
		t.Fatalf("exit = %d, want %d (validation, not unclassified)", got, cli.ExitValidation)
	}
	for _, want := range []string{"nothing was saved", "--file", "unchanged", "retained"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q lacks %q", err, want)
		}
	}
	envelope := cli.NewErrorEnvelope(err)
	if envelope.Error.Status != 413 || envelope.Error.Extensions["code"] != "artifact_history_quota" || envelope.Error.ExitCode != cli.ExitValidation {
		t.Fatalf("machine envelope lost the server refusal: %+v", envelope.Error)
	}
	if len(stub.CallsFor("GET", "/api/v1/pages/demo/artifact")) != 0 {
		t.Fatal("a definitive quota refusal triggered a reconcile read")
	}
}

// ─── reconcile after a lost save reply ──────────────────────────────────────

func paritySnapshot(version int, files string) []byte {
	return []byte(`{"page_id":"cpagedemo","slug":"demo","manifest":{"version":` + strconv.Itoa(version) + `,"entrypoint":"index.html","files":` + files + `}}`)
}

// Server order is sorted by path; the local manifest is not. Content is equal.
const parityServerFiles = `[{"path":"index.html","content":"<h1>Draft</h1>","media_type":"text/html"},{"path":"styles.css","content":"h1{}","media_type":"text/css"}]`

func TestPageArtifactPublishReconcilesLostReply(t *testing.T) {
	for _, status := range []int{502, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stub := pageStub(t)
			stub.OnPost("/api/v1/pages/demo/artifact/versions", func(*http.Request, []byte) (int, []byte, string) {
				return status, []byte(`{"error":"artifact changed; keep your draft"}`), "application/json"
			})
			stub.OnGet("/api/v1/pages/demo/artifact", func(r *http.Request, _ []byte) (int, []byte, string) {
				if r.URL.Query().Get("version") != "8" {
					t.Errorf("reconcile read %s, want version=8", r.URL.RawQuery)
				}
				return 200, paritySnapshot(8, parityServerFiles), "application/json"
			})
			out, err := runParityArtifact(t, newPageArtifactCommand, "publish", "demo", "--base-version", "7", "--file", pageBundleFile(t, parityManifest))
			if err != nil {
				t.Fatalf("matching successor not reconciled: %v", err)
			}
			if !strings.Contains(out, `"reconciled": true`) || !strings.Contains(out, `"version": 8`) {
				t.Fatalf("reconcile receipt missing: %s", out)
			}
			if len(stub.CallsFor("POST", "/api/v1/pages/demo/artifact/versions")) != 1 {
				t.Fatal("publish was retried; reconcile must only read")
			}
		})
	}
}

func TestPageArtifactPublishKeepsConflictWhenSuccessorDiffers(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost("/api/v1/pages/demo/artifact/versions", func(*http.Request, []byte) (int, []byte, string) {
		return 409, []byte(`{"error":"This project changed.","code":"artifact_version_conflict"}`), "application/json"
	})
	stub.OnGet("/api/v1/pages/demo/artifact", func(*http.Request, []byte) (int, []byte, string) {
		return 200, paritySnapshot(8, `[{"path":"index.html","content":"<h1>Someone else</h1>","media_type":"text/html"},{"path":"styles.css","content":"h1{}","media_type":"text/css"}]`), "application/json"
	})
	_, err := runParityArtifact(t, newPageArtifactCommand, "publish", "demo", "--base-version", "7", "--file", pageBundleFile(t, parityManifest))
	if err == nil || !strings.Contains(err.Error(), "This project changed") || cli.ExitCodeFor(err) != cli.ExitConflict {
		t.Fatalf("foreign successor must keep the original conflict: %v (exit %d)", err, cli.ExitCodeFor(err))
	}
}

func TestPageArtifactReconcileCommand(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		files  string
		exit   int
		want   string
	}{
		{"same", 200, parityServerFiles, cli.ExitOK, `"reconciled": true`},
		{"absent", 404, "", cli.ExitNotFound, "publishing again with --base-version 7 is safe"},
		{"differs", 200, `[{"path":"index.html","content":"<h1>Draft</h1>","media_type":"text/plain"},{"path":"styles.css","content":"h1{}","media_type":"text/css"}]`, cli.ExitConflict, "different content"},
		{"extra-file", 200, `[{"path":"index.html","content":"<h1>Draft</h1>","media_type":"text/html"},{"path":"styles.css","content":"h1{}","media_type":"text/css"},{"path":"x.js","content":"","media_type":"text/javascript"}]`, cli.ExitConflict, "different content"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := pageStub(t)
			stub.OnGet("/api/v1/pages/demo/artifact", func(*http.Request, []byte) (int, []byte, string) {
				if tc.status != 200 {
					return tc.status, []byte(`{"error":"artifact project or selected version not found"}`), "application/json"
				}
				return 200, paritySnapshot(8, tc.files), "application/json"
			})
			out, err := runParityArtifact(t, newPageArtifactCommand, "reconcile", "demo", "--base-version", "7", "--file", pageBundleFile(t, parityManifest))
			if got := cli.ExitCodeFor(err); got != tc.exit {
				t.Fatalf("exit = %d, want %d (%v)", got, tc.exit, err)
			}
			text := out
			if err != nil {
				text += err.Error()
			}
			if !strings.Contains(text, tc.want) {
				t.Fatalf("output %q lacks %q", text, tc.want)
			}
			if len(stub.CallsFor("POST", "/api/v1/pages/demo/artifact/versions")) != 0 {
				t.Fatal("reconcile wrote")
			}
		})
	}
}

func TestPageArtifactReconcileRequiresBase(t *testing.T) {
	stub := pageStub(t)
	_, err := runParityArtifact(t, newPageArtifactCommand, "reconcile", "demo", "--file", pageBundleFile(t, parityManifest))
	if cli.ExitCodeFor(err) != cli.ExitValidation || len(stub.Calls()) != 0 {
		t.Fatalf("missing base accepted or sent: %v", err)
	}
}

// ─── Design: --test-item and --authority-revision ──────────────────────────

func TestPageDesignCreateTestItem(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want any
	}{
		{[]string{"create", "board", "--name", "Board", "--test-item"}, true},
		{[]string{"create", "board", "--name", "Board"}, nil},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			stub := pageStub(t)
			stub.OnPost("/api/v1/pages/design", func(_ *http.Request, raw []byte) (int, []byte, string) {
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				if body["created_by_test"] != tc.want {
					t.Errorf("%v: created_by_test = %v, want %v", tc.args, body["created_by_test"], tc.want)
				}
				return 201, []byte(`{"slug":"board"}`), "application/json"
			})
			if _, err := runParityArtifact(t, newPageDesignCommand, tc.args...); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPageDesignWatchAuthorityRevision(t *testing.T) {
	stub := pageStub(t)
	if _, err := runParityArtifact(t, newPageDesignCommand, "watch", "board", "--authority-revision", "7; drop"); cli.ExitCodeFor(err) != cli.ExitValidation {
		t.Fatalf("non-decimal revision: %v", err)
	}
	if len(stub.Calls()) != 0 {
		t.Fatal("invalid revision reached the server")
	}
	stub.OnGet("/api/v1/pages/board/design/ws", func(*http.Request, []byte) (int, []byte, string) {
		return 403, []byte(`{"error":"Design access denied"}`), "application/json"
	})
	_, err := runParityArtifact(t, newPageDesignCommand, "watch", "board", "--authority-revision", "42")
	if err == nil || !strings.Contains(err.Error(), "Design access denied") || cli.ExitCodeFor(err) != cli.ExitAuth {
		t.Fatalf("refused upgrade lost its body or class: %v", err)
	}
	calls := stub.CallsFor("GET", "/api/v1/pages/board/design/ws")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "authority_revision=42") {
		t.Fatalf("authority revision not sent: %+v", calls)
	}
}

// ─── craft: server bodies, exit codes, redaction ───────────────────────────

func TestCraftErrorsCarryServerBodyAndExitCode(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		exit   int
		want   []string
	}{
		{409, `{"error":"the document changed after this proposal; it was not applied","proposal":{"id":"ncp_1","state":"stale"}}`, cli.ExitConflict, []string{"API error (409): the document changed", "proposal state: stale"}},
		{409, `{"error":"native Craft proposal already decided"}`, cli.ExitConflict, []string{"already decided"}},
		{422, `{"error":"original native engine operation refused"}`, cli.ExitValidation, []string{"engine operation refused"}},
		{503, `{"error":"native Craft unavailable"}`, cli.ExitServer, []string{"native Craft unavailable"}},
	} {
		t.Run(tc.want[0], func(t *testing.T) {
			stub := pageStub(t)
			stub.OnGet("/api/v1/pages/health/craft/availability", func(*http.Request, []byte) (int, []byte, string) {
				return tc.status, []byte(tc.body), "application/json"
			})
			_, err := runPageCLI(t, "", "craft", "availability", "health")
			if err == nil || cli.ExitCodeFor(err) != tc.exit {
				t.Fatalf("%d: exit %d, want %d (%v)", tc.status, cli.ExitCodeFor(err), tc.exit, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("%d: %q lacks %q", tc.status, err, want)
				}
			}
			var api *cli.APIError
			if !errors.As(err, &api) || api.Status != tc.status {
				t.Fatalf("%d: no APIError for the JSON envelope: %T", tc.status, err)
			}
		})
	}
}

func TestCraftLeaseConflictNamesHolderAndRedactsToken(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	secret := strings.Repeat("d", 64)
	stub.OnPost(base+"/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"tab"}`), "application/json"
	})
	stub.OnPost(base+"/lease/renew", func(*http.Request, []byte) (int, []byte, string) {
		return 409, []byte(`{"error":"native editor lease changed for ` + secret + `"}`), "application/json"
	})
	stub.OnGet(base+"/lease", func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != secret {
			t.Error("holder lookup lost the session binding")
		}
		return 200, []byte(`{"holder":{"actor_id":"agent:a1","display_name":"Writer","kind":"agent","agent_id":"a1","run_id":"run-9"},"mine":false,"generation":"12"}`), "application/json"
	})
	_, err := runPageCLI(t, `{"action":"renew","generation":"11"}`+"\n", "craft", "connect", "health")
	if err == nil || cli.ExitCodeFor(err) != cli.ExitConflict {
		t.Fatalf("lease conflict exit %d: %v", cli.ExitCodeFor(err), err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("session token leaked into the error")
	}
	envelope, _ := json.Marshal(cli.NewErrorEnvelope(err))
	if bytes.Contains(envelope, []byte(secret)) {
		t.Fatal("session token leaked into the JSON envelope")
	}
	if !strings.Contains(err.Error(), `lease held by agent "Writer" run run-9, generation 12`) {
		t.Fatalf("holder not surfaced: %v", err)
	}
}

// ─── mooring browser-transport routes ──────────────────────────────────────

func TestMooringCommandSessionAndReauthorizeSendOrigin(t *testing.T) {
	stub := pageStub(t)
	origin := stub.URL()
	// rootCmd flag values outlive Execute; leave --reauthorize as found.
	t.Cleanup(func() {
		if session, _, err := rootCmd.Find([]string{"mooring", "session"}); err == nil {
			_ = session.Flags().Set("reauthorize", "false")
		}
	})
	for _, tc := range []struct {
		args  []string
		route string
		body  string
	}{
		{[]string{"mooring", "session", "sheet", "--file", "-"}, "/api/v1/pages/sheet/mooring/session", `{"write":false}`},
		{[]string{"mooring", "command-session", "sheet", "--file", "-"}, "/api/v1/pages/sheet/mooring/command-session", `{"action":"open","write":true}`},
		{[]string{"mooring", "session", "sheet", "--reauthorize", "--file", "-"}, "/api/v1/pages/sheet/mooring/session/reauthorize", `{"connection_ref":"conn-1"}`},
	} {
		stub.OnPost(tc.route, func(r *http.Request, raw []byte) (int, []byte, string) {
			if string(raw) != tc.body {
				t.Errorf("%s: packet changed: %s", tc.route, raw)
			}
			return 200, []byte(`{"ok":true}`), "application/json"
		})
		if _, err := runPageCLI(t, tc.body, tc.args...); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		calls := stub.CallsFor("POST", tc.route)
		if len(calls) != 1 {
			t.Fatalf("%s: %d calls", tc.route, len(calls))
		}
		got := calls[0].Headers.Get("Origin")
		if strings.HasSuffix(tc.route, "/mooring/session") {
			if got != "" {
				t.Fatalf("plain session gained an Origin header: %q", got)
			}
			continue
		}
		if got != origin {
			t.Fatalf("%s: Origin = %q, want %q", tc.route, got, origin)
		}
	}
}

// ─── run --artifact-target parsing ─────────────────────────────────────────

func TestReadRunArtifactTarget(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		packet string
		ok     bool
	}{
		{`{"submission_id":"create-1"}`, true},
		{`{"submission_id":"s","page_id":"p","comment_id":"c","base_version":2}`, true},
		{`{"kind":"design","submission_id":"s","page_id":"p","comment_id":"c","base_version":2}`, true},
		{`{"kind":"design","submission_id":"s"}`, false},
		{`{"submission_id":"s","page_id":"p"}`, false},
		{`{"submission_id":"s","created_by_test":true,"page_id":"p","base_version":1}`, false},
		{`{"submission_id":"../x"}`, false},
		{`[]`, false},
		{strings.Repeat(" ", 2049) + `{"submission_id":"s"}`, false},
	} {
		path := filepath.Join(dir, "t.json")
		if err := os.WriteFile(path, []byte(tc.packet), 0o600); err != nil {
			t.Fatal(err)
		}
		intent, err := readRunArtifactTarget(path)
		if (err == nil) != tc.ok {
			t.Fatalf("%s: err=%v", tc.packet, err)
		}
		if err != nil && cli.ExitCodeFor(err) != cli.ExitValidation {
			t.Fatalf("%s: exit %d", tc.packet, cli.ExitCodeFor(err))
		}
		if tc.ok && intent.SubmissionID == "" {
			t.Fatalf("%s: lost submission", tc.packet)
		}
	}
}

func TestArtifactCommentWaitRequiresSendToAgent(t *testing.T) {
	stub := pageStub(t)
	_, err := runParityArtifact(t, newPageArtifactCommand, "comment", "demo", "--version", "3", "--path", "index.html", "--selector", "#hero", "--quote", "Title", "--text", "x", "--wait")
	if cli.ExitCodeFor(err) != cli.ExitValidation || len(stub.Calls()) != 0 {
		t.Fatalf("--wait without --send-to-agent: %v", err)
	}
	_, err = runParityArtifact(t, newPageDesignCommand, "comment", "board", "--version", "3", "--node", "n1", "--text", "x", "--wait")
	if cli.ExitCodeFor(err) != cli.ExitValidation || len(stub.Calls()) != 0 {
		t.Fatalf("design --wait without --send-to-agent: %v", err)
	}
}
