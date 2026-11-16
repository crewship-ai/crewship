package notify

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/nicholas-fedor/shoutrrr/pkg/router"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/teams"
)

// ValidateServiceURL checks that raw parses as a real delivery target for its
// scheme, by handing it to the delivery library's own parser.
//
// This is what keeps the hand-written composers in providers_catalog.go
// honest. They take webhook URLs apart by hand — "the last two path segments
// are the id and the token" — which is correct until a provider changes its
// URL shape. Without this check a wrong composer produces a channel that
// saves cleanly, passes every shape check we could write ourselves, and fails
// only on someone's first real notification. With it, the failure lands at
// compose time, in a test.
//
// It does not perform any network call: the parse is local.
func ValidateServiceURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("empty service url")
	}
	var r router.ServiceRouter
	if _, err := r.Locate(normalizeTeamsServiceURL(raw)); err != nil {
		return err
	}
	return nil
}

// Older Crewship composers stripped https:// from the Teams host parameter.
// Restore it only for a signed workflow accepted by the current library, so
// stored channels keep delivering without rewriting encrypted secrets.
func normalizeTeamsServiceURL(raw string) string {
	if !strings.HasPrefix(raw, "teams://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host != "" || u.User != nil || u.Path != "" || u.Fragment != "" {
		return raw
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return raw
	}
	host := q.Get("host")
	if strings.Contains(host, "://") || strings.HasPrefix(host, "//") {
		return raw
	}
	webhook := "https://" + host
	if teams.ValidateWebhookURL(webhook) != nil {
		return raw
	}
	q.Set("host", webhook)
	return "teams://?" + q.Encode()
}
