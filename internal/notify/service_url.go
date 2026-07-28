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

// paramsIgnoredBy names service schemes whose Send discards the params map.
//
// googlechat's signature is literally `Send(message string, _ *types.Params)`.
// Every other service in our catalog reads them. Keeping the exception here,
// rather than assuming uniformity, is the difference between a title rendering
// natively and a title disappearing.
var paramsIgnoredBy = map[string]bool{"googlechat": true}

// shoutrrrMessage splits a notification into the text a service receives and
// the params it renders natively.
//
// Every chat and push service shoutrrr exposes has a title field, and we used
// none of them: the title was glued onto the front of the message and sent as
// one blob. On a chat service that is cosmetic — a bold header instead of a
// plain first line. On a PUSH service it is not: Pushover, ntfy and Gotify put
// the title on the lock screen, so a phone notification was showing the first
// line of the body where the title belonged.
//
// Both delivery paths call this. Two producers building the same message two
// ways is how one of them gets fixed and the other does not.
func shoutrrrMessage(serviceURL, title, body string) (string, map[string]string) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	params := map[string]string{}

	// A service that throws params away must keep the title in the text, or
	// it is not sent at all.
	if title != "" && paramsIgnoredBy[schemeOf(serviceURL)] {
		if body == "" {
			return title, params
		}
		return title + "\n\n" + body, params
	}
	if title != "" {
		params["title"] = title
	}
	// A title-only notification is ordinary — a journal entry with no facts
	// beyond its summary. Sending an empty message is a delivery a service
	// can refuse outright, so the title stands in for the body.
	if body == "" {
		return title, params
	}
	return body, params
}

// schemeOf returns the part before "://", which is how shoutrrr picks a
// service.
func schemeOf(rawURL string) string {
	if i := strings.Index(rawURL, "://"); i > 0 {
		return rawURL[:i]
	}
	return ""
}
