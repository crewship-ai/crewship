package chatbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

type messageRefRecorder struct {
	recorded map[string][]string
}

func (m *messageRefRecorder) ArtifactRefsEnabled(context.Context, string) (bool, error) {
	return true, nil
}

func (m *messageRefRecorder) CheckArtifactRefVersions(context.Context, *chataudience.Receipt, []string, map[string]int64) error {
	return nil
}

func (m *messageRefRecorder) RecordArtifactRefVersions(_ context.Context, _ *chataudience.Receipt, message string, pages []string, _ map[string]int64) error {
	m.recorded[message] = append(m.recorded[message], pages...)
	return nil
}

// A message with an attached Sheet sent while the crew is being provisioned keeps the Sheet: its
// reference is recorded for the message saved at deferral, and the queued resume carries that same
// message ID and the reference, so the run it starts binds the Sheet (artifact_run_scope copies the
// message's references).
func TestDeferredMessageKeepsItsAttachedSheet(t *testing.T) {
	resolver := &mockResolver{info: &ChatInfo{
		HumanAuthority:     &chataudience.Receipt{UserID: "user-1", ChatID: "sess-1", MemberID: "original", MemberRevision: 2},
		AgentID:            "a1",
		AgentSlug:          "alice",
		CrewID:             "crew-1",
		CrewSlug:           "engineering",
		WorkspaceID:        "ws-1",
		CLIAdapter:         "CLAUDE_CODE",
		ToolProfile:        "CODING",
		TimeoutSecs:        30,
		DevcontainerConfig: `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`,
	}}
	b, _ := testBridge(t, resolver)
	enq := &stubEnqueuer{resStarted: true}
	b.SetProvisioningEnqueuer(enq)
	rec := &messageRefRecorder{recorded: map[string][]string{}}
	b.SetArtifactRefRecorder(rec)
	metadata := map[string]any{ArtifactRefsMetadataKey: []any{map[string]any{"page_id": "sheet-1"}}}
	_ = b.HandleChatMessage(context.Background(), "user-1", "sess-1", "Clear the Loan ID column", func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: metadata})
	if !enq.attachCalled || !enq.attachMsg.Opts.UserMessagePersisted {
		t.Fatalf("the message was not queued behind the build as saved: %+v", enq.attachMsg)
	}
	id := enq.attachMsg.Opts.OriginalTriggerMessageID
	if id == "" || len(rec.recorded) != 1 || len(rec.recorded[id]) != 1 || rec.recorded[id][0] != "sheet-1" {
		t.Fatalf("the Sheet is not recorded for the queued message %q: %v", id, rec.recorded)
	}
	if _, ok := enq.attachMsg.Opts.Metadata[ArtifactRefsMetadataKey]; !ok {
		t.Fatalf("the queued message lost its reference: %v", enq.attachMsg.Opts.Metadata)
	}
}

type refusingFinder struct {
	messageRefRecorder
	checks int
}

func (r *refusingFinder) CheckArtifactRefVersions(context.Context, *chataudience.Receipt, []string, map[string]int64) error {
	r.checks++
	return ErrArtifactRefUnavailable
}

func (r *refusingFinder) AttachedWorkbookSheets(context.Context, *chataudience.Receipt, []string) ([]string, error) {
	return []string{"sheet-1"}, nil
}

// Binding by attachment is the server's help and never refuses the message: a proposal the
// admission refuses is dropped, and an explicit empty reference list sends the workbook without a
// Sheet; only a reference the person attached and that is unavailable refuses.
func TestAttachedWorkbookNeverRefusesTheMessage(t *testing.T) {
	const content = "I've attached a file:\n- attachments/ab12/Loan_plan.xlsx"
	for name, metadata := range map[string]map[string]any{
		"a refused match":        nil,
		"an explicit empty list": {ArtifactRefsMetadataKey: []any{}},
	} {
		t.Run(name, func(t *testing.T) {
			b, _ := documentCreationBridgeFixture(t)
			rec := &refusingFinder{messageRefRecorder: messageRefRecorder{recorded: map[string][]string{}}}
			b.SetArtifactRefRecorder(rec)
			err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", content, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: metadata})
			if errors.Is(err, ErrArtifactRefUnavailable) || len(rec.recorded) != 0 {
				t.Fatalf("the message was refused or bound: %v %v", err, rec.recorded)
			}
		})
	}
}

// matchingRecorder would name a Sheet for any attached workbook, as the removed binding by name did.
type matchingRecorder struct{ messageRefRecorder }

func (r *matchingRecorder) AttachedWorkbookSheets(context.Context, *chataudience.Receipt, []string) ([]string, error) {
	return []string{"sheet-1"}, nil
}

// A message that carries only an attached workbook, without artifact_refs, never binds a Sheet:
// a Sheet goes only by the person's explicit reference.
// The message still reaches the agent.
func TestAttachedWorkbookAloneBindsNoSheet(t *testing.T) {
	b, _ := documentCreationBridgeFixture(t)
	rec := &matchingRecorder{messageRefRecorder{recorded: map[string][]string{}}}
	b.SetArtifactRefRecorder(rec)
	err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "I've attached a file:\n- attachments/ab12/Plan.xlsx", func(ws.ChatEvent) {}, ws.ChatMessageOption{})
	if errors.Is(err, ErrArtifactRefUnavailable) || errors.Is(err, ErrArtifactConsentStale) || len(rec.recorded) != 0 {
		t.Fatalf("the attachment bound a Sheet or the message was refused: %v %v", err, rec.recorded)
	}
}
