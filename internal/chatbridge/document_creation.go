package chatbridge

import (
	"errors"
	"strings"
	"unicode"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/ws"
)

// literalDocumentCreation deliberately recognizes one direct, single-line
// command. Quoted, negated and ambiguous prose remains ordinary conversation.
func literalDocumentCreation(content string) bool {
	if strings.TrimSpace(content) != content || strings.ContainsAny(content, "\r\n\t") {
		return false
	}
	const prefix = "vytvoř dokument o "
	lower := strings.ToLower(content)
	if !strings.HasPrefix(lower, prefix) {
		return false
	}
	topic := strings.TrimPrefix(lower, prefix)
	if topic == "" || strings.TrimSpace(topic) != topic || len([]rune(topic)) > 200 {
		return false
	}
	hasLetter := false
	for _, r := range topic {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasLetter = true
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && r != '-' {
			return false
		}
	}
	if !hasLetter {
		return false
	}
	for _, word := range strings.Fields(topic) {
		switch word {
		case "ne", "ani", "ale", "nevytvoř", "nevytvářej", "nevytvářet", "nechci", "nechceme", "místo":
			return false
		}
	}
	return true
}

// documentCreationForSend accepts client metadata only on the current trusted
// human send. A provisioning resume consumes the original server-owned state;
// it never infers consent again from replayed content or metadata.
func documentCreationForSend(enabled bool, opt ws.ChatMessageOption, info *ChatInfo, userID, chatID, content string, selected bool, triggerID string) (*documents.CreationIntent, error) {
	explicit, err := documents.CreationIntentFromMetadata(opt.Metadata)
	if err != nil {
		return nil, err
	}
	if !enabled {
		if explicit != nil || opt.DocumentCreationIntent != nil {
			return nil, errors.New("Document creation is unavailable")
		}
		return nil, nil
	}
	var intent *documents.CreationIntent
	if opt.HumanResume {
		intent = opt.DocumentCreationIntent
		if intent == nil {
			if explicit != nil {
				return nil, errors.New("queued document creation consent required")
			}
			return nil, nil
		}
		if opt.OriginalTriggerMessageID == "" || intent.Validate() != nil || (explicit != nil && *explicit != *intent) {
			return nil, errors.New("queued document creation consent changed")
		}
	} else {
		intent = explicit
		if intent == nil && !selected && info.Visibility == "private" && literalDocumentCreation(content) {
			intent = &documents.CreationIntent{SubmissionID: triggerID}
		}
	}
	if intent == nil {
		return nil, nil
	}
	receipt := info.HumanAuthority
	if selected || info.Visibility != "private" || receipt == nil || receipt.UserID != userID || receipt.ChatID != chatID || receipt.AgentID != info.AgentID || receipt.WorkspaceID != info.WorkspaceID {
		return nil, errors.New("Document creation requires one trusted private human message without a selected Page")
	}
	copy := *intent
	return &copy, nil
}
