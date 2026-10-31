package chatbridge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestArtifactProjectExplicitPrivateSendAndQueuedScope(t *testing.T) {
	info := &ChatInfo{WorkspaceID: "ws", AgentID: "agent", Visibility: "private", HumanAuthority: &chataudience.Receipt{WorkspaceID: "ws", AgentID: "agent", UserID: "human", ChatID: "chat"}}
	opt := ws.ChatMessageOption{Metadata: map[string]any{artifacts.ProjectIntentMetadataKey: map[string]any{"submission_id": "one"}}}
	got, err := artifactProjectForSend(true, opt, info, "human", "chat", false, false)
	if err != nil || got == nil || got.SubmissionID != "one" {
		t.Fatal("explicit private permission refused", got, err)
	}
	for _, change := range []string{"disabled", "group", "other-human", "selected", "document", "resume-forgery"} {
		t.Run(change, func(t *testing.T) {
			copyInfo := *info
			copyOpt := opt
			enabled, selected, document := true, false, false
			human := "human"
			switch change {
			case "disabled":
				enabled = false
			case "group":
				copyInfo.Visibility = "group"
			case "other-human":
				human = "other"
			case "selected":
				selected = true
			case "document":
				document = true
			case "resume-forgery":
				copyOpt.HumanResume = true
			}
			if _, err := artifactProjectForSend(enabled, copyOpt, &copyInfo, human, "chat", selected, document); err == nil {
				t.Fatal("invalid human purpose acquired authority")
			}
		})
	}
	opt.HumanResume = true
	opt.OriginalTriggerMessageID = "original-message"
	opt.ArtifactProjectIntent = got
	if _, err := artifactProjectForSend(true, opt, info, "human", "chat", false, false); err != nil {
		t.Fatal("exact queued retry refused", err)
	}
	opt.Metadata = map[string]any{artifacts.ProjectIntentMetadataKey: map[string]any{"submission_id": "other"}}
	if _, err := artifactProjectForSend(true, opt, info, "human", "chat", false, false); err == nil {
		t.Fatal("queued intent changed")
	}
}

type artifactRecorderFunc func(context.Context, *chataudience.Receipt, string, artifacts.ProjectIntent) error

func (f artifactRecorderFunc) RecordArtifactProjectIntent(ctx context.Context, r *chataudience.Receipt, m string, i artifacts.ProjectIntent) error {
	return f(ctx, r, m, i)
}

func TestArtifactProjectPersistedEchoConfirmsValidatedIntent(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint("denied=", denied), func(t *testing.T) {
			b, r := documentCreationBridgeFixture(t)
			b.cfg.ArtifactProjectsEnabled = true
			committed := false
			b.SetArtifactProjectIntentRecorder(artifactRecorderFunc(func(ctx context.Context, receipt *chataudience.Receipt, message string, intent artifacts.ProjectIntent) error {
				var count int
				if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_messages WHERE id=? AND author_user_id=?`, message, receipt.UserID).Scan(&count); err != nil || count != 1 {
					t.Fatal("message not durable before recorder", err)
				}
				if denied {
					return errors.New("artifact authority denied")
				}
				committed = true
				return nil
			}))
			echoed := 0
			err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Update selected heading", func(e ws.ChatEvent) {
				if e.Type != "user_message" {
					return
				}
				echoed++
				if !committed {
					t.Error("ACK before recorder accepted the durable purpose")
				}
				meta, _ := e.Metadata.(map[string]any)
				intent, ok := meta[artifacts.ProjectIntentMetadataKey].(artifacts.ProjectIntent)
				if !ok || intent.SubmissionID != "artifact-comment-c1" || intent.PageID != "page-1" || intent.CommentID != "c1" || intent.BaseVersion != 3 {
					t.Errorf("missing exact typed ACK: %#v", meta)
				}
				if _, forged := meta["forged"]; forged {
					t.Error("untrusted metadata echoed")
				}
			}, ws.ChatMessageOption{Metadata: map[string]any{artifacts.ProjectIntentMetadataKey: map[string]any{"submission_id": "artifact-comment-c1", "page_id": "page-1", "comment_id": "c1", "base_version": 3}, "forged": "must not echo"}})
			if denied {
				if err == nil || echoed != 0 || r.runCalls != 0 {
					t.Fatalf("denied send acknowledged or launched: echo=%d runs=%d err=%v", echoed, r.runCalls, err)
				}
			} else if !errors.Is(err, errDocumentCreationComponentBoundary) || echoed != 1 || r.runCalls != 1 {
				t.Fatalf("durable send missing ACK/admission: echo=%d runs=%d err=%v", echoed, r.runCalls, err)
			}
		})
	}
}

func TestArtifactProjectCrewPolicyRefusalIsTyped(t *testing.T) {
	b, r := documentCreationBridgeFixture(t)
	b.cfg.ArtifactProjectsEnabled = true
	b.SetArtifactProjectIntentRecorder(artifactRecorderFunc(func(context.Context, *chataudience.Receipt, string, artifacts.ProjectIntent) error {
		return fmt.Errorf("artifact project access denied: %w", artifacts.ErrCrewNotAllowed)
	}))
	var refusal *ws.ChatEvent
	err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Make a landing page", func(e ws.ChatEvent) {
		if e.Type == "error" {
			refusal = &e
		}
	}, ws.ChatMessageOption{Metadata: map[string]any{artifacts.ProjectIntentMetadataKey: map[string]any{"submission_id": "s1"}}})
	if err == nil || r.runCalls != 0 {
		t.Fatalf("refused send was not an error or started a run: runs=%d err=%v", r.runCalls, err)
	}
	if refusal == nil || refusal.Content == "failed to save message" {
		t.Fatalf("generic or missing refusal: %+v", refusal)
	}
	meta, _ := refusal.Metadata.(map[string]any)
	if meta["code"] != artifacts.CodeCrewNotAllowed || refusal.Content != artifacts.CrewNotAllowedMessage {
		t.Fatalf("untyped refusal: %+v", refusal)
	}
}
