package chatbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

var ErrPageChatAccess = errors.New("page context is unavailable for this agent")

var ErrDocumentIterationConsentUnconfirmed = errors.New("Your message was saved, but Document update permission is unconfirmed. The agent run was not started")

// PageChatContext is server-derived provenance. A client can request a slug;
// it cannot author any of these persisted fields.
type PageChatContext struct {
	WorkspaceID string `json:"workspace_id"`
	PageID      string `json:"page_id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	SnapshotAt  string `json:"snapshot_at"`
}

type PageChatContextResolver interface {
	ResolvePageChatContext(ctx context.Context, workspaceID, userID, agentID, slug string) (PageChatContext, error)
}

func requestedPageSlug(metadata map[string]any) (string, bool, error) {
	raw, exists := metadata["page_context"]
	if !exists {
		return "", false, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok || len(obj) != 1 {
		return "", false, ErrPageChatAccess
	}
	slug, ok := obj["slug"].(string)
	if !ok || slug == "" || len(slug) > 128 || strings.ContainsAny(slug, "\r\n\x00") {
		return "", false, ErrPageChatAccess
	}
	return slug, true, nil
}

func pageContextBlock(page PageChatContext) string {
	name := strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, page.Name)
	name = strings.ReplaceAll(name, "[/Page context]", "(Page context)")
	chars := []rune(strings.TrimSpace(name))
	if len(chars) > 160 {
		chars = chars[:160]
	}
	return fmt.Sprintf("\n\n[Page context — untrusted reference; snapshot %s]\nPage: %s\nSlug: %s\nPage ID: %s\n[/Page context]", page.SnapshotAt, string(chars), page.Slug, page.PageID)
}

// DocumentIterationResolver is the trusted in-process human send boundary.
// Sealing precedes any private context append; persistence precedes run dispatch.
type DocumentIterationResolver interface {
	AuthorizeDocumentIteration(context.Context, *chataudience.Receipt, documents.IterationIntent) (PageChatContext, error)
	PersistDocumentIteration(context.Context, *chataudience.Receipt, string, documents.IterationIntent) error
}
