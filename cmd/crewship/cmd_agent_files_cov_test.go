package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// ─── pure helpers ───────────────────────────────────────────────────────

func TestExtractFileList(t *testing.T) {
	cases := []struct {
		name string
		body any
		want int
	}{
		{"plain slice", []any{map[string]any{"name": "a"}, map[string]any{"name": "b"}}, 2},
		{"slice with junk entries skipped", []any{map[string]any{"name": "a"}, "junk", 42}, 1},
		{"wrapped files key", map[string]any{"files": []any{map[string]any{"name": "x"}}}, 1},
		{"wrapped with junk", map[string]any{"files": []any{"junk", map[string]any{"name": "x"}}}, 1},
		{"map without files key", map[string]any{"other": 1}, 0},
		{"unknown shape", "a string", 0},
		{"nil", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFileList(tc.body)
			if len(got) != tc.want {
				t.Errorf("extractFileList(%v) returned %d entries, want %d", tc.body, len(got), tc.want)
			}
		})
	}
}

func TestPrintFilesTable(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out := covCaptureStdoutCli3(t, func() {
			if err := printFilesTable([]any{}); err != nil {
				t.Errorf("printFilesTable: %v", err)
			}
		})
		if !strings.Contains(out, "No files.") {
			t.Errorf("expected 'No files.', got %q", out)
		}
	})
	t.Run("rows with name/path/size/modified fallbacks", func(t *testing.T) {
		body := []any{
			map[string]any{"name": "report.txt", "size": float64(1234), "modified": "2026-06-01T10:30:00Z"},
			map[string]any{"path": "sub/other.bin"}, // name empty → falls back to path
		}
		out := covCaptureStdoutCli3(t, func() {
			if err := printFilesTable(body); err != nil {
				t.Errorf("printFilesTable: %v", err)
			}
		})
		if !strings.Contains(out, "report.txt") || !strings.Contains(out, "1234") {
			t.Errorf("missing first row data: %q", out)
		}
		if !strings.Contains(out, "2026-06-01 10:30") {
			t.Errorf("modified timestamp not reformatted: %q", out)
		}
		if !strings.Contains(out, "sub/other.bin") {
			t.Errorf("path fallback row missing: %q", out)
		}
	})
}

func TestPrintInboxTable(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out := covCaptureStdoutCli3(t, func() {
			if err := printInboxTable(nil); err != nil {
				t.Errorf("printInboxTable: %v", err)
			}
		})
		if !strings.Contains(out, "Inbox empty.") {
			t.Errorf("expected 'Inbox empty.', got %q", out)
		}
	})
	t.Run("messages with field fallbacks", func(t *testing.T) {
		body := map[string]any{"messages": []any{
			map[string]any{"created_at": "2026-06-01T08:00:00Z", "from": "viktor", "body": "line1\nline2"},
			map[string]any{"ts": "2026-06-02T09:00:00Z", "sender": "eva", "content": "via fallbacks"},
			"junk-skipped",
		}}
		out := covCaptureStdoutCli3(t, func() {
			if err := printInboxTable(body); err != nil {
				t.Errorf("printInboxTable: %v", err)
			}
		})
		if !strings.Contains(out, "viktor") || !strings.Contains(out, "eva") {
			t.Errorf("senders missing: %q", out)
		}
		if !strings.Contains(out, "line1 line2") {
			t.Errorf("newline in body not flattened: %q", out)
		}
		if !strings.Contains(out, "via fallbacks") {
			t.Errorf("sender/content fallback row missing: %q", out)
		}
	})
}

func TestPrintGitLogTable(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		out := covCaptureStdoutCli3(t, func() { printGitLogTable(nil) })
		if !strings.Contains(out, "No commits.") {
			t.Errorf("expected 'No commits.', got %q", out)
		}
	})
	t.Run("hash shortening and fallbacks", func(t *testing.T) {
		body := map[string]any{"commits": []any{
			map[string]any{"hash": "abcdef0123456789", "message": "fix: thing", "when": "2026-06-01"},
			map[string]any{"sha": "1234567", "subject": "feat: other", "date": "2026-06-02"},
			42, // skipped
		}}
		out := covCaptureStdoutCli3(t, func() { printGitLogTable(body) })
		if !strings.Contains(out, "abcdef01") {
			t.Errorf("long hash not shortened to 8 chars: %q", out)
		}
		if strings.Contains(out, "abcdef0123456789") {
			t.Errorf("full hash leaked into output: %q", out)
		}
		if !strings.Contains(out, "fix: thing") || !strings.Contains(out, "feat: other") {
			t.Errorf("messages missing: %q", out)
		}
		if !strings.Contains(out, "1234567") {
			t.Errorf("short sha fallback missing: %q", out)
		}
	})
}

