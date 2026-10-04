//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeProcessFixture(t *testing.T) {
	mode := os.Getenv("CREWSHIP_NATIVE_PROCESS_FIXTURE")
	if mode == "" {
		return
	}
	emit := func(value any) {
		if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
			os.Exit(3)
		}
	}
	message := func(text string) {
		emit(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": text, "provider_private": "never forwarded"}})
	}
	complete := func() { emit(map[string]string{"type": "turn.completed"}) }
	switch mode {
	case "invalid-json":
		fmt.Fprintln(os.Stdout, "{not-json")
	case "turn-failed", "error":
		typ := "turn.failed"
		if mode == "error" {
			typ = "error"
		}
		emit(map[string]string{"type": typ})
	case "unknown":
		emit(map[string]string{"type": "unclassified.provider.event"})
	case "bad-exit", "missing-exit", "bad-status":
		item := map[string]any{"type": "command_execution", "status": "completed", "exit_code": 0}
		if mode == "bad-exit" {
			item["exit_code"] = 1
		}
		if mode == "missing-exit" {
			delete(item, "exit_code")
		}
		if mode == "bad-status" {
			item["status"] = "failed"
		}
		emit(map[string]any{"type": "item.completed", "item": item})
		message("partial")
		complete()
	case "late":
		message("first")
		complete()
		message("late text")
	case "no-completion":
		message("partial")
	case "no-text":
		complete()
	case "text-limit":
		message(strings.Repeat("x", 32769))
		complete()
	case "frame-limit":
		fmt.Fprintln(os.Stdout, strings.Repeat("x", (256<<10)+1))
	case "write-blocked":
		message("initial")
		time.Sleep(time.Minute)
	case "success", "exit-failed":
		for _, typ := range []string{"thread.started", "turn.started", "item.started", "item.updated"} {
			emit(map[string]string{"type": typ})
		}
		emit(map[string]any{"type": "item.completed", "item": map[string]any{"type": "command_execution", "status": "completed", "exit_code": 0}})
		message("completed answer")
		complete()
		if mode == "exit-failed" {
			os.Exit(7)
		}
	default:
		os.Exit(4)
	}
	os.Exit(0)
}

func fixtureCommand(t *testing.T, mode string) (*exec.Cmd, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeProcessFixture$")
	cmd.Env = append(os.Environ(), "CREWSHIP_NATIVE_PROCESS_FIXTURE="+mode)
	return cmd, cancel
}

func TestNativeProcessPublishesOnlyValidatedTextArtifactsAndCompletion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("owned artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, cancel := fixtureCommand(t, "success")
	var out bytes.Buffer
	if err := runNativeProcess(cmd, cancel, &out, root); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&out)
	var text struct{ Type, Text string }
	if err := dec.Decode(&text); err != nil || text.Type != "text" || text.Text != "completed answer" {
		t.Fatalf("text frame: %+v %v", text, err)
	}
	var file artifact
	if err := dec.Decode(&file); err != nil || file.Type != "artifact" || file.Name != "result.txt" || string(file.Content) != "owned artifact" {
		t.Fatalf("artifact frame: %+v %v", file, err)
	}
	var done map[string]string
	if err := dec.Decode(&done); err != nil || len(done) != 1 || done["type"] != "done" {
		t.Fatalf("completion frame: %v %v", done, err)
	}
	if err := dec.Decode(&done); !errors.Is(err, io.EOF) {
		t.Fatalf("extra frames: %v", err)
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
		t.Fatal("success emitted without a reaped successful child")
	}
}

func TestNativeProcessRefusesFailedIncompleteAndOversizedProtocols(t *testing.T) {
	for _, mode := range []string{"invalid-json", "turn-failed", "error", "unknown", "bad-exit", "missing-exit", "bad-status", "late", "no-completion", "no-text", "text-limit", "frame-limit", "exit-failed"} {
		t.Run(mode, func(t *testing.T) {
			cmd, cancel := fixtureCommand(t, mode)
			var out bytes.Buffer
			if err := runNativeProcess(cmd, cancel, &out, t.TempDir()); err == nil {
				t.Fatal("invalid child was accepted")
			}
			if strings.Contains(out.String(), `"type":"done"`) || strings.Contains(out.String(), "provider_private") || strings.Contains(out.String(), "late text") {
				t.Fatalf("invalid completion or unclassified data: %.200s", out.String())
			}
			if cmd.ProcessState == nil {
				t.Fatal("failed child was not reaped")
			}
		})
	}
}

type failingNativeOutput struct {
	after, writes int
	data          bytes.Buffer
	err           error
}

func (w *failingNativeOutput) Write(p []byte) (int, error) {
	if w.writes == w.after {
		return 0, w.err
	}
	w.writes++
	return w.data.Write(p)
}

func TestNativeProcessReapsChildrenWhenOutputOrArtifactsFail(t *testing.T) {
	sentinel := errors.New("owned output failure")
	for _, after := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("artifact"), 0o600); err != nil {
				t.Fatal(err)
			}
			mode := "success"
			if after == 0 {
				mode = "write-blocked"
			}
			cmd, cancel := fixtureCommand(t, mode)
			output := &failingNativeOutput{after: after, err: sentinel}
			if err := runNativeProcess(cmd, cancel, output, root); !errors.Is(err, sentinel) {
				t.Fatalf("output error hidden: %v", err)
			}
			if cmd.ProcessState == nil {
				t.Fatal("output failure stranded child")
			}
			if strings.Contains(output.data.String(), `"type":"done"`) {
				t.Fatal("output failure claimed completion")
			}
		})
	}
	cmd, cancel := fixtureCommand(t, "success")
	var output bytes.Buffer
	if err := runNativeProcess(cmd, cancel, &output, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("artifact read failure accepted")
	}
	if strings.Contains(output.String(), `"type":"done"`) {
		t.Fatal("unreadable artifacts claimed completion")
	}
}

func TestNativeProcessRejectsUnstartableCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	missing := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing"))
	if err := runNativeProcess(missing, cancel, io.Discard, t.TempDir()); err == nil {
		t.Fatal("missing child accepted")
	}
	configured, _ := fixtureCommand(t, "success")
	configured.Stdout = io.Discard
	if err := runNativeProcess(configured, cancel, io.Discard, t.TempDir()); err == nil {
		t.Fatal("conflicting output pipe accepted")
	}
}
