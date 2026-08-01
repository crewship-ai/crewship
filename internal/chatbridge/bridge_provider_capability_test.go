package chatbridge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// reportingCapturingContainer is the capturing provider plus the optional
// capability report — i.e. what the apple-container provider looks like from
// the bridge's side.
type reportingCapturingContainer struct {
	capturingContainer
	report provider.CrewConfigSupport
}

func (c *reportingCapturingContainer) UnsupportedCrewConfig(_ provider.CrewConfig) provider.CrewConfigSupport {
	return c.report
}

type eventSink struct {
	mu     sync.Mutex
	events []ws.ChatEvent
}

func (s *eventSink) fn() func(ws.ChatEvent) {
	return func(e ws.ChatEvent) {
		s.mu.Lock()
		s.events = append(s.events, e)
		s.mu.Unlock()
	}
}

func (s *eventSink) contents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.events))
	for _, e := range s.events {
		out = append(out, e.Type+": "+e.Content)
	}
	return out
}

// bridgeWithReportingContainer mirrors bridgeWithCapturingContainer but wires a
// provider that also answers the capability question.
func bridgeWithReportingContainer(t *testing.T, resolver ChatResolver, report provider.CrewConfigSupport) (*Bridge, *reportingCapturingContainer) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.Default()
	convStore := conversation.NewStore(dir, logger)
	logWriter := logcollector.NewWriter(dir, logger)
	ctr := &reportingCapturingContainer{report: report}
	orch := orchestrator.New(ctr, &memState{data: make(map[string]map[string][]byte)}, logger)
	return New(orch, ctr, convStore, logWriter, resolver, BridgeConfig{}, logger), ctr
}

func chatInfoForCapabilityTest() *ChatInfo {
	return &ChatInfo{
		AgentID:     "agent-1",
		AgentSlug:   "valid-slug",
		CrewID:      "crew-1",
		CrewSlug:    "ops",
		CLIAdapter:  "CLAUDE_CODE",
		ToolProfile: "CODING",
		TimeoutSecs: 30,
		MemoryMB:    2048,
		CPUs:        2,
	}
}

// TestBridge_ProviderCapabilityDropsReachTheUser is the generalisation of the
// one-field precedent already in bridge.go ("Sidecar services declared but
// provider doesn't support them yet"). A drop the operator cannot see is the
// defect; the chat surface is where they are standing when the crew starts.
func TestBridge_ProviderCapabilityDropsReachTheUser(t *testing.T) {
	b, ctr := bridgeWithReportingContainer(t, &mockResolver{info: chatInfoForCapabilityTest()},
		provider.CrewConfigSupport{Degraded: []provider.DroppedField{
			{Field: "CachedImage", Value: "crewship-cache:abc", Detail: "runs from the base runtime image"},
			{Field: "TTLHours", Value: "4", Detail: "no idle auto-stop"},
		}})
	sink := &eventSink{}

	_ = b.HandleChatMessage(context.Background(), "u", "sess-cap-1", "hello", sink.fn())

	if ctr.createCalls.Load() == 0 {
		t.Fatal("a degraded config must still start the crew")
	}
	joined := strings.Join(sink.contents(), "\n")
	for _, want := range []string{"CachedImage", "runs from the base runtime image", "TTLHours"} {
		if !strings.Contains(joined, want) {
			t.Errorf("chat stream must carry %q; events:\n%s", want, joined)
		}
	}
}

// TestBridge_NoCapabilityNoiseWhenProviderHonoursEverything: the docker
// provider declares no report, and a crew there must see exactly the events it
// saw before this mechanism existed.
func TestBridge_NoCapabilityNoiseWhenProviderHonoursEverything(t *testing.T) {
	b, _ := bridgeWithCapturingContainer(t, &mockResolver{info: chatInfoForCapabilityTest()}, BridgeConfig{})
	sink := &eventSink{}

	_ = b.HandleChatMessage(context.Background(), "u", "sess-cap-2", "hello", sink.fn())

	for _, c := range sink.contents() {
		if strings.Contains(c, "not honoured") || strings.Contains(c, "provider") && strings.Contains(c, "drop") {
			t.Errorf("provider that honours everything must add no capability event, got %q", c)
		}
	}
}

// TestBridge_SidecarDropIsReportedOnce guards the seam between the new
// general report and the one-field precedent it generalises: a provider that
// names Services in its report AND does not implement SidecarProvider used to
// be two independent code paths, and the user would read the two messages as
// two separate faults.
func TestBridge_SidecarDropIsReportedOnce(t *testing.T) {
	info := chatInfoForCapabilityTest()
	info.ServicesJSON = `[{"name":"db","image":"postgres:16"}]`
	b, _ := bridgeWithReportingContainer(t, &mockResolver{info: info},
		provider.CrewConfigSupport{Degraded: []provider.DroppedField{
			{Field: "Services", Value: "db", Detail: "sidecar containers are not started"},
		}})
	sink := &eventSink{}

	_ = b.HandleChatMessage(context.Background(), "u", "sess-cap-3", "hello", sink.fn())

	var mentions int
	for _, c := range sink.contents() {
		if strings.Contains(c, "Services") || strings.Contains(c, "Sidecar services") {
			mentions++
		}
	}
	if mentions != 1 {
		t.Fatalf("the sidecar drop must be reported exactly once, got %d mentions in:\n%s",
			mentions, strings.Join(sink.contents(), "\n"))
	}
}

// TestClassifyCrewRuntimeError_ConfigRefusalIsItsOwnCode pins that a refusal
// does not land in the "internal" bucket, where it would reach the user as
// "The agent container could not be started." — a message that names neither
// the control that was refused nor anything they can act on.
func TestClassifyCrewRuntimeError_ConfigRefusalIsItsOwnCode(t *testing.T) {
	refusal := provider.CrewConfigSupport{Refused: []provider.DroppedField{{
		Field: "NetworkMode", Value: "restricted",
		Detail: "no proxy binary reaches the container",
	}}}.RefusedError("apple-container")

	code, msg := classifyCrewRuntimeError(fmt.Errorf("ensure team runtime: %w", refusal))
	if code != "provider_capability" {
		t.Fatalf("code = %q, want provider_capability", code)
	}
	if !strings.Contains(msg, "NetworkMode") {
		t.Errorf("user message must name the refused field; got %q", msg)
	}
	if !strings.Contains(msg, "restricted") {
		t.Errorf("user message must name the refused value; got %q", msg)
	}
}
