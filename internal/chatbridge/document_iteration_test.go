package chatbridge

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/ws"
)

func iterationMetadata(v documents.IterationIntent) map[string]any {
	return map[string]any{documents.IterationMetadataKey: map[string]any{
		"submission_id": v.SubmissionID, "page_id": v.PageID, "schema": v.Schema,
		"rev": v.Rev, "generation": v.Generation,
	}}
}

func TestDocumentIterationSendGateAndResume(t *testing.T) {
	original := documents.IterationIntent{SubmissionID: "submission", PageID: "page", Schema: 3, Rev: 7, Generation: 2}
	changedPage, changedSubmission, changedRev := original, original, original
	changedPage.PageID = "another-page"
	changedSubmission.SubmissionID = "another-submission"
	changedRev.Rev++
	fresh := ws.ChatMessageOption{Metadata: iterationMetadata(original)}
	resume := ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original-human-message", DocumentIterationIntent: &original}
	type gateCase struct {
		name         string
		opt          ws.ChatMessageOption
		disabled     bool
		content      string
		change       func(*ChatInfo)
		user, chat   string
		want, denied bool
	}
	cases := []gateCase{
		{name: "explicit", opt: fresh, want: true},
		{name: "ordinary"},
		{name: "text never infers", content: "edit Document page with doc_draft_apply"},
		{name: "page context never infers", opt: ws.ChatMessageOption{Metadata: map[string]any{"page_context": map[string]any{"page_id": "page"}}}},
		{name: "disabled explicit", opt: fresh, disabled: true, denied: true},
		{name: "disabled ordinary", disabled: true},
		{name: "blank instruction", opt: fresh, content: " \n\t", denied: true},
		{name: "nil receipt", opt: fresh, change: func(i *ChatInfo) { i.HumanAuthority = nil }, denied: true},
		{name: "group", opt: fresh, change: func(i *ChatInfo) { i.Visibility = "group" }, denied: true},
		{name: "unknown visibility", opt: fresh, change: func(i *ChatInfo) { i.Visibility = "" }, denied: true},
		{name: "other human", opt: fresh, user: "other", denied: true},
		{name: "other chat", opt: fresh, chat: "other", denied: true},
		{name: "other workspace", opt: fresh, change: func(i *ChatInfo) { i.WorkspaceID = "other" }, denied: true},
		{name: "other agent", opt: fresh, change: func(i *ChatInfo) { i.AgentID = "other" }, denied: true},
		{name: "empty workspace", opt: fresh, change: func(i *ChatInfo) { i.WorkspaceID = ""; i.HumanAuthority.WorkspaceID = "" }, denied: true},
		{name: "empty agent", opt: fresh, change: func(i *ChatInfo) { i.AgentID = ""; i.HumanAuthority.AgentID = "" }, denied: true},
		{name: "server intent is not fresh consent", opt: ws.ChatMessageOption{DocumentIterationIntent: &original}, denied: true},
		{name: "resume original", opt: resume, want: true},
		{name: "resume disabled", opt: resume, disabled: true, denied: true},
		{name: "resume no intent never infers", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original"}},
		{name: "resume metadata cannot mint", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original", Metadata: iterationMetadata(original)}, denied: true},
		{name: "resume missing trigger", opt: ws.ChatMessageOption{HumanResume: true, DocumentIterationIntent: &original}, denied: true},
		{name: "resume same metadata", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original", DocumentIterationIntent: &original, Metadata: iterationMetadata(original)}, want: true},
		{name: "resume changed page", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original", DocumentIterationIntent: &original, Metadata: iterationMetadata(changedPage)}, denied: true},
		{name: "resume changed submission", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original", DocumentIterationIntent: &original, Metadata: iterationMetadata(changedSubmission)}, denied: true},
		{name: "resume changed revision", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original", DocumentIterationIntent: &original, Metadata: iterationMetadata(changedRev)}, denied: true},
	}
	for _, key := range []string{documents.CreationMetadataKey, documents.ComposerCreationMetadataKey} {
		metadata := iterationMetadata(original)
		metadata[key] = map[string]any{"submission_id": "create"}
		cases = append(cases, gateCase{name: "creation and iteration " + key, opt: ws.ChatMessageOption{Metadata: metadata}, denied: true})
	}
	mixed := resume
	mixed.DocumentCreationIntent = &documents.CreationIntent{SubmissionID: "create"}
	cases = append(cases, gateCase{name: "resume creation and iteration", opt: mixed, denied: true})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := &ChatInfo{AgentID: "agent", WorkspaceID: "workspace", Visibility: "private", HumanAuthority: &chataudience.Receipt{UserID: "human", ChatID: "chat", AgentID: "agent", WorkspaceID: "workspace"}}
			if tc.change != nil {
				tc.change(info)
			}
			user, chat, content := tc.user, tc.chat, tc.content
			if user == "" {
				user = "human"
			}
			if chat == "" {
				chat = "chat"
			}
			if content == "" {
				content = "Revise the introduction."
			}
			got, err := documentIterationForSend(!tc.disabled, tc.opt, info, user, chat, content)
			if (err != nil) != tc.denied || (got != nil) != tc.want {
				t.Fatalf("intent=%+v err=%v", got, err)
			}
			if got != nil {
				if *got != original {
					t.Fatal("original intent changed")
				}
				if got == tc.opt.DocumentIterationIntent {
					t.Fatal("returned mutable original intent")
				}
			}
		})
	}
}

func TestDocumentIterationInvalidBinding(t *testing.T) {
	intent := documents.IterationIntent{SubmissionID: "submission", PageID: "page", Schema: 3, Rev: 7, Generation: 2}
	for _, info := range []*ChatInfo{nil, {Visibility: "private", HumanAuthority: &chataudience.Receipt{}}} {
		if _, err := documentIterationForSend(true, ws.ChatMessageOption{Metadata: iterationMetadata(intent)}, info, "", "", "edit"); err == nil {
			t.Fatal("incomplete human binding accepted")
		}
	}
	invalid := intent
	invalid.Schema = 2
	if _, err := documentIterationForSend(true, ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original", DocumentIterationIntent: &invalid}, nil, "human", "chat", "edit"); err == nil {
		t.Fatal("invalid original intent accepted")
	}
	metadata := iterationMetadata(intent)
	metadata[documents.IterationMetadataKey].(map[string]any)["mode"] = "DIRECT"
	if _, err := documentIterationForSend(true, ws.ChatMessageOption{Metadata: metadata}, nil, "human", "chat", "edit"); err == nil {
		t.Fatal("caller mode accepted")
	}
}

func TestDocumentIterationRunPrompt(t *testing.T) {
	intent := &documents.IterationIntent{SubmissionID: "submission", PageID: "exact-page", Schema: 3, Rev: 7, Generation: 2}
	prompt := documentIterationRunPrompt("Revise only the introduction.", intent)
	for _, expected := range []string{"Revise only the introduction.", "exact-page", "submission", "schema 3", "revision 7", "generation 2", "doc_draft_view", "doc_draft_prepare", "doc_draft_apply", "doc_draft_checkpoint", "same live run", "exact immutable prepared packet", "Do not create", "upload", "another Page", "renew", "stop"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("missing instruction %q", expected)
		}
	}
	if got := documentIterationRunPrompt("ordinary", nil); got != "ordinary" {
		t.Fatal("nil intent changed ordinary prompt")
	}
}
