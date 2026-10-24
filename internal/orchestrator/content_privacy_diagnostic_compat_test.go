package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/telemetry"
)

func TestOrdinaryRunRetainsLoopAndInspectDiagnostics(t *testing.T) {
	const marker = "synthetic-ordinary-diagnostic"
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"loop","name":"` + marker + `","input":{"value":"ordinary"}}]}}` + "\n"
	for _, opts := range []covRunOpts{{stream: strings.Repeat(line, loopGuardThreshold)}, {agentInspectErr: errors.New(marker)}} {
		var log bytes.Buffer
		o := New(covNewRunContainer(opts), newMemState(), slog.New(slog.NewJSONHandler(&log, nil)))
		_ = o.RunAgent(context.Background(), covRunReq(), nil)
		if !strings.Contains(log.String(), marker) {
			t.Fatal("ordinary diagnostic unexpectedly suppressed")
		}
	}
}

func TestStrictContentPrivacyPanicIsOpaqueAndKeepsUnconfirmedAdmissions(t *testing.T) {
	const marker = "synthetic-private-panic"
	for _, private := range []bool{false, true} {
		c := covNewRunContainer(covRunOpts{})
		inspect := c.inspect
		c.inspect = func(id string) (bool, int, error) {
			if id == "agent-exec" {
				panic(marker)
			}
			return inspect(id)
		}
		o := New(c, newMemState(), covQuietLogger())
		req := covRunReq()
		if private {
			req = req.WithStrictContentPrivacy()
		}
		var caught any
		var err error
		func() { defer func() { caught = recover() }(); err = o.RunAgent(context.Background(), req, nil) }()
		if private {
			if caught != nil || !errors.Is(err, ErrPrivateRunUnqualified) {
				t.Fatal("private panic did not return normal opaque error")
			}
		} else {
			panicValue, ok := caught.(*telemetry.PanicWithStack)
			if !ok || panicValue.Value != marker {
				t.Fatal("ordinary panic handling changed")
			}
		}
		if _, active := o.agentRuns.Load(req.RunID); active {
			t.Fatal("panic leaked tracked run")
		}
		if private {
			if id, held := o.detachedHoldRunID(req.AgentID); !held || id != req.RunID {
				t.Fatal("private panic lost the unconfirmed original runtime")
			}
			if release, admissionErr := o.acquireAgentAdmissionMode(context.Background(), req.AgentID, true); admissionErr == nil {
				release()
				t.Fatal("private panic allowed a second runtime without death confirmation")
			}
			continue
		}
		release, admissionErr := o.acquireAgentAdmissionMode(context.Background(), req.AgentID, true)
		if admissionErr != nil {
			t.Fatal("panic leaked agent admission", admissionErr)
		}
		release()
		release, admissionErr = o.acquireServerAdmission(context.Background(), true)
		if admissionErr != nil {
			t.Fatal("panic leaked server admission", admissionErr)
		}
		release()
	}
}

type privatePreSpanPanicState struct {
	*memState
	reads int
}

func (s *privatePreSpanPanicState) Get(context.Context, string, string) ([]byte, error) {
	s.reads++
	panic("synthetic-private-pre-span-state")
}

func TestStrictContentPrivacyRefusesUnsupportedCaptureBeforeStateRead(t *testing.T) {
	state := &privatePreSpanPanicState{memState: newMemState()}
	o := New(covNewRunContainer(covRunOpts{}), state, covQuietLogger())
	req := covRunReq().WithStrictContentPrivacy()
	req.HostOutputDir = t.TempDir()
	var caught any
	var err error
	func() { defer func() { caught = recover() }(); err = o.RunAgent(context.Background(), req, nil) }()
	if caught != nil || !errors.Is(err, ErrPrivateRunUnqualified) || state.reads != 0 {
		t.Fatal("unsupported private capture reached pre-span state/panic boundary")
	}
}
