package notify

import (
	"net/url"
	"strings"
)

// ServiceURLForDelivery applies provider options that belong to HOW a message
// is presented rather than to WHERE it goes.
//
// It runs on the way out, not when a channel is created, because a channel's
// service URL is composed once and stored encrypted. Fixing the compose
// function alone would improve channels made from then on and leave every
// existing one broken, with no repair short of deleting and re-adding it —
// and the composed URL is deliberately never readable, so nobody could even
// see what was wrong.
//
// It only ever supplies a DEFAULT. An option the operator set explicitly is
// left as they set it.
func ServiceURLForDelivery(raw string) string {
	// splitLines is a Discord option. Adding it to a service that does not
	// know the key makes shoutrrr reject the whole URL, turning a cosmetic
	// improvement into a delivery outage — so this is matched on scheme, not
	// applied hopefully.
	if !strings.HasPrefix(raw, "discord://") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		// Delivering a fragmented message beats mangling the URL and
		// delivering nothing.
		return raw
	}
	q := u.Query()
	if q.Has("splitLines") {
		return raw
	}
	// shoutrrr's Discord service defaults splitLines=Yes, which renders each
	// line of a message as its own embedded item: a title, a body and a link
	// arrive as three disconnected boxes, and a body with a JSON payload in
	// it looks like a malfunction. One message is what a person expects; the
	// service still splits on Discord's own length limit.
	q.Set("splitLines", "No")
	u.RawQuery = q.Encode()
	return u.String()
}
