package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestPrivateRunJournalBoundaryDropsUntrustedStrings(t *testing.T) {
	const marker = "synthetic-private-boundary-diagnostic"
	for _, private := range []bool{false, true} {
		j := &covJournal{}
		o := New(nil, newMemState(), covQuietLogger())
		o.SetJournal(j)
		req := covRunReq()
		if private {
			req = req.WithStrictContentPrivacy()
		}
		payload := map[string]any{"error": marker, "cmd": []string{marker}, "reason": marker, "duration_ms": int64(7), "exit_code": 1, "refused": true, "stopped": marker}
		_, err := o.journalForRun(req).Emit(context.Background(), JournalEntry{Type: "exec.command", Summary: marker, Payload: payload, Refs: map[string]any{"private": marker}})
		if err != nil {
			t.Fatal(err)
		}
		entries := j.byType("exec.command")
		if len(entries) != 1 {
			t.Fatal("missing metadata event")
		}
		raw, _ := json.Marshal(entries[0])
		if strings.Contains(string(raw), marker) == private {
			t.Fatal("request-local journal selection failed")
		}
		if private && (entries[0].Payload["duration_ms"] != int64(7) || entries[0].Payload["refused"] != true || entries[0].Payload["stopped"] != nil) {
			t.Fatal("metadata whitelist failed")
		}
		if payload["error"] != marker {
			t.Fatal("boundary mutated caller payload")
		}
	}
}

type privacySinkErrorState struct{ *memState }

func (s privacySinkErrorState) Get(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("synthetic-private-helper-diagnostic")
}

func TestPrivateRunCleanupAndStatusUseOriginalLocalSink(t *testing.T) {
	const marker = "synthetic-private-helper-diagnostic"
	for _, private := range []bool{false, true} {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		c := covNewRunContainer(covRunOpts{})
		cleanupCalls := 0
		c.route = func(provider.ExecConfig) (*provider.ExecResult, error) {
			cleanupCalls++
			return nil, errors.New(marker)
		}
		o := New(c, privacySinkErrorState{newMemState()}, logger)
		req := covRunReq()
		if private {
			req = req.WithStrictContentPrivacy()
		}
		o.updateRunStatusForRun(context.Background(), req, req.RunID, "error")
		o.cleanupAgentSecretsWithLogger(req.ContainerID, req.AgentSlug, req.RunID, o.loggerForRun(req))
		o.cleanupRunHomeWithLogger(req.ContainerID, req.AgentSlug, req.RunID, "", o.loggerForRun(req))
		if cleanupCalls != 2 {
			t.Fatal("fixture did not execute both cleanup paths")
		}
		if strings.Contains(logs.String(), marker) == private {
			t.Fatal("helper diagnostic selection failed")
		}
		if o.logger != logger {
			t.Fatal("request changed shared logger")
		}
		logger.Warn("ordinary-after-private")
		if !strings.Contains(logs.String(), "ordinary-after-private") {
			t.Fatal("ordinary shared sink suppressed")
		}
	}
}

func TestOrdinaryRunCreationAndGateRetainDiagnostics(t *testing.T) {
	const marker = "synthetic-ordinary-create-diagnostic"
	for _, gate := range []bool{false, true} {
		var logs bytes.Buffer
		c := covNewRunContainer(covRunOpts{})
		route := c.route
		c.route = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			if !gate && len(cfg.Cmd) > 0 && cfg.Cmd[0] == "stdbuf" {
				return nil, errors.New(marker)
			}
			return route(cfg)
		}
		j := &covJournal{}
		o := New(c, newMemState(), slog.New(slog.NewJSONHandler(&logs, nil)))
		o.SetJournal(j)
		req := covRunReq()
		if gate {
			req.ExecGate = func(context.Context) error { return errors.New(marker) }
		}
		_ = o.RunAgent(context.Background(), req, nil)
		raw, _ := json.Marshal(j.byType("exec.command"))
		if !strings.Contains(logs.String(), marker) || !strings.Contains(string(raw), marker) {
			t.Fatal("ordinary diagnostic changed")
		}
	}
}

func TestPrivateRunSidecarHealthDoesNotReachSharedLogger(t *testing.T) {
	const marker = "synthetic-private-sidecar-health"
	for _, private := range []bool{false, true} {
		var logs bytes.Buffer
		c := covNewRunContainer(covRunOpts{})
		route := c.route
		c.route = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			if strings.Contains(covScript(cfg), "127.0.0.1:9119/health") {
				return covResult("health", `{"status":"ok","network_mode":"`+marker+`"}`), nil
			}
			return route(cfg)
		}
		o := New(c, newMemState(), slog.New(slog.NewJSONHandler(&logs, nil)))
		o.sidecarEnabled = true
		req := covRunReq()
		if private {
			req = req.WithStrictContentPrivacy()
		}
		_ = o.RunAgent(context.Background(), req, nil)
		if strings.Contains(logs.String(), marker) == private {
			t.Fatal("sidecar health diagnostic selection failed")
		}
	}
}

func TestPrivateDetachedInspectionUsesSelectedLogger(t *testing.T) {
	const marker = "synthetic-private-exec-id"
	for _, private := range []bool{false, true} {
		var logs bytes.Buffer
		c := covNewRunContainer(covRunOpts{})
		c.inspect = func(string) (bool, int, error) { return true, 0, nil }
		o := New(c, newMemState(), slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		req := covRunReq()
		if private {
			req = req.WithStrictContentPrivacy()
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		running, _, err := o.awaitExecTerminalWithLogger(ctx, marker, o.loggerForRun(req))
		if !running || err != nil {
			t.Fatal("fixture did not inspect live detached exec")
		}
		if strings.Contains(logs.String(), marker) == private {
			t.Fatal("detached inspection diagnostic selection failed")
		}
	}
}
