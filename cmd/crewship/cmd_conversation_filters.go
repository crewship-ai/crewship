package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// conversationSearchFilters is lib/chat-search.ts ChatSearchFilters plus the
// "This chat" scope: everything the search endpoint cannot do, applied to the
// page it returns.
type conversationSearchFilters struct {
	chat   string
	from   string
	has    []string
	after  *time.Time
	before *time.Time
}

func (f conversationSearchFilters) active() bool {
	return f.chat != "" || f.from != "" || len(f.has) > 0 || f.after != nil || f.before != nil
}

var conversationLinkRE = regexp.MustCompile(`(?i)https?://\S+`)

func readConversationSearchFilters(cmd *cobra.Command) (conversationSearchFilters, error) {
	var f conversationSearchFilters
	f.chat, _ = cmd.Flags().GetString("chat")
	f.chat = strings.TrimSpace(f.chat)
	from, _ := cmd.Flags().GetString("from")
	f.from = strings.ToLower(strings.TrimSpace(from))
	if f.from == "me" || f.from == "user" || f.from == "you" {
		// Same refusal as the search box: a hit carries a role, not an
		// author, so "mine" cannot be told apart in a group chat.
		return f, fmt.Errorf("--from %s is not supported: search hits do not carry the message author; use --from agent or an agent slug", from)
	}
	has, _ := cmd.Flags().GetStringSlice("has")
	seen := map[string]bool{}
	for _, h := range has {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "link" && h != "code" && h != "tool" {
			return f, fmt.Errorf("--has %q: must be link, code or tool", h)
		}
		if !seen[h] {
			seen[h] = true
			f.has = append(f.has, h)
		}
	}
	for _, d := range []struct {
		flag string
		dst  **time.Time
	}{{"after", &f.after}, {"before", &f.before}} {
		v, _ := cmd.Flags().GetString(d.flag)
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			return f, fmt.Errorf("--%s %q: want a date as YYYY-MM-DD", d.flag, v)
		}
		*d.dst = &t
	}
	return f, nil
}

// match mirrors applyClientFilters: --from keeps only the agent's replies
// (all of them for "agent"/"assistant", one agent's by slug or name); --after
// is inclusive and --before exclusive, both at UTC midnight.
func (f conversationSearchFilters) match(sessionID, role, agentSlug, agentName, content, toolSummary, ts string) bool {
	if f.chat != "" && sessionID != f.chat {
		return false
	}
	if f.from != "" {
		r := strings.ToLower(role)
		if r != "assistant" && r != "agent" {
			return false
		}
		if f.from != "agent" && f.from != "assistant" &&
			strings.ToLower(agentSlug) != f.from && strings.ToLower(agentName) != f.from {
			return false
		}
	}
	for _, h := range f.has {
		switch h {
		case "link":
			if !conversationLinkRE.MatchString(content) {
				return false
			}
		case "code":
			if !strings.Contains(content, "```") {
				return false
			}
		case "tool":
			if toolSummary == "" {
				return false
			}
		}
	}
	if f.after != nil || f.before != nil {
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return false
		}
		if f.after != nil && t.Before(*f.after) {
			return false
		}
		if f.before != nil && !t.Before(*f.before) {
			return false
		}
	}
	return true
}
