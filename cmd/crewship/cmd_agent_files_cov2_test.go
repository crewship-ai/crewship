package main

import (
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestAgentFileCmds_NoAuth(t *testing.T) {
	covRunNoAuth(t, []covCmdCase{
		{name: "files", cmd: agentFilesCmd, args: []string{covAgentIDCli3}},
		{name: "inbox", cmd: agentInboxCmd, args: []string{covAgentIDCli3}},
		{name: "git-log", cmd: agentGitLogCmd, args: []string{covAgentIDCli3}},
		{name: "file-write", cmd: agentFileWriteCmd, args: []string{covAgentIDCli3, "p"}},
	})
}

func TestAgentFileCmds_ResolveFails(t *testing.T) {
	cases := []covCmdCase{
		{name: "files", cmd: agentFilesCmd, args: []string{"viktor"}},
		{name: "inbox", cmd: agentInboxCmd, args: []string{"viktor"}},
		{name: "git-log", cmd: agentGitLogCmd, args: []string{"viktor"}},
		{name: "file-write", cmd: agentFileWriteCmd, args: []string{"viktor", "p"},
			flags: map[string]string{"content": "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := covStub(t)
			covResetFlags(t, tc.cmd)
			stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents broke"))
			covSetFlags(t, tc.cmd, tc.flags)
			err := tc.cmd.RunE(tc.cmd, tc.args)
			if err == nil || !strings.Contains(err.Error(), "agents broke") {
				t.Fatalf("expected resolver error, got %v", err)
			}
		})
	}
}

func TestAgentFilesCmd_FilterFlag(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files",
		clitest.JSONResponse(200, []map[string]any{{"name": "x.txt"}}))
	// jq missing → graceful fallback to raw JSON, still through the
	// filter code path.
	covSwapJQ(t, func(string) (string, error) { return "", os.ErrNotExist }, nil)

	if err := agentFilesCmd.Flags().Set("filter", ".[0].name"); err != nil {
		t.Fatal(err)
	}
	out := covCaptureStdoutCli3(t, func() {
		if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "x.txt") {
		t.Errorf("fallback output missing payload: %q", out)
	}
}

func TestAgentFilesCmd_YAMLFormat(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	cliCfg.Format = "yaml"
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files",
		clitest.JSONResponse(200, []map[string]any{{"name": "y.txt"}}))
	out := covCaptureStdoutCli3(t, func() {
		if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "y.txt") {
		t.Errorf("yaml output missing: %q", out)
	}
}

func TestAgentFilesCmd_DownloadDefaultsToBasename(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, agentFilesCmd)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/files/download",
		clitest.TextResponse(200, "basename-bytes"))
	t.Chdir(t.TempDir()) // download lands in CWD when --out is unset

	if err := agentFilesCmd.Flags().Set("download", "deep/nested/report.txt"); err != nil {
		t.Fatal(err)
	}
	if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	data, err := os.ReadFile("report.txt")
	if err != nil {
		t.Fatalf("expected download at basename: %v", err)
	}
	if string(data) != "basename-bytes" {
		t.Errorf("content: %q", data)
	}
}

func TestAgentFilesCmd_DownloadTransportError(t *testing.T) {
	covStub(t)
	covResetFlags(t, agentFilesCmd)
	cliCfg.Server = "http://127.0.0.1:1"
	if err := agentFilesCmd.Flags().Set("download", "x.txt"); err != nil {
		t.Fatal(err)
	}
	if err := agentFilesCmd.Flags().Set("out", "-"); err != nil {
		t.Fatal(err)
	}
	if err := agentFilesCmd.RunE(agentFilesCmd, []string{covAgentIDCli3}); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestAgentInboxCmd_Formats(t *testing.T) {
	payload := []map[string]any{{"from": "eva", "body": "hello"}}

	t.Run("json", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, agentInboxCmd)
		cliCfg.Format = "json"
		stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/inbox", clitest.JSONResponse(200, payload))
		out := covCaptureStdoutCli3(t, func() {
			if err := agentInboxCmd.RunE(agentInboxCmd, []string{covAgentIDCli3}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, `"from"`) {
			t.Errorf("json missing: %q", out)
		}
	})
	t.Run("yaml", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, agentInboxCmd)
		cliCfg.Format = "yaml"
		stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/inbox", clitest.JSONResponse(200, payload))
		out := covCaptureStdoutCli3(t, func() {
			if err := agentInboxCmd.RunE(agentInboxCmd, []string{covAgentIDCli3}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "eva") {
			t.Errorf("yaml missing: %q", out)
		}
	})
	t.Run("filter", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, agentInboxCmd)
		stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/inbox", clitest.JSONResponse(200, payload))
		covSwapJQ(t,
			func(string) (string, error) { return "/fake/jq", nil },
			func(string, string) jqRunner { return &fakeJQCov{out: []byte("\"eva\"\n")} })
		if err := agentInboxCmd.Flags().Set("filter", ".[0].from"); err != nil {
			t.Fatal(err)
		}
		out := covCaptureStdoutCli3(t, func() {
			if err := agentInboxCmd.RunE(agentInboxCmd, []string{covAgentIDCli3}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if out != "\"eva\"\n" {
			t.Errorf("filtered output: %q", out)
		}
	})
}

func TestAgentGitLogCmd_Formats(t *testing.T) {
	payload := []map[string]any{{"hash": "abc123", "message": "init"}}

	t.Run("json", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, agentGitLogCmd)
		cliCfg.Format = "json"
		stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/git-log", clitest.JSONResponse(200, payload))
		out := covCaptureStdoutCli3(t, func() {
			if err := agentGitLogCmd.RunE(agentGitLogCmd, []string{covAgentIDCli3}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, `"hash"`) {
			t.Errorf("json missing: %q", out)
		}
	})
	t.Run("yaml", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, agentGitLogCmd)
		cliCfg.Format = "yaml"
		stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/git-log", clitest.JSONResponse(200, payload))
		out := covCaptureStdoutCli3(t, func() {
			if err := agentGitLogCmd.RunE(agentGitLogCmd, []string{covAgentIDCli3}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "abc123") {
			t.Errorf("yaml missing: %q", out)
		}
	})
	t.Run("filter", func(t *testing.T) {
		stub := covStub(t)
		covResetFlags(t, agentGitLogCmd)
		stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/git-log", clitest.JSONResponse(200, payload))
		covSwapJQ(t,
			func(string) (string, error) { return "/fake/jq", nil },
			func(string, string) jqRunner { return &fakeJQCov{out: []byte("\"abc123\"\n")} })
		if err := agentGitLogCmd.Flags().Set("filter", ".[0].hash"); err != nil {
			t.Fatal(err)
		}
		out := covCaptureStdoutCli3(t, func() {
			if err := agentGitLogCmd.RunE(agentGitLogCmd, []string{covAgentIDCli3}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if out != "\"abc123\"\n" {
			t.Errorf("filtered output: %q", out)
		}
	})
}
