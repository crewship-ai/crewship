package chatbridge

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// legacyVolumeContainer passes the cached-image gate (present) then fails
// EnsureCrewRuntime with the real legacy-volume migration error — the exact
// cause the generic "failed to start agent container" message hides.
type legacyVolumeContainer struct {
	failContainer
	raw string
}

func (c *legacyVolumeContainer) ImagePresentLocally(_ context.Context, _ string) (bool, error) {
	return true, nil
}
func (c *legacyVolumeContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", fmt.Errorf("%s", c.raw)
}

// The masked-error bug: every EnsureCrewRuntime failure streamed the same
// opaque "failed to start agent container" while the real cause went only to
// logs. A legacy-volume conflict (the common cause) must reach the user with a
// classified, actionable message and a machine-readable code — the returned
// error still wraps the raw cause for logs/run record. RED on main (Content is
// the bare generic string; no Metadata code).
func TestContainerStartError_SurfacesCause(t *testing.T) {
	t.Parallel()
	raw := `C1 migration of legacy slug-scoped volume "old" into "new" failed at /data: disk full; the legacy volume was NOT removed`
	resolver := &mockResolver{info: cacheMissingInfo("crewship-cache:present")}
	b := testBridgeWithContainer(t, resolver, &legacyVolumeContainer{raw: raw})

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hello", streamFn)
	if err == nil || !strings.Contains(err.Error(), "ensure team runtime") {
		t.Fatalf("returned error must still wrap the raw cause for logs; got %v", err)
	}

	var errEvent *ws.ChatEvent
	for i := range events {
		if events[i].Type == "error" {
			errEvent = &events[i]
		}
	}
	if errEvent == nil {
		t.Fatalf("expected a streamed error event; got %+v", events)
	}
	// It must NOT be the opaque generic string, and must name the cause.
	if errEvent.Content == "failed to start agent container" {
		t.Errorf("error event still shows the opaque generic message: %q", errEvent.Content)
	}
	if !strings.Contains(strings.ToLower(errEvent.Content), "volume") {
		t.Errorf("error event should name the legacy-volume cause, got %q", errEvent.Content)
	}
	// A machine-readable code lets the UI/agent branch on the cause.
	md, ok := errEvent.Metadata.(map[string]any)
	if !ok {
		t.Fatalf("error event should carry structured Metadata, got %T", errEvent.Metadata)
	}
	if md["code"] != "legacy_volume_conflict" {
		t.Errorf("error code = %v, want legacy_volume_conflict", md["code"])
	}
	// The raw daemon text must NOT leak verbatim into the user stream.
	if strings.Contains(errEvent.Content, "disk full") {
		t.Errorf("raw daemon detail leaked into user stream: %q", errEvent.Content)
	}
}

// A cause we can't classify falls back to the internal code with a safe message
// (never the raw text, never a false specific cause).
func TestContainerStartError_UnknownCauseIsInternal(t *testing.T) {
	t.Parallel()
	resolver := &mockResolver{info: cacheMissingInfo("crewship-cache:present")}
	b := testBridgeWithContainer(t, resolver, &legacyVolumeContainer{raw: "some unmapped daemon failure xyzzy"})

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }
	_ = b.HandleChatMessage(context.Background(), "user-1", "sess-1", "hi", streamFn)

	var errEvent *ws.ChatEvent
	for i := range events {
		if events[i].Type == "error" {
			errEvent = &events[i]
		}
	}
	if errEvent == nil {
		t.Fatalf("expected a streamed error event; got %+v", events)
	}
	if strings.Contains(errEvent.Content, "xyzzy") {
		t.Errorf("raw text leaked for unknown cause: %q", errEvent.Content)
	}
	md, _ := errEvent.Metadata.(map[string]any)
	if md["code"] != "internal" {
		t.Errorf("unknown cause code = %v, want internal", md["code"])
	}
}
