package main

// Coverage tests for cmd_crew_files.go — list / get / save against the
// crew shared volume, plus the raw-bytes putBytes helper.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covCrewIDCli7 = "ccrewa1234567890123456789"

func covCrewFilesStub(s *clitest.StubServer) {
	s.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli7, "slug": "alpha"},
	}))
}

// ─── putBytes ────────────────────────────────────────────────────────────

func TestPutBytes(t *testing.T) {
	t.Run("streams raw body with auth and workspace", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		path := "/api/v1/crews/" + covCrewIDCli7 + "/files/save"
		s.OnPut(path, clitest.EmptyResponse(204))

		client := covStubClient(s)
		err := putBytes(context.Background(), client, path+"?path=shared%2Fhi.txt", strings.NewReader("raw-bytes"))
		if err != nil {
			t.Fatalf("putBytes: %v", err)
		}
		calls := s.CallsFor("PUT", path)
		if len(calls) != 1 {
			t.Fatalf("expected 1 PUT, got %d", len(calls))
		}
		c := calls[0]
		if string(c.Body) != "raw-bytes" {
			t.Errorf("body = %q, must NOT be JSON-wrapped", c.Body)
		}
		if got := c.Headers.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q", got)
		}
		if got := c.Headers.Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("content-type = %q", got)
		}
		if !strings.Contains(c.Query, "workspace_id="+covWSCli7) {
			t.Errorf("workspace_id missing from query: %q", c.Query)
		}
		if !strings.Contains(c.Query, "path=shared%2Fhi.txt") {
			t.Errorf("file path param lost: %q", c.Query)
		}
	})

	t.Run("existing workspace_id is not overwritten", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		path := "/api/v1/crews/" + covCrewIDCli7 + "/files/save"
		s.OnPut(path, clitest.EmptyResponse(204))

		err := putBytes(context.Background(), covStubClient(s), path+"?workspace_id=explicit", strings.NewReader("x"))
		if err != nil {
			t.Fatal(err)
		}
		c := s.CallsFor("PUT", path)[0]
		if !strings.Contains(c.Query, "workspace_id=explicit") || strings.Contains(c.Query, covWSCli7) {
			t.Errorf("query = %q, explicit workspace_id should win", c.Query)
		}
	})

	t.Run("server error surfaces", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		path := "/api/v1/crews/" + covCrewIDCli7 + "/files/save"
		s.OnPut(path, clitest.ErrorResponse(413, "too large"))
		err := putBytes(context.Background(), covStubClient(s), path, strings.NewReader("x"))
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("nil context falls back to background", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		path := "/api/v1/crews/" + covCrewIDCli7 + "/files/save"
		s.OnPut(path, clitest.EmptyResponse(204))
		//nolint:staticcheck // deliberately passing nil ctx to exercise the fallback
		if err := putBytes(nil, covStubClient(s), path, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unparseable URL errors", func(t *testing.T) {
		client := cli.NewClient("http://[::1]:bad", "t", covWSCli7)
		err := putBytes(context.Background(), client, "/x", strings.NewReader("x"))
		// putBytes now builds the request via client.NewRequest, whose URL-parse
		// failure is reported as "parse URL: …".
		if err == nil || !strings.Contains(err.Error(), "parse URL") {
			t.Fatalf("got %v", err)
		}
	})
}

// ─── crew files list ─────────────────────────────────────────────────────

