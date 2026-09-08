package groupchat

import "net/url"

// URL is the canonical human-conversation entry in the unified Chat surface.
// Agent-session URLs retain their separate /chat/<slug>?session=... contract.
func URL(conversationID string) string {
	return "/chat?conversation=" + url.QueryEscape(conversationID)
}
