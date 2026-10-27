package chatbridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

type ArtifactProjectIntentRecorder interface {
	RecordArtifactProjectIntent(context.Context, *chataudience.Receipt, string, artifacts.ProjectIntent) error
}

func (b *Bridge) SetArtifactProjectIntentRecorder(recorder ArtifactProjectIntentRecorder) {
	b.artifactProjectIntentRecorder = recorder
}

func artifactProjectForSend(enabled bool, opt ws.ChatMessageOption, info *ChatInfo, user, chat string, selected, document bool) (*artifacts.ProjectIntent, error) {
	explicit, err := artifacts.ProjectIntentFromMetadata(opt.Metadata)
	if err != nil {
		return nil, err
	}
	intent := explicit
	if opt.HumanResume {
		intent = opt.ArtifactProjectIntent
		if intent == nil && explicit != nil || intent != nil && (opt.OriginalTriggerMessageID == "" || intent.Validate() != nil || explicit != nil && *explicit != *intent) {
			return nil, errors.New("original artifact project purpose changed")
		}
	}
	if intent == nil {
		return nil, nil
	}
	receipt := info.HumanAuthority
	if !enabled || selected || document || info.Visibility != "private" || receipt == nil || receipt.UserID != user || receipt.ChatID != chat || receipt.AgentID != info.AgentID || receipt.WorkspaceID != info.WorkspaceID {
		return nil, errors.New("artifact projects require one explicit private human chat purpose")
	}
	copy := *intent
	return &copy, nil
}

func artifactProjectRunPrompt(content string, intent *artifacts.ProjectIntent) string {
	if intent.PageID == "" {
		return content + "\n\nThe human explicitly authorized one private multi-screen HTML artifact project in this live run. Use artifact_create with a complete atomic manifest (entrypoint plus files with path/content/media_type), include all three linked screens, and return the confirmed project URL/version. Do not use save_page, a human CLI profile, or create a replacement Document. A tool denial or uncertain outcome must be reported honestly."
	}
	return fmt.Sprintf("%s\n\nThe human explicitly authorized updating only artifact Page @%s (use this exact @ID as the tools' slug argument) from base version %d, comment %s. Use artifact_versions and artifact_comments, verify the exact path/selector/quote at the original version, then artifact_publish with the exact read base_version and complete manifest. If the anchor no longer matches or the version changed, ask for a new selection; never blindly rebase. Reply with artifact_reply describing the confirmed new version. Do not mutate another Page or silently change the shared version.", content, intent.PageID, intent.BaseVersion, intent.CommentID)
}
