package chatbridge

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/askforms"
)

// validateAskSubmission binds client answers to this chat's trusted definition
// before any side effects. Only an absent key denotes an ordinary message;
// malformed explicit submissions must never degrade into unvalidated text.
func validateAskSubmission(info *ChatInfo, chatID, content string, metadata map[string]any) (*askforms.Envelope, error) {
	raw, present := metadata[askforms.EnvelopeMetadataKey]
	if !present {
		return nil, nil
	}
	var env askforms.Envelope
	refuse := func(message string) (*askforms.Envelope, error) {
		return nil, &askforms.SubmissionError{SubmissionID: env.SubmissionID, Message: message}
	}
	data, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(data, &env) != nil || env.Validate() != nil {
		return refuse("Invalid form submission — reopen the form and try again.")
	}
	// Stored definitions are decoded without Parse's write-time validation:
	// ValidateAnswers intentionally ignores patterns Go cannot compile in legacy
	// rows. New writes still go through Parse/Validate and cannot introduce them.
	var forms []askforms.Form
	if json.Unmarshal([]byte(info.AskForms), &forms) != nil {
		return refuse("Form is unavailable — reopen the form and try again.")
	}
	var form *askforms.Form
	for i := range forms {
		if forms[i].ID == env.FormID {
			if form != nil {
				return refuse("Form definition is ambiguous — reopen the form.")
			}
			form = &forms[i]
		}
	}
	if form == nil {
		return refuse("Form is unavailable — reopen the form and try again.")
	}
	version := form.Version
	if version < 1 {
		version = 1
	}
	if env.FormVersion != version {
		return refuse("Form has changed — reopen the form and try again.")
	}
	values := askforms.Values{}
	attachments := map[string][]string{}
	for _, field := range form.Fields {
		if askforms.IsAttachmentType(field.Type) {
			ids := env.FieldAttachmentIDs[field.Name]
			for _, id := range ids {
				if !askAttachmentInChat(chatID, id) {
					return refuse("Form attachment does not belong to this chat.")
				}
			}
			if len(ids) > 0 {
				values[field.Name] = ids
				attachments[field.Name] = ids
			}
		} else if value, ok := env.Values[field.Name]; ok {
			values[field.Name] = value
		}
		if field.Type == "money" {
			key := askforms.CurrencyPlaceholder(field.Name)
			if value, ok := env.Values[key]; ok {
				values[key] = value
			}
		}
	}
	if errors := askforms.ValidateAnswers(*form, values); len(errors) > 0 {
		return nil, &askforms.SubmissionError{SubmissionID: env.SubmissionID, Errors: errors}
	}
	if form.Attachment == "required" && len(attachments) == 0 && len(env.MessageAttachmentIDs) == 0 {
		return refuse(fmt.Sprintf("“%s” needs a document — attach a file or take a photo before sending.", form.Label))
	}
	rendered := askforms.Render(*form, values, chatID)
	if env.RenderedText != rendered {
		return refuse("Form message does not match its answers — reopen the form.")
	}
	expected := rendered
	if len(env.MessageAttachmentIDs) > 0 {
		for _, id := range env.MessageAttachmentIDs {
			if !askAttachmentInChat(chatID, id) {
				return refuse("Form attachment does not belong to this chat.")
			}
		}
		lead := "I've attached a file to this message. The path is relative to your working directory:"
		if len(env.MessageAttachmentIDs) > 1 {
			lead = fmt.Sprintf("I've attached %d files to this message. The paths are relative to your working directory:", len(env.MessageAttachmentIDs))
		}
		block := lead + "\n\n- " + strings.Join(env.MessageAttachmentIDs, "\n- ")
		if strings.TrimSpace(expected) == "" {
			expected = block
		} else {
			expected = strings.TrimSpace(expected) + "\n\n" + block
		}
	}
	if content != expected {
		return refuse("Form message does not match its answers — reopen the form.")
	}
	// Rebuild, rather than persist arbitrary fields or a forged label from the
	// untrusted client. The submitted values have just passed the shared validator.
	canonical := askforms.NewEnvelope(*form, env.SubmissionID, values, attachments, rendered)
	canonical.MessageAttachmentIDs = append([]string(nil), env.MessageAttachmentIDs...)
	return &canonical, nil
}

func askAttachmentInChat(chatID, id string) bool {
	prefix := "attachments/" + chatID + "/"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	name := strings.TrimPrefix(id, prefix)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range id {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
