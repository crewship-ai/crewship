package chatbridge

import (
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