func TestCrewFilesListRunE(t *testing.T) {
	t.Run("table output with subdir and recursive", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnGet("/api/v1/crews/"+covCrewIDCli7+"/files", clitest.JSONResponse(200, []map[string]any{
			{"name": "notes.md", "size": 1234, "modified": "2026-06-01T10:00:00Z"},
		}))

		for _, set := range [][2]string{{"path", "shared/notes"}, {"recursive", "true"}} {
			if err := crewFilesListCmd.Flags().Set(set[0], set[1]); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			_ = crewFilesListCmd.Flags().Set("path", "")
			_ = crewFilesListCmd.Flags().Set("recursive", "false")
			_ = crewFilesListCmd.Flags().Set("filter", "")
		})

		out, err := covCaptureStdoutCli7(t, func() error {
			return crewFilesListCmd.RunE(crewFilesListCmd, []string{"alpha"})
		})
		if err != nil {
			t.Fatalf("files list: %v", err)
		}
		if !strings.Contains(out, "notes.md") || !strings.Contains(out, "1234") {
			t.Errorf("table output = %q", out)
		}
		gets := s.CallsFor("GET", "/api/v1/crews/"+covCrewIDCli7+"/files")
		if len(gets) != 1 {
			t.Fatalf("expected 1 list GET, got %d", len(gets))
		}
		if !strings.Contains(gets[0].Query, "subdir=shared%2Fnotes") || !strings.Contains(gets[0].Query, "recursive=true") {
			t.Errorf("query = %q", gets[0].Query)
		}
	})

	t.Run("API error surfaces", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnGet("/api/v1/crews/"+covCrewIDCli7+"/files", clitest.ErrorResponse(404, "crew offline"))
		err := crewFilesListCmd.RunE(crewFilesListCmd, []string{"alpha"})
		if err == nil || !strings.Contains(err.Error(), "crew offline") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("no auth", func(t *testing.T) {
		saveCLIState(t)
		cliCfg = &cli.CLIConfig{}
		err := crewFilesListCmd.RunE(crewFilesListCmd, []string{"alpha"})
		if err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("got %v", err)
		}
	})
}

// ─── crew files get ──────────────────────────────────────────────────────

func TestCrewFilesGetRunE(t *testing.T) {
	dlPath := "/api/v1/crews/" + covCrewIDCli7 + "/files/download"

	t.Run("stdout by default", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnGet(dlPath, clitest.TextResponse(200, "file-bytes"))
		_ = crewFilesGetCmd.Flags().Set("out", "")

		out, err := covCaptureStdoutCli7(t, func() error {
			return crewFilesGetCmd.RunE(crewFilesGetCmd, []string{"alpha", "shared/notes.md"})
		})
		if err != nil {
			t.Fatalf("files get: %v", err)
		}
		if out != "file-bytes" {
			t.Errorf("stdout = %q", out)
		}
		gets := s.CallsFor("GET", dlPath)
		if len(gets) != 1 || !strings.Contains(gets[0].Query, "path=shared%2Fnotes.md") {
			t.Errorf("download query = %+v", gets)
		}
	})

	t.Run("atomic write to --out", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnGet(dlPath, clitest.TextResponse(200, "to-disk"))

		dest := filepath.Join(t.TempDir(), "dump.bin")
		if err := crewFilesGetCmd.Flags().Set("out", dest); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = crewFilesGetCmd.Flags().Set("out", "") })

		if err := crewFilesGetCmd.RunE(crewFilesGetCmd, []string{"alpha", "shared/dump.bin"}); err != nil {
			t.Fatalf("files get --out: %v", err)
		}
		data, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("dest missing: %v", err)
		}
		if string(data) != "to-disk" {
			t.Errorf("file content = %q", data)
		}
	})

	t.Run("download error", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnGet(dlPath, clitest.ErrorResponse(404, "no such file"))
		_ = crewFilesGetCmd.Flags().Set("out", "")
		err := crewFilesGetCmd.RunE(crewFilesGetCmd, []string{"alpha", "missing.txt"})
		if err == nil || !strings.Contains(err.Error(), "no such file") {
			t.Fatalf("got %v", err)
		}
	})
}

// ─── crew files save ─────────────────────────────────────────────────────

