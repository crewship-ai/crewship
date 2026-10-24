package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestReviewStrictPrivacyLoopToolMetadataNeverReachesHostLog(t *testing.T) {
	const marker = "synthetic-private-tool-name"
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"loop","name":"` + marker + `","input":{"private":"value"}}]}}` + "\n"
	var log bytes.Buffer
	o := New(covNewRunContainer(covRunOpts{stream: strings.Repeat(line, loopGuardThreshold)}), newMemState(), slog.New(slog.NewJSONHandler(&log, nil)))
	_ = o.RunAgent(context.Background(), covRunReq().WithStrictContentPrivacy(), nil)
	if !strings.Contains(log.String(), "loop guard tripped") {
		t.Fatal("fixture did not trigger actual loop guard")
	}
	if strings.Contains(log.String(), marker) {
		t.Fatal("child-controlled private tool metadata reached host log")
	}
}
func TestReviewStrictPrivacyInspectFailureDoesNotPersistDiagnostic(t *testing.T) {
	const marker = "synthetic-private-inspect-diagnostic"
	var log bytes.Buffer
	j := &covJournal{}
	o := New(covNewRunContainer(covRunOpts{agentInspectErr: errors.New(marker)}), newMemState(), slog.New(slog.NewJSONHandler(&log, nil)))
	o.SetJournal(j)
	err := o.RunAgent(context.Background(), covRunReq().WithStrictContentPrivacy(), nil)
	if !errors.Is(err, ErrPrivateRunUnqualified) {
		t.Fatal("fixture did not produce opaque private failure", err)
	}
	if strings.Contains(log.String(), marker) {
		t.Error("private inspect diagnostic reached host log")
	}
	for _, entry := range j.byType("exec.command") {
		if strings.Contains(entry.Summary, marker) {
			t.Error("private inspect diagnostic persisted in command summary")
		}
		if message, _ := entry.Payload["inspect_error"].(string); strings.Contains(message, marker) {
			t.Error("private inspect diagnostic persisted in command payload")
		}
	}
}

func TestReviewStrictPrivacyPanicDoesNotExposeDiagnostic(t *testing.T) {
	const marker = "synthetic-private-inspect-panic"
	container := covNewRunContainer(covRunOpts{})
	original := container.inspect
	container.inspect = func(id string) (bool, int, error) {
		if id == "agent-exec" {
			panic(marker)
		}
		return original(id)
	}
	o := New(container, newMemState(), covQuietLogger())
	var caught any
	func() {
		defer func() { caught = recover() }()
		_ = o.RunAgent(context.Background(), covRunReq().WithStrictContentPrivacy(), nil)
	}()
	if strings.Contains(fmt.Sprint(caught), marker) {
		t.Fatal("private panic diagnostic reached caller and telemetry panic path")
	}
}
