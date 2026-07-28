package notify

import (
	"fmt"
	"regexp"
	"strings"
)

// Message templates: how an operator changes what a notification SAYS.
//
// A routine's notify step has always been able to write its own message. What
// the product generates itself could not be changed at all — "Pipeline x
// completed" and "Scheduled routine failed: y" are computed at their
// producers, in Go, one string per site.
//
// The envelope carrying Vars is what makes this possible: a template can only
// reference what the message carries, so until those facts survived to
// delivery, automatic notifications were unparameterisable by construction.
//
// Scope is the CATEGORY, optionally narrowed to one channel. Wording is a
// property of the event — "a routine failed" reads the same wherever it goes —
// while format is a property of the destination, and conflating the two axes
// is the mistake that makes a template system hard to unwind later. The
// channel narrowing exists for the case where one destination genuinely wants
// something different (a terse line for a pager, a fuller one for e-mail).
//
// Templates deliberately do NOT apply to messages somebody already wrote: a
// routine's notify step and an agent's notify_send carry an author's words,
// and overwriting those would be a different feature with a worse name.

// messageTemplateRE matches the same `{{ ... }}` placeholder the routine DSL
// uses (internal/pipeline.templateRE). Same syntax on purpose: an operator who
// has written a notify step already knows this one.
var messageTemplateRE = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// MessageTemplate overrides the wording of one category's notifications.
//
// An empty Title or Body means "leave what the producer computed" — which is
// the state every category ships in, so the common path changes nothing.
type MessageTemplate struct {
	// Category is the notification category this applies to.
	Category string
	// ChannelID narrows it to a single channel. Empty applies to every
	// channel the category reaches.
	ChannelID string
	// Title and Body are templates. Empty means "unchanged".
	Title string
	Body  string
}

// Apply returns msg with the template's wording rendered in.
//
// It writes the WORDS and nothing else: links, category, priority and the
// facts themselves are routing and evidence, not prose, and a template that
// could alter them would be able to send a message somewhere its author never
// chose.
//
// A template that renders to nothing falls back to what the producer computed.
// The alternative is a notification with an empty subject line — on a push
// service, a blank entry on someone's lock screen — and an operator whose
// template names a fact this particular event lacks has made a mistake worth
// degrading gracefully around.
func (t MessageTemplate) Apply(msg CategoryMessage) CategoryMessage {
	if title := strings.TrimSpace(RenderTemplate(t.Title, msg)); title != "" {
		msg.Title = title
	}
	if body := strings.TrimSpace(RenderTemplate(t.Body, msg)); body != "" {
		msg.Body = body
	}
	return msg
}

// RenderTemplate substitutes `{{ ... }}` references in tmpl against msg.
//
// Two namespaces:
//
//	vars.<key>[.<sub>]  — the source event's own facts
//	source.<field>      — what the producer computed: title, body, category,
//	                      kind. This is what lets a template ADD to a message
//	                      ("[{{ source.category }}] {{ source.title }}")
//	                      rather than only replace it.
//
// A reference that resolves to nothing renders empty, matching what an absent
// routine input does. A template naming a fact this event does not carry
// should cost that fragment, not the delivery.
func RenderTemplate(tmpl string, msg CategoryMessage) string {
	if tmpl == "" {
		return ""
	}
	return messageTemplateRE.ReplaceAllStringFunc(tmpl, func(match string) string {
		ref := strings.TrimSpace(match[2 : len(match)-2])
		v, ok := resolveTemplateRef(ref, msg)
		if !ok {
			return ""
		}
		return v
	})
}

func resolveTemplateRef(ref string, msg CategoryMessage) (string, bool) {
	head, rest, found := strings.Cut(ref, ".")
	if !found || rest == "" {
		return "", false
	}
	switch head {
	case "vars":
		return lookupVar(msg.Vars, rest)
	case "source":
		switch rest {
		case "title":
			return msg.Title, true
		case "body":
			return msg.Body, true
		case "category":
			return msg.Category, true
		case "kind":
			return msg.SourceKind, true
		}
		return "", false
	}
	return "", false
}

// lookupVar resolves `key` or `key.sub` against the fact bag. One level of
// nesting, like the routine DSL's inputs — deeper paths belong to a step that
// projects the value, not to a message template.
func lookupVar(vars map[string]any, path string) (string, bool) {
	key, sub, nested := strings.Cut(path, ".")
	v, ok := vars[key]
	if !ok {
		return "", false
	}
	if nested {
		m, isMap := v.(map[string]any)
		if !isMap {
			return "", false
		}
		if v, ok = m[sub]; !ok {
			return "", false
		}
	}
	if v == nil {
		return "", false
	}
	return fmt.Sprint(v), true
}
