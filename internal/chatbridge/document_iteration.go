package chatbridge

import (
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/ws"
)

// documentIterationForSend binds one explicit target to the resolved human
// receipt. It does not authorize the target: the API authorizer must check its
// live audience and original revision before accepting the run.
func documentIterationForSend(enabled bool, opt ws.ChatMessageOption, info *ChatInfo, userID, chatID, content string) (*documents.IterationIntent, error) {
	explicit, err := documents.IterationIntentFromMetadata(opt.Metadata)
	if err != nil {
		return nil, err
	}
	if explicit == nil && opt.DocumentIterationIntent == nil {
		return nil, nil
	}
	if !enabled {
		return nil, errors.New("Document iteration is unavailable")
	}
	creation, err := documents.CreationIntentFromMetadata(opt.Metadata)
	if err != nil {
		return nil, err
	}
	if creation != nil || opt.DocumentCreationIntent != nil {
		return nil, errors.New("one Document creation or iteration intent required")
	}
	intent := explicit
	if opt.HumanResume {
		// A queued send carries its original host-owned selection. Metadata can
		// agree with that selection, but cannot create or refresh it.
		intent = opt.DocumentIterationIntent
		if intent == nil || opt.OriginalTriggerMessageID == "" || intent.Validate() != nil || (explicit != nil && *explicit != *intent) {
			return nil, errors.New("original queued Document iteration intent required")
		}
	} else if opt.DocumentIterationIntent != nil {
		return nil, errors.New("explicit current human Document iteration intent required")
	}
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("Document iteration requires an instruction")
	}
	if info == nil || userID == "" || chatID == "" || info.AgentID == "" || info.WorkspaceID == "" || info.Visibility != "private" {
		return nil, errors.New("Document iteration requires one trusted private human message")
	}
	receipt := info.HumanAuthority
	if receipt == nil || receipt.UserID != userID || receipt.ChatID != chatID || receipt.AgentID != info.AgentID || receipt.WorkspaceID != info.WorkspaceID {
		return nil, errors.New("Document iteration requires one trusted private human message")
	}
	copy := *intent
	return &copy, nil
}

func documentIterationRunPrompt(content string, intent *documents.IterationIntent) string {
	if intent == nil {
		return content
	}
	return fmt.Sprintf("%s\n\nThe human explicitly selected only the existing Document Page %q (schema %d, revision %d, generation %d) for this instruction, submission %q. This selection authorizes drafting only that Document in this same live run, subject to current server authorization. Use doc_draft_view to read the authorized target, doc_draft_prepare for the requested supported changes, and doc_draft_apply with the exact returned packet to save them and their checkpoint. Keep the exact immutable prepared packet for retries after uncertain confirmation; never regenerate it to bypass an uncertain attempt. If an edit commits but checkpoint confirmation fails, retry only the original checkpoint packet with doc_draft_checkpoint and report the saved edit and checkpoint outcome accurately. Do not create a Document, upload Files, edit another Page, overwrite a conflicting human edit, renew permission, or assume a later run inherits it. If access or a revision precondition is refused, stop and report the refusal and confirmed saved state honestly. A prepared change is not Saved until the server confirms its commit.", content, intent.PageID, intent.Schema, intent.Rev, intent.Generation, intent.SubmissionID)
}
