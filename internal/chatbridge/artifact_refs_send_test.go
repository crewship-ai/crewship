package chatbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

type fakeArtifactRefRecorder struct {
	enabled         bool
	checks, records int
	storedAtCheck   int
	storedAtRecord  int
	countStored     func(context.Context) int
}

func (f *fakeArtifactRefRecorder) ArtifactRefsEnabled(context.Context, string) (bool, error) {
	return f.enabled, nil
}

func (f *fakeArtifactRefRecorder) CheckArtifactRefs(ctx context.Context, _ *chataudience.Receipt, _ []string) error {
	f.checks++
	f.storedAtCheck = f.countStored(ctx)
	return nil
}

func (f *fakeArtifactRefRecorder) RecordArtifactRefs(ctx context.Context, _ *chataudience.Receipt, _ string, _ []string) error {
	f.records++
	f.storedAtRecord = f.countStored(ctx)
	return nil
}

// #5317: the send path checks references before the human message is stored
// and records them after; with the workspace flag off it ignores the key as
// it did before the flag existed; and it refuses turns that would never bind
// them instead of dropping them.
func TestArtifactRefsSendPath(t *testing.T) {
	refs := []any{map[string]any{"page_id": "page-1"}}
	cases := []struct {
		name        string
		enabled     bool
		metadata    map[string]any
		wantRefused bool
		wantChecks  int
	}{
		{name: "flag off ignores the key", metadata: map[string]any{ArtifactRefsMetadataKey: refs}},
		{name: "flag off ignores even a malformed key", metadata: map[string]any{ArtifactRefsMetadataKey: "nonsense"}},
		{name: "flag on checks then records", enabled: true, metadata: map[string]any{ArtifactRefsMetadataKey: refs}, wantChecks: 1},
		{name: "flag on refuses a malformed key", enabled: true, metadata: map[string]any{ArtifactRefsMetadataKey: []any{map[string]any{"page_id": "a b"}}}, wantRefused: true},
		{name: "flag on refuses alongside page_context", enabled: true, metadata: map[string]any{ArtifactRefsMetadataKey: refs, "page_context": map[string]any{"slug": "x"}}, wantRefused: true},
		{name: "flag on refuses on a Native Craft instruction", enabled: true, metadata: map[string]any{ArtifactRefsMetadataKey: refs, NativeCraftInstructionMetadataKey: true}, wantRefused: true, wantChecks: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, r := documentCreationBridgeFixture(t)
			stored := func(ctx context.Context) int {
				var n int
				if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE session_id='creation-chat' AND role='user'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			rec := &fakeArtifactRefRecorder{enabled: tc.enabled, countStored: stored, storedAtCheck: -1, storedAtRecord: -1}
			b.SetArtifactRefRecorder(rec)
			before := stored(t.Context())
			err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Look at this", func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: tc.metadata})
			refused := errors.Is(err, ErrArtifactRefUnavailable)
			if refused != tc.wantRefused {
				t.Fatalf("refused=%v, want %v (err %v)", refused, tc.wantRefused, err)
			}
			if rec.checks != tc.wantChecks {
				t.Fatalf("checks=%d, want %d", rec.checks, tc.wantChecks)
			}
			if tc.wantRefused {
				if got := stored(t.Context()); got != before || rec.records != 0 {
					t.Fatalf("a refused message was stored (%d→%d) or recorded (%d)", before, got, rec.records)
				}
				return
			}
			if tc.wantChecks == 1 && (rec.storedAtCheck != before || rec.records != 1 || rec.storedAtRecord != before+1) {
				t.Fatalf("order: check saw %d stored, record saw %d (before %d, records %d)", rec.storedAtCheck, rec.storedAtRecord, before, rec.records)
			}
			if tc.wantChecks == 0 && rec.records != 0 {
				t.Fatalf("flag off recorded references: %d", rec.records)
			}
		})
	}
}
