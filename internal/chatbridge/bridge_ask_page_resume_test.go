package chatbridge

import (
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestDeferredAskPageRevalidatesOriginalSubmission(t *testing.T) {
	for _, mode := range []string{"valid", "form changed", "page revoked"} {
		t.Run(mode, func(t *testing.T) {
			form := askValidationForm(`approved`)
			info := baseInfo()
			info.Visibility = "private"
			info.AskForms = *askValidationDefinitions(t, form)
			info.HumanAuthority = &chataudience.Receipt{UserID: askValidationHuman, ChatID: askValidationChat, MemberID: "member", MemberRevision: 1}
			info.DevcontainerConfig = `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`
			info.CachedImage = ""
			b := testBridgeWithContainer(t, &mockResolver{info: info}, &failContainer{})
			enq := &stubEnqueuer{resStarted: true}
			b.SetProvisioningEnqueuer(enq)
			page := &pageResolverStub{page: PageChatContext{PageID: "page-before", Slug: "fleet", Name: "Before"}}
			b.SetPageChatContextResolver(page)
			content, metadata := askValidationMetadata(t, form, askforms.Values{"answer": "approved"}, nil)
			metadata["page_context"] = map[string]any{"slug": "fleet"}
			if err := b.HandleChatMessage(t.Context(), askValidationHuman, askValidationChat, content, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: metadata}); !errors.Is(err, ws.ErrCrewProvisioning) || !enq.attachCalled {
				t.Fatalf("message not deferred: %v, attached=%v", err, enq.attachCalled)
			}
			info.CachedImage = "crewship-cache:ready"
			info.CachedRequirements = &devcontainer.AggregatedRequirements{AdapterBinaries: []string{"claude"}}
			page.page.PageID = "page-current"
			page.page.Name = "Current"
			if mode == "form changed" {
				form.Version++
				info.AskForms = *askValidationDefinitions(t, form)
			}
			if mode == "page revoked" {
				page.err = ErrPageChatAccess
			}
			pending := enq.attachMsg
			pending.Opts.HumanResume = true
			var events []ws.ChatEvent
			err := b.HandleChatMessage(t.Context(), pending.UserID, pending.ChatID, pending.Content, func(e ws.ChatEvent) { events = append(events, e) }, pending.Opts)
			messages, readErr := b.convStore.Read(t.Context(), askValidationChat, 0, 0)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode != "valid" {
				if mode == "form changed" {
					var refused *askforms.SubmissionError
					if !errors.As(err, &refused) {
						t.Fatalf("changed form not revalidated: %v", err)
					}
				}
				if mode == "page revoked" && !errors.Is(err, ErrPageChatAccess) {
					t.Fatalf("Page access not rechecked: %v", err)
				}
				if len(messages) != 0 {
					t.Fatalf("rejected resume persisted %d turns", len(messages))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "container unavailable") {
				t.Fatalf("valid deferred Ask+Page did not reach runtime after acceptance: %v", err)
			}
			if len(messages) != 1 || strings.Count(messages[0].Content, "[/Page context]") != 1 || !strings.Contains(messages[0].Content, "Page ID: page-current") || strings.Contains(messages[0].Content, "page-before") {
				t.Fatalf("resume did not rebuild one current Page block: %+v", messages)
			}
			if page.calls != 2 {
				t.Fatalf("Page resolver calls=%d, want2", page.calls)
			}
			receipts := 0
			for _, event := range events {
				if event.Type == "user_message" {
					meta, _ := event.Metadata.(map[string]any)
					if meta["ask_submission_id"] != "ask-submission" {
						t.Fatalf("uncorrelated receipt: %#v", meta)
					}
					receipts++
				}
			}
			if receipts != 1 {
				t.Fatalf("acceptance receipts=%d, want1", receipts)
			}
		})
	}
}