func TestCrewFilesSaveRunE(t *testing.T) {
	savePath := "/api/v1/crews/" + covCrewIDCli7 + "/files/save"

	resetSaveFlags := func(t *testing.T) {
		t.Helper()
		t.Cleanup(func() {
			_ = crewFilesSaveCmd.Flags().Set("content", "")
			_ = crewFilesSaveCmd.Flags().Set("file", "")
			// Changed() state survives Set("") — recreate cleanliness by
			// resetting the Changed bookkeeping via the flag's def value.
			crewFilesSaveCmd.Flags().Lookup("content").Changed = false
			crewFilesSaveCmd.Flags().Lookup("file").Changed = false
		})
	}

	t.Run("--content uploads inline bytes", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnPut(savePath, clitest.EmptyResponse(204))
		resetSaveFlags(t)
		if err := crewFilesSaveCmd.Flags().Set("content", "draft text"); err != nil {
			t.Fatal(err)
		}

		if err := crewFilesSaveCmd.RunE(crewFilesSaveCmd, []string{"alpha", "shared/note.md"}); err != nil {
			t.Fatalf("save --content: %v", err)
		}
		puts := s.CallsFor("PUT", savePath)
		if len(puts) != 1 {
			t.Fatalf("expected 1 PUT, got %d", len(puts))
		}
		if string(puts[0].Body) != "draft text" {
			t.Errorf("body = %q", puts[0].Body)
		}
		if !strings.Contains(puts[0].Query, "path=shared%2Fnote.md") {
			t.Errorf("query = %q", puts[0].Query)
		}
	})

	t.Run("--file uploads local file", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnPut(savePath, clitest.EmptyResponse(204))
		resetSaveFlags(t)

		src := filepath.Join(t.TempDir(), "data.bin")
		if err := os.WriteFile(src, []byte("binary-payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := crewFilesSaveCmd.Flags().Set("file", src); err != nil {
			t.Fatal(err)
		}

		if err := crewFilesSaveCmd.RunE(crewFilesSaveCmd, []string{"alpha", "shared/data.bin"}); err != nil {
			t.Fatalf("save --file: %v", err)
		}
		puts := s.CallsFor("PUT", savePath)
		if len(puts) != 1 || string(puts[0].Body) != "binary-payload" {
			t.Errorf("uploaded body = %q", puts[0].Body)
		}
	})

	t.Run("--content and --file are mutually exclusive", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		resetSaveFlags(t)
		if err := crewFilesSaveCmd.Flags().Set("content", "x"); err != nil {
			t.Fatal(err)
		}
		if err := crewFilesSaveCmd.Flags().Set("file", "/tmp/whatever"); err != nil {
			t.Fatal(err)
		}
		err := crewFilesSaveCmd.RunE(crewFilesSaveCmd, []string{"alpha", "p"})
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("--file missing on disk", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		resetSaveFlags(t)
		if err := crewFilesSaveCmd.Flags().Set("file", filepath.Join(t.TempDir(), "ghost")); err != nil {
			t.Fatal(err)
		}
		err := crewFilesSaveCmd.RunE(crewFilesSaveCmd, []string{"alpha", "p"})
		if err == nil || !strings.Contains(err.Error(), "stat") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("server rejection propagates", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnPut(savePath, clitest.ErrorResponse(403, "read-only volume"))
		resetSaveFlags(t)
		if err := crewFilesSaveCmd.Flags().Set("content", "x"); err != nil {
			t.Fatal(err)
		}
		err := crewFilesSaveCmd.RunE(crewFilesSaveCmd, []string{"alpha", "p"})
		if err == nil || !strings.Contains(err.Error(), "read-only volume") {
			t.Fatalf("got %v", err)
		}
	})
}

// ─── additional error paths ──────────────────────────────────────────────

func TestCrewFiles_NoWorkspace(t *testing.T) {
	covNoWorkspaceCLI(t)
	if err := crewFilesListCmd.RunE(crewFilesListCmd, []string{"alpha"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("list: %v", err)
	}
	if err := crewFilesGetCmd.RunE(crewFilesGetCmd, []string{"alpha", "p"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("get: %v", err)
	}
	if err := crewFilesSaveCmd.RunE(crewFilesSaveCmd, []string{"alpha", "p"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("save: %v", err)
	}
}

func TestCrewFiles_UnknownCrew(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covCrewFilesStub(s)

	if err := crewFilesListCmd.RunE(crewFilesListCmd, []string{"ghost"}); err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Errorf("list: %v", err)
	}
	if err := crewFilesGetCmd.RunE(crewFilesGetCmd, []string{"ghost", "p"}); err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Errorf("get: %v", err)
	}
	if err := crewFilesSaveCmd.RunE(crewFilesSaveCmd, []string{"ghost", "p"}); err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Errorf("save: %v", err)
	}
}

