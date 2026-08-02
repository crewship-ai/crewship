package chatbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/admission"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// heldToDeath produces the error a real admission hold dies with: a real
// Controller, a floor the host can never meet, and a deadline that runs out
// while the start is still held — then wrapped exactly the way
// (*docker.Provider).admitContainerStart wraps it.
//
// Built from the real gate on purpose. A hand-written string would let the
// classifier be "fixed" against wording that the gate never produces, which
// is how the defect this test covers got in.
func heldToDeath(t *testing.T) error {
	t.Helper()

	c := admission.New(
		func(context.Context) admission.Limits { return admission.Limits{RequiredFreeMB: 62048} },
		func() (admission.HostMemory, error) {
			return admission.HostMemory{AvailableMB: 41837, TotalMB: 128000, SomeStallPct: admission.PressureUnknown}, nil
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	release, err := c.Admit(ctx, "crew-x", "alpha", nil)
	if err == nil {
		release()
		t.Fatal("the gate admitted a start against a floor the host cannot meet; this test needs a real hold")
	}
	return fmt.Errorf("admission control refused a container start for crew %s: %w", "crew-x", err)
}

// A start held until its deadline must be reported as a CAPACITY failure that
// names the host resource that ran out and its numbers — not as a generic
// provisioning error.
//
// This asserts the message a user actually sees for a REAL gate error, not
// that the switch contains a capacity case. The difference is the whole bug:
// the gate's wrapper contains the substring "container start", so a capacity
// case placed below the provision_failed case would never fire and a
// shape-assertion would still pass.
func TestClassifyCrewRuntimeError_CapacityHoldNamesTheResourceAndTheNumbers(t *testing.T) {
	t.Parallel()

	err := heldToDeath(t)

	// The collision this test exists for, stated rather than assumed.
	if !strings.Contains(strings.ToLower(err.Error()), "container start") {
		t.Fatalf("the gate's error no longer contains %q, so this test is no longer covering the collision it was written for: %v",
			"container start", err)
	}

	code, msg := classifyCrewRuntimeError(err)

	if code != "capacity" {
		t.Errorf("code = %q, want %q — a start held to its deadline was classified as something else, "+
			"so the one failure mode with an actionable cause is reported without it", code, "capacity")
	}
	if strings.Contains(msg, "provisioning error") {
		t.Errorf("message = %q; a capacity hold is not a provisioning error", msg)
	}
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "memory") {
		t.Errorf("message = %q; it never names memory, the host resource that ran out", msg)
	}
	// The numbers the gate already knew. Asserting the values, not that some
	// detail was carried: a message that says "the host is short of memory"
	// and drops the figures sends the operator to the journal for something
	// the error had in hand.
	for _, want := range []string{"41837", "62048"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message = %q; it drops %q, a figure the gate produced", msg, want)
		}
	}
}

// The sentinel is the point. Classifying by substring is what broke, so the
// replacement has to be structural — and it must not cost the caller the
// underlying context error, which other code (and operators reading logs)
// still relies on.
func TestClassifyCrewRuntimeError_CapacityHoldIsStructurallyIdentifiable(t *testing.T) {
	t.Parallel()

	err := heldToDeath(t)

	var held *admission.HoldExpiredError
	if !errors.As(err, &held) {
		t.Fatalf("the hold failure is not an *admission.HoldExpiredError: %v", err)
	}
	if held.Reason != admission.ReasonHostMemory {
		t.Errorf("held.Reason = %q, want %q", held.Reason, admission.ReasonHostMemory)
	}
	if !strings.Contains(held.Detail, "41837") || !strings.Contains(held.Detail, "62048") {
		t.Errorf("held.Detail = %q; it does not carry the figures the gate computed", held.Detail)
	}
	if held.Waited <= 0 {
		t.Errorf("held.Waited = %v, want the time the start actually spent waiting", held.Waited)
	}
	if !errors.Is(err, admission.ErrHeldForCapacity) {
		t.Error("errors.Is(err, admission.ErrHeldForCapacity) is false; the failure cannot be classified structurally")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("errors.Is(err, context.DeadlineExceeded) is false; the typed error swallowed the context cause")
	}
}

