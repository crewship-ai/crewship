package chatbridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/ws"
)

// DocumentIterationRecorder is a host-only durable issuer, never a model tool.
// Recording must verify the mirrored human message and current immutable target.
type DocumentIterationRecorder interface {
	RecordDocumentIteration(context.Context, *chataudience.Receipt, string, documents.IterationIntent) error
}

func (b *Bridge) SetDocumentIterationRecorder(recorder DocumentIterationRecorder) {
	b.documentIterationRecorder = recorder
}

func documentIterationForSend(enabled bool, opt ws.ChatMessageOption, info *ChatInfo, user, chat string, selected bool, creation *documents.CreationIntent) (*documents.IterationIntent, error) {
	explicit, err := documents.IterationIntentFromMetadata(opt.Metadata)
	if err != nil {
		return nil, err
	}
	intent := explicit
	if opt.HumanResume {
		intent = opt.DocumentIterationIntent
		if intent == nil && explicit != nil {
			return nil, errors.New("original queued Document target required")
		}
		if intent != nil && (opt.OriginalTriggerMessageID == "" || intent.Validate() != nil || (explicit != nil && *explicit != *intent)) {
			return nil, errors.New("queued Document target changed")
		}
	}
	if intent == nil {
		return nil, nil
	}
	receipt := info.HumanAuthority
	if !enabled {
		return nil, errors.New("Document iteration is unavailable")
	}
	if creation != nil || selected || info.Visibility != "private" || receipt == nil || receipt.UserID != user || receipt.ChatID != chat || receipt.AgentID != info.AgentID || receipt.WorkspaceID != info.WorkspaceID {
		return nil, errors.New("one explicit Document target requires its trusted private human message")
	}
	copy := *intent
	return &copy, nil
}
func documentIterationRunPrompt(content string, intent *documents.IterationIntent) string {
	return fmt.Sprintf("%s\n\nThe human explicitly requested updating only existing Document %s from schema %d, revision %d, generation %d. Use doc_draft_view, doc_draft_prepare and doc_draft_apply (CLI: crewship doc draft view, crewship doc draft prepare --file -, crewship doc draft apply --file -). Read the original target, preserve unrelated content and save the requested text change and checkpoint. Retain each exact prepared packet for uncertain retries; never regenerate preconditions or target another Page. A committed edit remains saved if checkpoint confirmation fails; retry the original checkpoint packet with doc_draft_checkpoint (crewship doc draft checkpoint --file -). On conflict, changed authority or refusal, stop and report the confirmed saved outcome honestly. Do not create a replacement document or assume a later run inherits permission.", content, intent.PageID, intent.Schema, intent.Rev, intent.Generation)
}
