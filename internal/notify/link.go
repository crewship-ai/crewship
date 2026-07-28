package notify

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Link is a deep link carried on a notification: where a person goes to act
// on what the message is telling them.
//
// Path is APP-RELATIVE ("/issues/CS-12"), never absolute, because a producer
// knows what a notification is about but not where this instance is reachable
// from — that depends on a reverse proxy, a hostname and a scheme none of the
// producing packages have any business knowing. Delivery makes it absolute,
// once, in AbsoluteLink.
//
// Before this, a notification carried one opaque URL string, set by exactly
// one producer (the chat bridge) from a relative path and delivered verbatim.
// A relative path is not a link in Discord: there is no origin to resolve it
// against. So the single clickable thing a notification could carry only ever
// worked inside the app's own inbox.
type Link struct {
	// Label is what the link says. It is author-influenced text and is
	// scrubbed with the rest of the envelope.
	Label string `json:"label,omitempty"`
	// Path is app-relative, with or without a leading slash. An absolute
	// URL is tolerated for links that genuinely point elsewhere (a GitHub
	// PR, a Composio account) and is passed through untouched.
	Path string `json:"path"`
}

// AbsoluteLink resolves a Link path against the instance's public URL.
//
// Three cases the callers depend on:
//
//   - base == "": nothing is configured, so there is no honest absolute form.
//     The path is returned unchanged, which keeps the in-app inbox working
//     and declines to guess a hostname. A guessed host is worse than a
//     relative one — it produces a link that looks right and 404s.
//   - path is already absolute: returned untouched, so a producer can carry
//     a link that points outside this app.
//   - otherwise: joined with exactly one slash.
func AbsoluteLink(base, path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if base == "" {
		return path
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// PrimaryLink returns the first link, which is the one single-URL formats
// use — the webhook payload's `url`, an e-mail footer, a push notification
// that has room for one tap target.
//
// Link order is therefore meaningful: producers put the primary action first.
func (m CategoryMessage) PrimaryLink() (Link, bool) {
	if len(m.Links) == 0 {
		return Link{}, false
	}
	return m.Links[0], true
}

// resolveLinks returns the message's links with every path made absolute
// against base. Delivery formats call this instead of reading msg.Links
// directly, so no format can forget the step.
func (m CategoryMessage) resolveLinks(base string) []Link {
	if len(m.Links) == 0 {
		return nil
	}
	out := make([]Link, 0, len(m.Links))
	for _, l := range m.Links {
		if url := AbsoluteLink(base, l.Path); url != "" {
			out = append(out, Link{Label: l.Label, Path: url})
		}
	}
	return out
}

// linkLines renders links as one "Label: url" line each, for the plain-text
// formats (shoutrrr chat services, e-mail text). Chat clients auto-link a
// bare URL, so no markup is needed and none is emitted — a format that
// mangles links in half the services is worse than none.
func linkLines(links []Link) string {
	if len(links) == 0 {
		return ""
	}
	var b strings.Builder
	for i, l := range links {
		if i > 0 {
			b.WriteString("\n")
		}
		if l.Label != "" {
			b.WriteString(l.Label)
			b.WriteString(": ")
		}
		b.WriteString(l.Path)
	}
	return b.String()
}

// scrubMessage redacts secrets across EVERY author-influenced field of the
// envelope.
//
// This used to be one line covering Body, and the agent-send handler worked
// around it by scrubbing its own title — with a comment noting the delivery
// path "was never asked to" cover that field. The three producers that did
// not know to work around it (journal bridge, inbox router, recovery sweep)
// delivered titles straight from a journal summary to Discord, unscrubbed.
//
// Scrubbing is a property of delivering a message, not of one caller, so it
// happens once, here, where all four producers converge.
func scrubMessage(m *CategoryMessage, scrub func(string) string) {
	m.Title = scrub(m.Title)
	m.Body = scrub(m.Body)
	for i := range m.Links {
		m.Links[i].Label = scrub(m.Links[i].Label)
		// The path too: a link is a place a secret can hide in a query
		// string, and a scrubbed link that no longer resolves is the
		// correct outcome — the link was the leak.
		m.Links[i].Path = scrub(m.Links[i].Path)
	}
	if m.Vars != nil {
		if out, ok := scrubValue(jsonNormalise(m.Vars), scrub).(map[string]any); ok {
			m.Vars = out
		} else {
			// Unreachable in practice, but the safe direction is explicit:
			// facts we could not normalise are facts we cannot redact.
			m.Vars = nil
		}
	}
}

// bytesToText replaces every []byte anywhere in a producer payload with its
// text form, BEFORE the value reaches json.Marshal.
//
// json.Marshal encodes a []byte as base64, so a secret in one arrives at
// scrubValue as a string that matches no pattern and leaves as base64 — an
// encoding, not a redaction, and one anyone can undo. Converting first means
// the scrubber sees the bytes as the text they are.
//
// Reflection because a []byte can sit anywhere: a field of a struct, an
// element of a slice, a value in a typed map. Only values are read and a new
// value is always returned, so the caller's data is never modified. Kinds
// that cannot contain one (numbers, bools) are returned as they are.
func bytesToText(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return bytesToText(v.Elem())
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			// The one case this exists for.
			if v.Kind() == reflect.Array {
				out := reflect.New(v.Type()).Elem()
				reflect.Copy(out, v)
				v = out.Slice(0, v.Len())
			}
			return string(v.Bytes())
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = bytesToText(v.Index(i))
		}
		return out
	case reflect.Map:
		out := make(map[string]any, v.Len())
		for _, k := range v.MapKeys() {
			out[fmt.Sprint(k.Interface())] = bytesToText(v.MapIndex(k))
		}
		return out
	case reflect.Struct:
		out := make(map[string]any, v.NumField())
		t := v.Type()
		for i := range v.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			out[jsonFieldName(f)] = bytesToText(v.Field(i))
		}
		return out
	default:
		return v.Interface()
	}
}