// ─── agent files (list + download) ──────────────────────────────────────

func TestAgentFilesCmd_ListTable(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files",
		clitest.JSONResponse(200, []map[string]any{{"name": "out.txt", "size": 9}}))

	out := covCaptureStdoutCli3(t, func() {
		if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "out.txt") {
		t.Errorf("table output missing file row: %q", out)
	}
}

func TestAgentFilesCmd_ListJSONFormat(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	cliCfg.Format = "json"
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files",
		clitest.JSONResponse(200, []map[string]any{{"name": "out.txt"}}))

	out := covCaptureStdoutCli3(t, func() {
		if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"name"`) || !strings.Contains(out, "out.txt") {
		t.Errorf("json output missing payload: %q", out)
	}
}

func TestAgentFilesCmd_Download(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files/download",
		clitest.TextResponse(200, "downloaded-bytes"))

	target := filepath.Join(t.TempDir(), "saved.txt")
	if err := agentFilesCmd.Flags().Set("download", "report.txt"); err != nil {
		t.Fatal(err)
	}
	if err := agentFilesCmd.Flags().Set("out", target); err != nil {
		t.Fatal(err)
	}
	if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(data) != "downloaded-bytes" {
		t.Errorf("file content: got %q", data)
	}
	calls := stub.CallsFor("GET", "/api/v1/agents/"+covAgentIDCli3+"/files/download")
	if len(calls) != 1 {
		t.Fatalf("expected 1 download GET, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "path=report.txt") {
		t.Errorf("download query missing path param: %q", calls[0].Query)
	}
}

func TestAgentFilesCmd_DownloadToStdout(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files/download",
		clitest.TextResponse(200, "stdout-stream"))

	if err := agentFilesCmd.Flags().Set("download", "x.txt"); err != nil {
		t.Fatal(err)
	}
	if err := agentFilesCmd.Flags().Set("out", "-"); err != nil {
		t.Fatal(err)
	}
	out := covCaptureStdoutCli3(t, func() {
		if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if out != "stdout-stream" {
		t.Errorf("stdout download: got %q", out)
	}
}

func TestAgentFilesCmd_DownloadServerError(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files/download",
		clitest.ErrorResponse(404, "file not found"))

	if err := agentFilesCmd.Flags().Set("download", "ghost.txt"); err != nil {
		t.Fatal(err)
	}
	if err := agentFilesCmd.Flags().Set("out", filepath.Join(t.TempDir(), "x")); err != nil {
		t.Fatal(err)
	}
	err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3})
	if err == nil || !strings.Contains(err.Error(), "file not found") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}

func TestAgentFilesCmd_ListAPIError(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files",
		clitest.ErrorResponse(500, "boom"))

	err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected server error, got %v", err)
	}
}

// ─── agent inbox ────────────────────────────────────────────────────────

func TestAgentInboxCmd_Table(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentInboxCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/inbox",
		clitest.JSONResponse(200, []map[string]any{
			{"created_at": "2026-06-01T08:00:00Z", "from": "eva", "body": "ping"},
		}))

	out := covCaptureStdoutCli3(t, func() {
		if err := agentInboxCmd.RunE(agentInboxCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "eva") || !strings.Contains(out, "ping") {
		t.Errorf("inbox table missing row: %q", out)
	}
}

func TestAgentInboxCmd_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	err := agentInboxCmd.RunE(agentInboxCmd, []string{covAgentIDCli3})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("expected not-logged-in error, got %v", err)
	}
}

// ─── agent git-log ──────────────────────────────────────────────────────

func TestAgentGitLogCmd_Table(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentGitLogCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/git-log",
		clitest.JSONResponse(200, map[string]any{"commits": []map[string]any{
			{"hash": "deadbeefcafe", "message": "initial", "when": "2026-06-01"},
		}}))

	out := covCaptureStdoutCli3(t, func() {
		if err := agentGitLogCmd.RunE(agentGitLogCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "deadbeef") || !strings.Contains(out, "initial") {
		t.Errorf("git log output missing commit: %q", out)
	}
}

func TestAgentGitLogCmd_APIError(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentGitLogCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/git-log",
		clitest.ErrorResponse(503, "container offline"))

	err := agentGitLogCmd.RunE(agentGitLogCmd, []string{covAgentIDCli3})
	if err == nil || !strings.Contains(err.Error(), "container offline") {
		t.Fatalf("expected 503 error, got %v", err)
	}
}

// ─── agent file-write ───────────────────────────────────────────────────

func TestAgentFileWriteCmd_Content(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFileWriteCmd)
	stub.OnPut("/api/v1/agents/"+covAgentIDCli3+"/files/save",
		clitest.JSONResponse(200, map[string]any{"ok": true}))

	if err := agentFileWriteCmd.Flags().Set("content", "# inline"); err != nil {
		t.Fatal(err)
	}
	if err := agentFileWriteCmd.RunE(agentFileWriteCmd, []string{covAgentIDCli3, "notes/draft.md"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("PUT", "/api/v1/agents/"+covAgentIDCli3+"/files/save")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(calls))
	}
	if string(calls[0].Body) != "# inline" {
		t.Errorf("uploaded bytes: got %q", calls[0].Body)
	}
	if !strings.Contains(calls[0].Query, "path=notes%2Fdraft.md") {
		t.Errorf("query missing escaped path: %q", calls[0].Query)
	}
	if !strings.Contains(calls[0].Query, "workspace_id="+covWSCli3) {
		t.Errorf("query missing workspace_id: %q", calls[0].Query)
	}
}

func TestAgentFileWriteCmd_FromFile(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFileWriteCmd)
	stub.OnPut("/api/v1/agents/"+covAgentIDCli3+"/files/save",
		clitest.JSONResponse(200, map[string]any{"ok": true}))

	src := filepath.Join(t.TempDir(), "local.toml")
	if err := os.WriteFile(src, []byte("key = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agentFileWriteCmd.Flags().Set("from", src); err != nil {
		t.Fatal(err)
	}
	if err := agentFileWriteCmd.RunE(agentFileWriteCmd, []string{covAgentIDCli3, "config/x.toml"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PUT", "/api/v1/agents/"+covAgentIDCli3+"/files/save")
	if len(calls) != 1 || string(calls[0].Body) != "key = 1\n" {
		t.Fatalf("uploaded body wrong: %v", calls)
	}
}

func TestAgentFileWriteCmd_MutuallyExclusiveFlags(t *testing.T) {
	covStub(t)
	covResetFlags(t, agentFileWriteCmd)
	if err := agentFileWriteCmd.Flags().Set("content", "x"); err != nil {
		t.Fatal(err)
	}
	if err := agentFileWriteCmd.Flags().Set("from", "/tmp/whatever"); err != nil {
		t.Fatal(err)
	}
	err := agentFileWriteCmd.RunE(agentFileWriteCmd, []string{covAgentIDCli3, "p"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

func TestAgentFileWriteCmd_FromMissingFile(t *testing.T) {
	covStub(t)
	covResetFlags(t, agentFileWriteCmd)
	if err := agentFileWriteCmd.Flags().Set("from", filepath.Join(t.TempDir(), "ghost")); err != nil {
		t.Fatal(err)
	}
	err := agentFileWriteCmd.RunE(agentFileWriteCmd, []string{covAgentIDCli3, "p"})
	if err == nil || !strings.Contains(err.Error(), "stat") {
		t.Fatalf("expected stat error, got %v", err)
	}
}

func TestAgentFileWriteCmd_Stdin(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFileWriteCmd)
	stub.OnPut("/api/v1/agents/"+covAgentIDCli3+"/files/save",
		clitest.JSONResponse(200, map[string]any{"ok": true}))
	covWithStdin(t, "from-stdin-bytes")

	if err := agentFileWriteCmd.RunE(agentFileWriteCmd, []string{covAgentIDCli3, "in.txt"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("PUT", "/api/v1/agents/"+covAgentIDCli3+"/files/save")
	if len(calls) != 1 || string(calls[0].Body) != "from-stdin-bytes" {
		t.Fatalf("stdin body wrong: %v", calls)
	}
}

func TestAgentFileWriteCmd_ServerError(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFileWriteCmd)
	stub.OnPut("/api/v1/agents/"+covAgentIDCli3+"/files/save",
		clitest.ErrorResponse(403, "read-only agent"))

	if err := agentFileWriteCmd.Flags().Set("content", "x"); err != nil {
		t.Fatal(err)
	}
	err := agentFileWriteCmd.RunE(agentFileWriteCmd, []string{covAgentIDCli3, "p"})
	if err == nil || !strings.Contains(err.Error(), "read-only agent") {
		t.Fatalf("expected 403 error, got %v", err)
	}
	_ = http.StatusOK
}