func TestCrewFilesListRunE_Formats(t *testing.T) {
	newListStub := func(t *testing.T) *clitest.StubServer {
		t.Helper()
		s := clitest.NewStubServer()
		t.Cleanup(s.Close)
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnGet("/api/v1/crews/"+covCrewIDCli7+"/files", clitest.JSONResponse(200, []map[string]any{
			{"name": "notes.md", "size": 7},
		}))
		return s
	}
	origFormat := flagFormat
	t.Cleanup(func() { flagFormat = origFormat })

	t.Run("json", func(t *testing.T) {
		newListStub(t)
		flagFormat = "json"
		out, err := covCaptureStdoutCli7(t, func() error {
			return crewFilesListCmd.RunE(crewFilesListCmd, []string{"alpha"})
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"notes.md"`) {
			t.Errorf("json output = %q", out)
		}
	})

	t.Run("yaml", func(t *testing.T) {
		newListStub(t)
		flagFormat = "yaml"
		out, err := covCaptureStdoutCli7(t, func() error {
			return crewFilesListCmd.RunE(crewFilesListCmd, []string{"alpha"})
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "notes.md") {
			t.Errorf("yaml output = %q", out)
		}
	})

	t.Run("jq filter falls back to raw JSON when jq missing", func(t *testing.T) {
		newListStub(t)
		flagFormat = ""
		// Force the "jq unavailable" branch via the documented injection
		// point so the test never execs a real binary.
		origLookPath := lookPath
		lookPath = func(string) (string, error) { return "", os.ErrNotExist }
		t.Cleanup(func() { lookPath = origLookPath })

		if err := crewFilesListCmd.Flags().Set("filter", ".[].name"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = crewFilesListCmd.Flags().Set("filter", "") })

		out, err := covCaptureStdoutCli7(t, func() error {
			return crewFilesListCmd.RunE(crewFilesListCmd, []string{"alpha"})
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `"name": "notes.md"`) {
			t.Errorf("fallback output should be raw JSON: %q", out)
		}
	})
}

func TestCrewFilesGetRunE_TransportAndAtomicErrors(t *testing.T) {
	t.Run("transport error", func(t *testing.T) {
		saveCLIState(t)
		flagServer = ""
		flagWorkspace = ""
		cliCfg = &cli.CLIConfig{Token: "test-token", Workspace: covWSCli7, Server: covDeadURL(t)}
		_ = crewFilesGetCmd.Flags().Set("out", "")
		err := crewFilesGetCmd.RunE(crewFilesGetCmd, []string{covCrewIDCli7, "p"})
		if err == nil || !strings.Contains(err.Error(), "request failed") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("atomic file in missing directory", func(t *testing.T) {
		s := clitest.NewStubServer()
		defer s.Close()
		covSetupCLI(t, s)
		covCrewFilesStub(s)
		s.OnGet("/api/v1/crews/"+covCrewIDCli7+"/files/download", clitest.TextResponse(200, "x"))
		dest := filepath.Join(t.TempDir(), "no-such-dir", "f.bin")
		if err := crewFilesGetCmd.Flags().Set("out", dest); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = crewFilesGetCmd.Flags().Set("out", "") })

		if err := crewFilesGetCmd.RunE(crewFilesGetCmd, []string{"alpha", "p"}); err == nil {
			t.Fatal("expected atomic-file creation error")
		}
	})
}

func TestCrewFilesSaveRunE_StdinDefault(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covCrewFilesStub(s)
	savePath := "/api/v1/crews/" + covCrewIDCli7 + "/files/save"
	s.OnPut(savePath, clitest.EmptyResponse(204))

	// Neither --content nor --file → body comes from stdin.
	crewFilesSaveCmd.Flags().Lookup("content").Changed = false
	crewFilesSaveCmd.Flags().Lookup("file").Changed = false
	_ = crewFilesSaveCmd.Flags().Set("file", "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("from stdin"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	if err := crewFilesSaveCmd.RunE(crewFilesSaveCmd, []string{"alpha", "shared/in.txt"}); err != nil {
		t.Fatalf("save from stdin: %v", err)
	}
	puts := s.CallsFor("PUT", savePath)
	if len(puts) != 1 || string(puts[0].Body) != "from stdin" {
		t.Errorf("uploaded body = %q", puts[0].Body)
	}
}

func TestCrewFilesSaveRunE_UnreadableFile(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCLI(t, s)
	covCrewFilesStub(s)

	src := filepath.Join(t.TempDir(), "secret.bin")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o600) })

	crewFilesSaveCmd.Flags().Lookup("content").Changed = false
	if err := crewFilesSaveCmd.Flags().Set("file", src); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = crewFilesSaveCmd.Flags().Set("file", "")
		crewFilesSaveCmd.Flags().Lookup("file").Changed = false
	})

	err := crewFilesSaveCmd.RunE(crewFilesSaveCmd, []string{"alpha", "p"})
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("got %v", err)
	}
}

func TestPutBytes_TransportError(t *testing.T) {
	client := covDeadClient(t)
	err := putBytes(context.Background(), client, "/api/v1/x", strings.NewReader("x"))
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("got %v", err)
	}
}
