package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestWaitConsumersTerminalOutcomes(t *testing.T) {
	for _, status := range []string{"needs_reconciliation", "canceled", "cancelled", "completed", "dry_run"} {
		for _, format := range []string{"table", "json"} {
			for _, consumer := range []string{"wait", "routine"} {
				t.Run(consumer+"/"+status+"/"+format, func(t *testing.T) {
					s := clitest.NewStubServer()
					defer s.Close()
					s.OnGet("/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipeline-runs/review-run", clitest.JSONResponse(200, map[string]any{"id": "review-run", "status": status, "output": "must-only-print-on-success"}))
					s.OnPost("/api/v1/workspaces/"+covWorkspaceIDCli10+"/pipelines/review/run", clitest.JSONResponse(200, map[string]any{"run_id": "review-run", "status": "DEDUPED"}))
					cfg := filepath.Join(t.TempDir(), "cli.yaml")
					if err := os.WriteFile(cfg, []byte("server: "+s.URL()+"\nworkspace: "+covWorkspaceIDCli10+"\ntoken: fixture-token\n"), 0600); err != nil {
						t.Fatal(err)
					}
					args := []string{"wait", "review-run", "--routine", "--format", format}
					if consumer == "routine" {
						args = []string{"routine", "run", "review", "--wait", "--format", format}
					}
					child := exec.Command(buildCrewshipBinary(t), args...)
					child.Env = append(os.Environ(), "CREWSHIP_CONFIG="+cfg)
					var out, diagnostics bytes.Buffer
					child.Stdout = &out
					child.Stderr = &diagnostics
					err := child.Run()
					code := 0
					if err != nil {
						var exit *exec.ExitError
						if !errors.As(err, &exit) {
							t.Fatal(err)
						}
						code = exit.ExitCode()
					}
					want := 0
					if status == "needs_reconciliation" {
						want = 5
					} else if status == "canceled" || status == "cancelled" {
						want = 2
					}
					if consumer == "routine" && want != 0 {
						want = 1
					}
					if code != want {
						t.Errorf("exit=%d want=%d stdout=%q stderr=%q", code, want, out.String(), diagnostics.String())
					}
					expected := strings.ToUpper(status)
					if format == "json" {
						expected = `"status": "` + status + `"`
					}
					if !strings.Contains(out.String(), expected) {
						t.Errorf("terminal output missing %q: %s", expected, out.String())
					}
					if want != 0 && format == "table" && strings.Contains(out.String(), "Final output:") {
						t.Errorf("failure printed successful final output: %s", out.String())
					}
				})
			}
		}
	}
}

