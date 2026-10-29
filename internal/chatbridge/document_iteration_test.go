package chatbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/ws"
)

type iterationRecorderFunc func(context.Context, *chataudience.Receipt, string, documents.IterationIntent) error

func (f iterationRecorderFunc) RecordDocumentIteration(ctx context.Context, r *chataudience.Receipt, m string, i documents.IterationIntent) error {
	return f(ctx, r, m, i)
}
func iterationMetadata() map[string]any {
	return map[string]any{"document_iteration_intent": map[string]any{"submission_id": "followup", "page_id": "same-page", "schema": float64(3), "rev": float64(7), "generation": float64(2)}}
}

func TestDocumentIterationTrustedBridgeRecordsBeforeRun(t *testing.T) {
	b, r := documentCreationBridgeFixture(t)
	var recorded string
	b.SetDocumentIterationRecorder(iterationRecorderFunc(func(ctx context.Context, receipt *chataudience.Receipt, message string, intent documents.IterationIntent) error {
		var content, author string
		if err := r.db.QueryRowContext(ctx, `SELECT content,author_user_id FROM conversation_messages WHERE id=? AND session_id=?`, message, receipt.ChatID).Scan(&content, &author); err != nil {
			t.Fatal(err)
		}
		if content != "Change the introduction" || author != "creation-human" || intent.PageID != "same-page" || r.runCalls != 0 {
			t.Fatal("original message not mirrored before purpose/admission")
		}
		recorded = message
		return nil
	}))
	var echoed string
	err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Change the introduction", func(e ws.ChatEvent) {
		if e.Type == "user_message" {
			meta, _ := e.Metadata.(map[string]any)
			intent, ok := meta["document_iteration_intent"].(documents.IterationIntent)
			if !ok || intent.SubmissionID != "followup" {
				t.Error("saved echo lacks exact target purpose")
			}
			echoed, _ = meta["message_id"].(string)
		}
	}, ws.ChatMessageOption{Metadata: iterationMetadata()})
	if !errors.Is(err, errDocumentCreationComponentBoundary) || r.runCalls != 1 || recorded == "" || r.trigger != recorded || echoed != recorded {
		t.Fatalf("retained send did not reach admission: recorded=%s echo=%s calls=%d err=%v", recorded, echoed, r.runCalls, err)
	}
	got, ok := r.metadata["document_iteration_intent"].(*documents.IterationIntent)
	if !ok || got == nil || got.PageID != "same-page" || got.Rev != 7 {
		t.Fatal("typed original purpose not promoted")
	}
	if creationDBCount(t, r.db, `SELECT count(*) FROM document_creation_consents`) != 0 {
		t.Fatal("iteration created creation authority")
	}
}

func TestDocumentIterationRecorderFailureNeverAdmitsRun(t *testing.T) {
	for _, scenario := range []string{"unwired", "recorder fault", "disabled", "combined creation", "combined page", "mirror failure"} {
		t.Run(scenario, func(t *testing.T) {
			b, r := documentCreationBridgeFixture(t)
			meta := iterationMetadata()
			if scenario != "unwired" {
				b.SetDocumentIterationRecorder(iterationRecorderFunc(func(ctx context.Context, receipt *chataudience.Receipt, message string, _ documents.IterationIntent) error {
					if scenario == "recorder fault" {
						return errors.New("fixture retained write failure")
					}
					var n int
					if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE id=?`, message).Scan(&n); err != nil || n != 1 {
						return errors.New("missing durable mirror")
					}
					return nil
				}))
			}
			if scenario == "disabled" {
				b.cfg.DocumentCreationEnabled = false
			}
			if scenario == "combined creation" {
				meta["document_creation_intent"] = map[string]any{"submission_id": "new"}
			}
			if scenario == "combined page" {
				meta["page_context"] = map[string]any{"slug": "existing-page"}
			}
			if scenario == "mirror failure" {
				if _, err := r.db.Exec(`CREATE TRIGGER deny_iteration_message BEFORE INSERT ON conversation_messages BEGIN SELECT RAISE(ABORT,'fixture mirror failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Change introduction", func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: meta})
			if err == nil || r.runCalls != 0 {
				t.Fatalf("failed retained intent admitted run: %d %v", r.runCalls, err)
			}
		})
	}
}

func TestDocumentIterationProvisioningPreservesOriginalTuple(t *testing.T) {
	b, r := documentCreationBridgeFixture(t)
	r.info.DevcontainerConfig = `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`
	enq := &stubEnqueuer{resStarted: true}
	b.SetProvisioningEnqueuer(enq)
	calls := 0
	b.SetDocumentIterationRecorder(iterationRecorderFunc(func(_ context.Context, _ *chataudience.Receipt, _ string, i documents.IterationIntent) error {
		calls++
		if i.PageID != "same-page" || i.Rev != 7 {
			t.Error("original target changed")
		}
		return nil
	}))
	err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Edit existing document", func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: iterationMetadata()})
	if !errors.Is(err, ws.ErrCrewProvisioning) || calls != 0 {
		t.Fatalf("premature grant: %d %v", calls, err)
	}
	original := enq.attachMsg.Opts
	if original.DocumentIterationIntent == nil || original.HumanAuthority == nil || original.OriginalTriggerMessageID == "" {
		t.Fatal("queued purpose lost")
	}
	r.info.DevcontainerConfig = ""
	original.HumanResume = true
	altered := original
	altered.Metadata = iterationMetadata()
	altered.Metadata["document_iteration_intent"].(map[string]any)["page_id"] = "other-page"
	if err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Edit existing document", func(ws.ChatEvent) {}, altered); err == nil || calls != 0 {
		t.Fatal("queued metadata changed original target")
	}
	// The queue owns the validated tuple; do not mutate the shallow copied metadata.
	original.Metadata = iterationMetadata()
	err = b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Edit existing document", func(ws.ChatEvent) {}, original)
	if !errors.Is(err, errDocumentCreationComponentBoundary) || calls != 1 || r.trigger != original.OriginalTriggerMessageID {
		t.Fatalf("resume changed trigger/purpose: %d %v", calls, err)
	}
}
