package notifyroute

import (
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/notify"
)

// journalKindPrefix marks an inbox kind synthesised by the journal bridge
// rather than read from inbox_items. Must match journalItem's construction.
const journalKindPrefix = "journal:"

// notificationFacts derives everything a notification carries ABOUT its
// source: where a reader can go (links) and what a message template can
// reference (vars).
//
// Both the live router and the recovery sweep used to do this inline, and
// both did the same one thing — read "chat_url" off the payload. So a
// notification was clickable if and only if it came from a chat reply, while
// every other producer's payload already held the ids a link needs and threw
// them away.
//
// Links are APP-RELATIVE. Delivery makes them absolute (notify.AbsoluteLink),
// because only delivery knows where this instance is reachable from.
//
// Links point only where a page actually resolves. There is no per-run and no
// per-journal-entry route in the app today, so those get the list page rather
// than an invented "?run=" filter the page would ignore: a link that looks
// precise and lands somewhere generic spends the reader's trust once per
// notification, and coarse-but-honest costs nothing.
func notificationFacts(kind string, payload map[string]any) ([]notify.Link, map[string]any) {
	// Vars is the payload itself, not a per-kind allowlist. A producer that
	// starts recording a new fact gets a new template variable for free;
	// an allowlist would silently lag every producer that ever changes.
	vars := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		vars[k] = v
	}

	str := func(key string) string {
		s, _ := payload[key].(string)
		return strings.TrimSpace(s)
	}

	// Journal-sourced kinds are "journal:<entry type>"; the entry type is
	// already in vars as entry_type, so match on the prefix alone.
	if strings.HasPrefix(kind, journalKindPrefix) {
		if mission := str("mission_id"); mission != "" {
			// A journal entry has no route of its own, but one belonging
			// to a mission can point at that mission's timeline — where
			// the entry is actually visible in context.
			return []notify.Link{{Label: "Open mission", Path: "/missions/" + url.PathEscape(mission) + "/timeline"}}, vars
		}
		return []notify.Link{{Label: "Open journal", Path: "/journal"}}, vars
	}

	switch kind {
	case inbox.KindMessage:
		// "message" covers two unrelated producers. A chat reply carries a
		// chat_url — the one link that already worked, read from the same
		// key so chat replies are unchanged by this refactor.
		if chat := str("chat_url"); chat != "" {
			return []notify.Link{{Label: "Open chat", Path: chat}}, vars
		}
		// A routine's notify step also writes "message", and carries no
		// chat_url. It marks itself with subkind=routine_update. Without
		// this it would be the only producer people actually author by
		// hand, and the only one with nowhere to click.
		if str("subkind") == "routine_update" {
			return []notify.Link{{Label: "Open runs", Path: "/runs"}}, vars
		}
		return nil, vars

	case inbox.KindEscalation:
		// agent_id is the only filter /approvals supports.
		if agent := str("agent_id"); agent != "" {
			return []notify.Link{{Label: "Review", Path: "/approvals?agent_id=" + url.QueryEscape(agent)}}, vars
		}
		return []notify.Link{{Label: "Review", Path: "/approvals"}}, vars

	case inbox.KindWaitpoint:
		// Waitpoint payloads carry a pipeline run and step, no agent, so
		// there is nothing to narrow the approvals page with.
		return []notify.Link{{Label: "Review", Path: "/approvals"}}, vars

	case inbox.KindFailedRun:
		return []notify.Link{{Label: "Open runs", Path: "/runs"}}, vars

	case inbox.KindScheduleMissed, inbox.KindScheduleCircuitBreakerTripped:
		return []notify.Link{{Label: "Open routines", Path: "/routines"}}, vars

	case inbox.KindMemoryConsolidation:
		return []notify.Link{{Label: "Open inbox", Path: "/inbox"}}, vars
	}

	// An inbox kind this table has not learned yet. No link is the right
	// answer — new kinds reach here before anyone updates this file, and a
	// guessed route would be a broken link shipped by default.
	return nil, vars
}
