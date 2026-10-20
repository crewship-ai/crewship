package chatbridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/ws"
)

type iterationWiringResolver struct {
	t                        *testing.T
	b                        *Bridge
	r                        *documentCreationResolver
	authorizeErr, persistErr error
	sealed, persisted        bool
}

func (s *iterationWiringResolver) ResolvePageChatContext(context.Context, string, string, string, string) (PageChatContext, error) {
	s.t.Fatal("iteration fell back to an ordinary Page context grant")
	return PageChatContext{}, ErrPageChatAccess
}

func (s *iterationWiringResolver) AuthorizeDocumentIteration(ctx context.Context, receipt *chataudience.Receipt, intent documents.IterationIntent) (PageChatContext, error) {
	s.t.Helper()
	if receipt == nil || receipt.UserID != "creation-human" || intent.PageID != "existing-page" {
		s.t.Fatal("preappend boundary lost original human/target")
	}
	rows, err := s.b.convStore.Read(ctx, receipt.ChatID, 0, 0)
	if err != nil || len(rows) != 0 {
		s.t.Fatalf("private context was appended before sealing: rows=%d err=%v", len(rows), err)
	}
	if s.authorizeErr != nil {
		return PageChatContext{}, s.authorizeErr
	}
	if _, err := s.r.db.ExecContext(ctx, `UPDATE chats SET private_artifact_author_user_id=? WHERE id=?`, receipt.UserID, receipt.ChatID); err != nil {
		s.t.Fatal(err)
	}
	s.sealed = true
	return PageChatContext{WorkspaceID: receipt.WorkspaceID, PageID: intent.PageID, Slug: "existing", Name: "Private existing title", SnapshotAt: "2026-10-20T00:00:00Z"}, nil
}

func (s *iterationWiringResolver) PersistDocumentIteration(ctx context.Context, receipt *chataudience.Receipt, message string, intent documents.IterationIntent) error {
	s.t.Helper()
	rows, err := s.b.convStore.Read(ctx, receipt.ChatID, 0, 0)
	if err != nil || len(rows) != 1 || rows[0].ID != message || rows[0].AuthorUserID != receipt.UserID || intent.PageID != "existing-page" || !s.sealed {
		s.t.Fatalf("postappend boundary lost sealed original turn: rows=%+v err=%v", rows, err)
	}
	if creationDBCount(s.t, s.r.db, `SELECT count(*) FROM conversation_messages WHERE session_id='creation-chat'`) != 1 {
		s.t.Fatal("postappend permission preceded the searchable human mirror")
	}
	s.persisted = s.persistErr == nil
	return s.persistErr
}

func iterationWiringFixture(t *testing.T) (*Bridge, *documentCreationResolver, *iterationWiringResolver, documents.IterationIntent) {
	t.Helper()
	b, r := documentCreationBridgeFixture(t)
	resolver := &iterationWiringResolver{t: t, b: b, r: r}
	b.SetPageChatContextResolver(resolver)
	return b, r, resolver, documents.IterationIntent{SubmissionID: "targeted-message", PageID: "existing-page", Schema: 3, Rev: 7, Generation: 2}
}

func TestDocumentIterationBridgeWiringOrderAndDenial(t *testing.T) {
	for _, outcome := range []string{"preappend denied", "postappend unconfirmed", "accepted"} {
		t.Run(outcome, func(t *testing.T) {
			b, r, resolver, intent := iterationWiringFixture(t)
			failure := errors.New("synthetic permission failure")
			if outcome == "preappend denied" {
				resolver.authorizeErr = failure
			} else if outcome == "postappend unconfirmed" {
				resolver.persistErr = failure
			}
			var events []ws.ChatEvent
			err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Revise the existing introduction.", func(e ws.ChatEvent) {
				if e.Type == "user_message" && !resolver.persisted {
					t.Fatal("persistence ACK preceded confirmed iteration permission")
				}
				events = append(events, e)
			}, ws.ChatMessageOption{Metadata: iterationMetadata(intent)})
			rows, readErr := b.convStore.Read(t.Context(), "creation-chat", 0, 0)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if outcome == "accepted" {
				if !errors.Is(err, errDocumentCreationComponentBoundary) || r.runCalls != 1 || !resolver.persisted || len(rows) != 1 {
					t.Fatalf("accepted path did not reach original run boundary: calls=%d err=%v", r.runCalls, err)
				}
				stored, ok := r.metadata[documents.IterationMetadataKey].(*documents.IterationIntent)
				if !ok || *stored != intent || r.trigger != rows[0].ID {
					t.Fatal("run metadata lost the original iteration/message identity")
				}
				return
			}
			if err == nil || r.runCalls != 0 {
				t.Fatalf("refused permission started model work: calls=%d err=%v", r.runCalls, err)
			}
			var errorText string
			for _, event := range events {
				if event.Type == "user_message" || strings.Contains(event.Content, "Private existing title") {
					t.Fatalf("refused permission emitted a private/ack frame: %+v", event)
				}
				if event.Type == "error" {
					errorText += strings.ToLower(event.Content)
				}
			}
			if outcome == "preappend denied" {
				if len(rows) != 0 || resolver.sealed || resolver.persisted {
					t.Fatal("preappend refusal persisted private content")
				}
			} else if len(rows) != 1 || !strings.Contains(errorText, "message") || !strings.Contains(errorText, "saved") || !strings.Contains(errorText, "permission") || strings.Contains(errorText, "failed to save message") {
				t.Fatalf("stored human turn was reported as unsaved: rows=%d errors=%q", len(rows), errorText)
			}
		})
	}
}

func TestDocumentIterationBridgeProvisioningKeepsRawOriginalInstruction(t *testing.T) {
	b, r, _, intent := iterationWiringFixture(t)
	r.info.DevcontainerConfig = `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`
	enqueuer := &stubEnqueuer{resStarted: true}
	b.SetProvisioningEnqueuer(enqueuer)
	content := "Revise the existing introduction."
	err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", content, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: iterationMetadata(intent)})
	if !errors.Is(err, ws.ErrCrewProvisioning) || !enqueuer.attachCalled {
		t.Fatalf("provisioning boundary not reached: %v", err)
	}
	pending := enqueuer.attachMsg
	if pending.Content != content {
		t.Errorf("deferred message already contains server Page context: %q", pending.Content)
	}
	if pending.Opts.OriginalTriggerMessageID == "" || pending.Opts.DocumentIterationIntent == nil || *pending.Opts.DocumentIterationIntent != intent || pending.Opts.HumanAuthority == nil || r.runCalls != 0 {
		t.Fatal("deferred send lost original human/message/iteration identity")
	}
	r.info.DevcontainerConfig = ""
	r.info.CLIAdapter = ""
	pending.Opts.HumanResume = true
	err = b.HandleChatMessage(t.Context(), pending.UserID, pending.ChatID, pending.Content, func(ws.ChatEvent) {}, pending.Opts)
	if !errors.Is(err, errDocumentCreationComponentBoundary) || r.runCalls != 1 || r.trigger != pending.Opts.OriginalTriggerMessageID {
		t.Fatalf("resume changed the original run trigger: calls=%d err=%v", r.runCalls, err)
	}
	rows, err := b.convStore.Read(t.Context(), "creation-chat", 0, 0)
	if err != nil || len(rows) != 1 || strings.Count(rows[0].Content, "[Page context") != 1 {
		t.Fatalf("resumed turn did not contain exactly one Page context: rows=%+v err=%v", rows, err)
	}
}