// Every other classification the switch makes has to keep working — a new
// case ordered above them is exactly how one gets stolen.
func TestClassifyCrewRuntimeError_OtherCausesUnchangedByTheCapacityCase(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"plain container start", errors.New("container start: no such image"), "provision_failed"},
		{"container create", errors.New("container create: bad mount"), "provision_failed"},
		{"resource limit", errors.New("insufficient memory for container"), "resource_limit"},
		{"image missing", errors.New("image missing locally, needs reprovisioning"), "image_missing"},
		{"legacy volume", errors.New("legacy volume must be migrated"), "legacy_volume_conflict"},
		{"unknown", errors.New("something else entirely"), "internal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, _ := classifyCrewRuntimeError(tc.err); code != tc.want {
				t.Errorf("code = %q, want %q", code, tc.want)
			}
		})
	}
}

// ---------- Part 2: the hold has to be visible while it is happening ----------

// A hold reported by the provider must reach the caller's own stream. Without
// this the chat shows "Starting container..." for up to thirty minutes and a
// queued run is indistinguishable from a hung one.
func TestBridge_CapacityHold_ReachesTheCallersStream(t *testing.T) {
	resolver := &mockResolver{
		info: &ChatInfo{
			AgentID:     "agent-1",
			AgentSlug:   "valid-slug",
			CrewID:      "crew-held",
			CrewSlug:    "ops",
			CLIAdapter:  "CLAUDE_CODE",
			ToolProfile: "CODING",
			TimeoutSecs: 30,
			MemoryMB:    2048,
		},
	}
	b, ctr := bridgeWithCapturingContainer(t, resolver, BridgeConfig{})

	// Stand in for the gate: the provider reports one hold, then a step that
	// is NOT a hold, on the sink the bridge handed it.
	ctr.onEnsure = func(cc provider.CrewConfig) {
		if cc.ProvisionSink == nil {
			t.Error("the bridge wired no ProvisionSink, so a capacity hold can never reach the caller")
			return
		}
		cc.ProvisionSink(devcontainer.ProvisionEvent{
			Step:       devcontainer.ProvStepCapacityHold,
			Status:     devcontainer.ProvStatusStarted,
			Reason:     admission.ReasonHostMemory,
			Detail:     "host has 41837 MiB available, 62048 MiB needed for one more agent container",
			DurationMs: 125_000,
		})
		cc.ProvisionSink(devcontainer.ProvisionEvent{
			Step:   devcontainer.ProvStepContainerCreate,
			Status: devcontainer.ProvStatusCompleted,
			Detail: "captured-container-id",
		})
	}

	var statuses []string
	streamFn := func(e ws.ChatEvent) {
		if e.Type == "status" {
			statuses = append(statuses, e.Content)
		}
	}
	_ = b.HandleChatMessage(context.Background(), "u", "sess-held", "hello", streamFn)

	var hold string
	for _, s := range statuses {
		if strings.Contains(strings.ToLower(s), "capacity") {
			hold = s
		}
	}
	if hold == "" {
		t.Fatalf("no capacity line on the caller's stream; the wait is silent. statuses = %v", statuses)
	}
	if !strings.Contains(strings.ToLower(hold), "memory") {
		t.Errorf("capacity line = %q; it never says which host resource is short", hold)
	}
	for _, want := range []string{"41837", "62048"} {
		if !strings.Contains(hold, want) {
			t.Errorf("capacity line = %q; it drops %q", hold, want)
		}
	}
	// "roughly how long it has been going" — 125s must read as a duration a
	// person can act on, not as a raw millisecond count.
	if !strings.Contains(hold, "2m5s") {
		t.Errorf("capacity line = %q; it does not say how long the start has been waiting (want 2m5s)", hold)
	}

	// And nothing else from the provisioning stream is allowed onto the chat:
	// this sink exists for the hold, not to replay the whole pipeline.
	for _, s := range statuses {
		if strings.Contains(s, "captured-container-id") {
			t.Errorf("a non-hold provisioning step leaked onto the chat stream: %q", s)
		}
	}
}
