package chatbridge

import (
	"errors"
	"fmt"
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
			intent = &documents.CreationIntent{SubmissionID: triggerID, Drafting: true}
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

func documentCreationRunPrompt(content string, intent *documents.CreationIntent) string {
	prompt := fmt.Sprintf("%s\n\nThe human explicitly requested a new private document in this message. Create one complete saved Document with Crewship's `doc_create` MCP tool. If using the CLI, use `crewship doc create --file -`. Supply JSON like this: {\"slug\":\"document-topic\",\"name\":\"Document title\",\"mutation_id\":%q,\"blocks\":[{\"type\":\"heading\",\"level\":1,\"text\":\"Document title\"},{\"type\":\"paragraph\",\"text\":\"Complete content about the requested topic.\"}]}. Replace the example slug, title and text with useful content matching the request. Keep this mutation_id and the exact input for any retry after uncertain confirmation. The command generates block identities and hashes. Return the saved result link only after success. Do not upload document contents to crew Files.", content, intent.SubmissionID)
	if !intent.Drafting {
		return prompt + " This request does not authorize editing other Pages or later access to the created document."
	}
	return prompt + " This original request also authorizes drafting only the newly created Document in this same live run. Save a useful populated initial draft, then use doc_draft_view, doc_draft_prepare, and doc_draft_apply to save further paragraph or heading text and a checkpoint as you work. CLI equivalents are crewship doc draft view, crewship doc draft prepare --file -, and crewship doc draft apply --file -. Keep the exact immutable prepared packet for retries; never regenerate it for an uncertain attempt. A committed edit remains saved even if its checkpoint confirmation fails; retry the original checkpoint packet with doc_draft_checkpoint or crewship doc draft checkpoint --file -, and report that outcome accurately. Do not edit another Page, overwrite a human edit, renew this permission, or assume a new run inherits it. If access or a revision precondition is refused, stop drafting and report the saved document honestly."
}
