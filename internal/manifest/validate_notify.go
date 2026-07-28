package manifest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/notify"
)

// Validation for the notification-channel block and the Composio grants.
//
// Split from validate.go because it is the only part of the manifest that
// reaches into internal/notify for its vocabulary — the category list and the
// provider catalog are owned there, and duplicating either into a string
// literal here is how the two drift.

// notifyChannelTypes are the words the MANIFEST uses. "chat" is deliberately
// not "shoutrrr": that is the delivery library's name, an implementation
// detail nobody authoring a file should have to learn.
var notifyChannelTypes = map[string]bool{
	"email":   true,
	"webhook": true,
	"chat":    true,
}

// composioGrantModes mirrors the scopes the Composio bind endpoint accepts.
var composioGrantModes = map[string]bool{
	"full":   true,
	"read":   true,
	"custom": true,
}

// checkNotificationChannels validates the workspace's channel block.
//
// agentSlugs is every agent declared anywhere in the document, so a grant can
// be checked against the manifest itself rather than requiring a round trip.
func (v *validator) checkNotificationChannels(scope string, channels []NotificationChannel, agentSlugs map[string]bool) {
	seen := map[string]bool{}
	for i := range channels {
		ch := &channels[i]
		label := fmt.Sprintf("%s: notification_channel %q", scope, ch.Slug)

		v.checkSlug(scope+": notification_channel", ch.Slug)
		if ch.Slug != "" {
			if seen[ch.Slug] {
				v.errf("%s: duplicate slug %q — the slug is a channel's only identity in a manifest, so two rows sharing one make re-applies non-deterministic", scope, ch.Slug)
			}
			seen[ch.Slug] = true
		}

		if !notifyChannelTypes[ch.Type] {
			v.errf("%s: unknown type %q (allowed: chat, email, webhook)", label, ch.Type)
			continue
		}

		switch ch.Type {
		case "chat":
			if ch.Provider == "" {
				v.errf("%s: type chat needs a provider (see `crewship notifychannel providers`)", label)
			} else if _, ok := notify.ProviderByName(ch.Provider); !ok {
				v.errf("%s: unknown provider %q — this instance supports: %s",
					label, ch.Provider, strings.Join(notify.SupportedProviders(), ", "))
			}
			if len(ch.FieldsFromEnv) == 0 {
				// Not a nit: without a source there is nothing to build the
				// delivery URL from, and the alternative the author probably
				// reached for — writing the webhook inline — is exactly what
				// this schema refuses to allow.
				v.errf("%s: type chat needs fields_from_env naming the environment variable for each provider field (run `crewship notifychannel providers --provider %s` to see them). Secrets are never written into a manifest.",
					label, ch.Provider)
			}
			v.checkProviderFieldNames(label, ch)
		case "email":
			if strings.TrimSpace(ch.To) == "" {
				v.errf("%s: type email needs `to` (an address is not a secret, so it belongs in the manifest)", label)
			}
		case "webhook":
			if strings.TrimSpace(ch.URLFromEnv) == "" {
				v.errf("%s: type webhook needs url_from_env — an endpoint usually carries its own token, so it is read from the environment", label)
			}
		}

		if ch.MinPriority != "" {
			switch ch.MinPriority {
			case "low", "medium", "high", "urgent":
			default:
				v.errf("%s: min_priority %q invalid (allowed: low, medium, high, urgent)", label, ch.MinPriority)
			}
		}

		for _, cat := range ch.Categories {
			if !notifyKnownCategory(cat) {
				v.errf("%s: unknown category %q — allowing a category that cannot fire creates a channel that looks configured and never delivers. Known: %s",
					label, cat, strings.Join(notifyCategoryList(), ", "))
			}
		}

		for _, slug := range ch.Agents {
			if len(agentSlugs) > 0 && !agentSlugs[slug] {
				v.errf("%s: agents references %q, which this manifest does not declare", label, slug)
			}
		}
	}
}

// checkProviderFieldNames rejects a field the provider does not have, so a
// typo fails here rather than composing a delivery URL that is missing the
// value it needed.
func (v *validator) checkProviderFieldNames(label string, ch *NotificationChannel) {
	spec, ok := notify.ProviderByName(ch.Provider)
	if !ok {
		return
	}
	known := map[string]bool{}
	names := make([]string, 0, len(spec.Fields))
	for _, f := range spec.Fields {
		known[f.Key] = true
		names = append(names, f.Key)
	}
	// Deterministic order in the message — a set iterated raw would make the
	// same failure read differently on every run.
	sort.Strings(names)
	for field := range ch.FieldsFromEnv {
		if !known[field] {
			v.errf("%s: provider %q has no field %q (it takes: %s)",
				label, ch.Provider, field, strings.Join(names, ", "))
		}
	}
	for _, f := range spec.Fields {
		if f.Required && ch.FieldsFromEnv[f.Key] == "" {
			v.errf("%s: provider %q requires field %q, but fields_from_env does not name a variable for it",
				label, ch.Provider, f.Key)
		}
	}
}

// checkComposioGrants validates one agent's toolkit grants.
func (v *validator) checkComposioGrants(scope string, grants []ComposioToolkitGrant) {
	for _, g := range grants {
		if strings.TrimSpace(g.Toolkit) == "" {
			v.errf("%s: composio_toolkits entry is missing `toolkit`", scope)
			continue
		}
		// Empty means read — the least surprising thing to hand an agent
		// implicitly is the read-only subset, not every tool on the app.
		if g.Mode != "" && !composioGrantModes[g.Mode] {
			v.errf("%s: composio_toolkits[%s].mode %q invalid (allowed: full, read, custom)",
				scope, g.Toolkit, g.Mode)
		}
	}
}

// notifyKnownCategory reports whether cat is in the taxonomy. Legacy names
// are accepted so a manifest written against an older instance still applies.
func notifyKnownCategory(cat string) bool {
	for _, c := range notify.AllCategories {
		if c == cat {
			return true
		}
	}
	// LegacyCategories is keyed by the OLD name (migration v169 rewrites them
	// in place), so a manifest written against an older instance still
	// validates rather than failing on a name the server itself accepts.
	if _, ok := notify.LegacyCategories[cat]; ok {
		return true
	}
	return false
}

func notifyCategoryList() []string {
	out := append([]string(nil), notify.AllCategories...)
	sort.Strings(out)
	return out
}
