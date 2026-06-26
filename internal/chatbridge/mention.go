package chatbridge

import (
	"regexp"
	"strings"
	"sync"
)

// ShouldAgentRespond decides whether the agent should run for an incoming
// message. In a private (1:1) chat the agent always responds. In a group chat
// (multiple humans + agent) the agent stays silent unless it is @mentioned by
// slug — the universal turn-taking convention (Slack/Teams/Discord) that keeps
// the bot from dominating a shared conversation while humans talk among
// themselves.
//
// Visibility is matched case-insensitively; anything other than "group"
// (including empty/legacy) is treated as private → always respond.
func ShouldAgentRespond(visibility, content, agentSlug string) bool {
	if !strings.EqualFold(strings.TrimSpace(visibility), "group") {
		return true
	}
	return MentionsAgent(content, agentSlug)
}

var (
	mentionReMu    sync.Mutex
	mentionReCache = map[string]*regexp.Regexp{}
)

// MentionsAgent reports whether content @mentions agentSlug as a whole token
// (case-insensitive). The @ must sit on a word boundary so "email@riley.com"
// and "@rileybot" do not match "@riley".
func MentionsAgent(content, agentSlug string) bool {
	agentSlug = strings.TrimSpace(agentSlug)
	if agentSlug == "" {
		return false
	}
	mentionReMu.Lock()
	re, ok := mentionReCache[agentSlug]
	if !ok {
		re = regexp.MustCompile(`(?i)(^|[^a-z0-9_])@` + regexp.QuoteMeta(agentSlug) + `($|[^a-z0-9_])`)
		mentionReCache[agentSlug] = re
	}
	mentionReMu.Unlock()
	return re.MatchString(content)
}