func TestAIReconnectFailurePreservesRegistration(t *testing.T) {
	// Keep real client subprocesses without depending on a POSIX shell or its
	// utilities. Build once, then install the fixture under each client's name.
	fixture := buildAIClientFixture(t)
	for _, client := range []string{"claude", "codex"} {
		for _, mode := range []string{"remove-fails", "add-fails", "verify-fails", "verify-error", "success", "success-public-mode", "receipt-fails-public-mode", "race", "receipt-race", "symlink", "custom", "receipt-fails", "receipt-fails-after-rename", "config-fails-after-rename", "rollback-fails"} {
			t.Run(client+"/"+mode, func(t *testing.T) {
				base := strings.TrimSuffix(mode, "-public-mode")
				fileMode := os.FileMode(0640)
				if strings.HasSuffix(mode, "-public-mode") {
					fileMode = 0644
				}
				dir := t.TempDir()
				t.Setenv("CLAUDE_CONFIG_DIR", dir)
				t.Setenv("CODEX_HOME", dir)
				oldDir := aiConnectionDir
				aiConnectionDir = func() (string, error) { return dir, nil }
				t.Cleanup(func() { aiConnectionDir = oldDir })
				path, err := aiClientContext(client)
				if err != nil {
					t.Fatal(err)
				}
				old := aiEntry{Type: "stdio", Command: "/installed/crewship", Args: []string{"mcp", "serve"}}
				desired := old
				desired.Args = []string{"mcp", "serve", "--allow-write"}
				config := func(entry aiEntry) []byte {
					var value any = map[string]any{"mcpServers": map[string]any{"crewship": entry, "other": map[string]any{"command": "keep"}}, "setting": "keep"}
					if client == "codex" {
						value = map[string]any{"transport": entry, "setting": "keep"}
					}
					data, _ := json.Marshal(value)
					return data
				}
				baseline := config(old)
				if err = os.WriteFile(path, baseline, 0600); err != nil {
					t.Fatal(err)
				}
				desiredFile := filepath.Join(dir, "desired.json")
				if err := os.WriteFile(desiredFile, config(desired), 0600); err != nil {
					t.Fatal(err)
				}
				empty := `{"mcpServers":{"other":{"command":"keep"}},"setting":"keep"}`
				if client == "codex" {
					empty = ""
				}
				if err := os.WriteFile(filepath.Join(dir, "empty.json"), []byte(empty), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("FIXTURE_DIR", dir)
				t.Setenv("FIXTURE_MODE", base)
				t.Setenv("FIXTURE_ORIGINAL", path)
				fixtureName := client
				if runtime.GOOS == "windows" {
					fixtureName += ".exe"
				}
				if err = os.WriteFile(filepath.Join(dir, fixtureName), fixture, 0700); err != nil {
					t.Fatal(err)
				}
				// Only the fixture is available during reconnect: no shell utilities
				// or installed native client can accidentally satisfy the test.
				t.Setenv("PATH", dir)
				entry, found, err := inspectAIEntry(context.Background(), client)
				if err != nil || !found {
					t.Fatal(found, err)
				}
				if err = saveAIReceipt(client, entry); err != nil {
					t.Fatal(err)
				}
				receiptPath, _ := aiReceiptPath(client)
				receiptBefore, _ := os.ReadFile(receiptPath)
				if info, err := os.Stat(receiptPath); err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("new receipt must be private: info=%v err=%v", info, err)
				}
				t.Setenv("FIXTURE_RECEIPT", receiptPath)
				if err := os.Chmod(path, fileMode); err != nil {
					t.Fatal(err)
				}
				if base == "symlink" {
					backup := path + ".original"
					if err := os.Rename(path, backup); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(backup, path); err != nil {
						t.Fatal(err)
					}
				}
				if base == "custom" {
					baseline = config(desired)
					if err := os.WriteFile(path, baseline, 0600); err != nil {
						t.Fatal(err)
					}
				}
				cmd := &cobra.Command{}
				cmd.SetContext(context.Background())
				cmd.SetOut(&bytes.Buffer{})
				cmd.SetErr(&bytes.Buffer{})
				originalWriter := aiWriteConnectionFile
				t.Cleanup(func() { aiWriteConnectionFile = originalWriter })
				writes := 0
				aiWriteConnectionFile = func(target string, data []byte, perm os.FileMode) error {
					writes++
					if base == "receipt-fails" && target == receiptPath {
						return errors.New("fixture receipt failure")
					}
					if base == "rollback-fails" && writes >= 2 {
						return errors.New("fixture rollback failure")
					}
					if err := originalWriter(target, data, perm); err != nil {
						return err
					}
					if base == "receipt-fails-after-rename" && writes == 2 {
						return errors.New("fixture receipt fsync failure")
					}
					if base == "config-fails-after-rename" && writes == 1 {
						return errors.New("fixture config fsync failure")
					}
					return nil
				}
				registration, _ := aiRegistrationArgs(client, desired.Command, desired.Args)
				err = connectAIClient(cmd, client, desired.Command, desired.Args, registration)
				stageDirectories, globErr := filepath.Glob(filepath.Join(dir, ".crewship-connect-*"))
				if globErr != nil || len(stageDirectories) != 0 {
					t.Fatal("failed to clean staged configuration", stageDirectories, globErr)
				}
				info, statErr := os.Stat(path)
				if statErr != nil || info.Mode().Perm() != fileMode {
					t.Fatal("changed config file mode", statErr)
				}
				if base == "success" {
					if err != nil {
						t.Fatal(err)
					}
					actual, ok, e := inspectAIEntry(context.Background(), client)
					if e != nil || !ok || !ownsAIEntry(client, actual) || !strings.Contains(string(actual.Raw), "--allow-write") {
						t.Fatal("replacement not owned", e)
					}
					data, err := os.ReadFile(path)
					if err != nil || !strings.Contains(string(data), `"setting":"keep"`) {
						t.Fatal("unrelated settings lost", err)
					}
					return
				}
				if err == nil {
					t.Fatal("failure or concurrent edit accepted")
				}
				if base == "rollback-fails" {
					if !strings.Contains(err.Error(), "restoration of previous configuration could not be confirmed") || strings.Contains(err.Error(), "receipt restored") {
						t.Fatal("claimed unconfirmed rollback succeeded", err)
					}
					return
				}
				current, _ := os.ReadFile(path)
				receiptAfter, _ := os.ReadFile(receiptPath)
				if base == "race" {
					if !strings.Contains(string(current), "concurrent-change") {
						t.Fatal("overwrote concurrent edit")
					}
				} else if !bytes.Equal(current, baseline) {
					t.Errorf("registration lost: %s", current)
				}
				if base == "receipt-race" {
					if !strings.Contains(string(receiptAfter), "concurrent-change") {
						t.Fatal("overwrote concurrent receipt edit")
					}
				} else if !bytes.Equal(receiptBefore, receiptAfter) {
					t.Errorf("ownership receipt lost or changed: %s", receiptAfter)
				}
			})
		}
	}
}