// jsonFieldName honours a field's json tag so the normalised shape matches
// what the webhook would have serialised.
func jsonFieldName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return f.Name
	}
	if name, _, _ := strings.Cut(tag, ","); name != "" {
		return name
	}
	return f.Name
}

// jsonNormalise converts a producer's payload into the JSON-native shapes
// scrubValue can walk: string, map[string]any, []any, and scalars.
//
// Vars comes from Go producers, not from decoded JSON, so it holds Go values —
// lookout writes `Payload["findings"] = result.Findings`, a []lookout.Finding.
// scrubValue's type switch only knew string, map[string]any and []any, so
// every other shape fell to its default branch and was returned untouched: a
// []string of hosts, a map[string]string of headers, a slice of structs
// carrying an excerpt of the prompt that tripped a guardrail. The body and
// title of the same message were redacted, so nothing in the delivered text
// showed it — only the webhook JSON carried it.
//
// Round-tripping through JSON is what the webhook serialiser does anyway, so
// this normalises to exactly the shape that would be sent, and no earlier.
// Reflection would work too and would mutate values the caller still owns.
//
// A payload that cannot be marshalled (a channel, a func) is a producer bug;
// returning nil drops the facts rather than forwarding something unredactable.
func jsonNormalise(v map[string]any) any {
	raw, err := json.Marshal(bytesToText(reflect.ValueOf(v)))
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// scrubValue walks a Vars tree redacting every string it reaches.
//
// Vars is the fact bag templates render against, so anything in it can end up
// in a delivered body — and a producer copying a source payload in wholesale
// is the expected usage, not the exotic one. Recursing costs little and
// removes the footgun of a secret one level down surviving.
func scrubValue(v any, scrub func(string) string) any {
	switch t := v.(type) {
	case string:
		return scrub(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			// Keys too. A payload keyed by a token — or a header map built
			// the wrong way round — put the secret on the left-hand side,
			// where scrubbing only values never reached it.
			out[scrub(k)] = scrubValue(val, scrub)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = scrubValue(val, scrub)
		}
		return out
	default:
		return v
	}
}
