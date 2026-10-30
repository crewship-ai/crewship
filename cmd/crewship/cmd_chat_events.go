package main

// Human renderings for the session-channel events that `crewship run` and
// `crewship chat stream` did not used to recognise. The web chat draws each of
// these (hooks/use-chat.ts handle*Event); a terminal that dropped them showed a
// run that delegated, escalated, provisioned or was refused as one that just
// went quiet. Every line goes to stderr — stdout stays the reply text only.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// eventMeta reads metadata as a string-keyed map, whatever shape decoding gave.
func eventMeta(meta any) map[string]any {
	switch m := meta.(type) {
	case map[string]any:
		return m
	case nil:
		return nil
	default:
		// json.RawMessage or a typed struct — normalise through JSON once.
		raw, err := json.Marshal(m)
		if err != nil {
			return nil
		}
		var out map[string]any
		if json.Unmarshal(raw, &out) != nil {
			return nil
		}
		return out
	}
}

func metaStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%g", v)
	case bool:
		return fmt.Sprintf("%t", v)
	}
	return ""
}

func metaNum(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key].(float64)
	return v, ok
}

// formatResultSummary renders a `result` event's metadata — the run summary
// the adapter reports (total_cost_usd, usage.input/output_tokens, num_turns,
// duration_ms) — as one line. Cost is absent for viewers who may not see
// money (the server redacts it), and the line simply omits it then.
func formatResultSummary(meta any) string {
	m := eventMeta(meta)
	var parts []string
	if v, ok := metaNum(m, "total_cost_usd"); ok {
		parts = append(parts, fmt.Sprintf("$%.4f", v))
	}
	if usage := eventMeta(m["usage"]); usage != nil {
		in, okIn := metaNum(usage, "input_tokens")
		out, okOut := metaNum(usage, "output_tokens")
		if okIn || okOut {
			parts = append(parts, fmt.Sprintf("%d in / %d out tokens", int64(in), int64(out)))
		}
	}
	if v, ok := metaNum(m, "num_turns"); ok {
		parts = append(parts, fmt.Sprintf("%d turns", int64(v)))
	}
	if v, ok := metaNum(m, "duration_ms"); ok && v > 0 {
		parts = append(parts, (time.Duration(v) * time.Millisecond).Round(100*time.Millisecond).String())
	}
	return strings.Join(parts, " · ")
}

// askSubmissionErrorText turns an ask_submission_error event into the message
// to print and return: the notice plus one line per refused field.
func askSubmissionErrorText(content string, meta any) string {
	m := eventMeta(meta)
	msg := strings.TrimSpace(content)
	var fields []string
	if list, ok := m["ask_errors"].([]any); ok {
		for _, item := range list {
			e := eventMeta(item)
			line := metaStr(e, "message")
			if f := metaStr(e, "field"); f != "" {
				line = f + ": " + line
			}
			if line != "" {
				fields = append(fields, line)
			}
		}
	}
	if msg == "" && len(fields) > 0 {
		msg = "the form was refused"
	}
	if msg == "" {
		msg = "the form could not be sent"
	}
	if len(fields) > 0 {
		msg += " (" + strings.Join(fields, "; ") + ")"
	}
	return msg
}

// describeSessionEvent renders the informational events — never text, never
// terminal — as "[label] detail". ok is false for types it does not know.
func describeSessionEvent(typ, content string, meta any) (label, detail string, ok bool) {
	m := eventMeta(meta)
	pre := func(prefix, v string) string {
		if strings.TrimSpace(v) == "" {
			return ""
		}
		return prefix + v
	}
	join := func(parts ...string) string {
		var kept []string
		for _, p := range parts {
			if strings.TrimSpace(p) != "" {
				kept = append(kept, p)
			}
		}
		return strings.Join(kept, " · ")
	}
	switch typ {
	case "user_message":
		who := metaStr(m, "author_user_id")
		text := truncate(content, 100)
		if who != "" {
			return "user", who + ": " + text, true
		}
		return "user", text, true
	case "result":
		return "summary", formatResultSummary(meta), true
	case "agent_busy":
		return "busy", content, true
	case "crew_provisioning":
		return "building", join(content, metaStr(m, "status")), true
	case "ask_submission_error":
		return "form refused", askSubmissionErrorText(content, meta), true
	case "system":
		sub := metaStr(m, "subtype")
		if sub == "init" {
			return "session", join(pre("model ", metaStr(m, "model")), metaStr(m, "cwd")), true
		}
		return "system", join(sub, truncate(content, 100)), true
	case "image":
		name := metaStr(m, "path")
		if name == "" {
			name = metaStr(m, "filename")
		}
		if name == "" {
			name = metaStr(m, "media_type")
		}
		return "image", join(name, fmt.Sprintf("%d bytes", len(content))), true
	case "assignment_created":
		return "delegated", join(pre("→ ", metaStr(m, "target")), truncate(metaStr(m, "task"), 100), metaStr(m, "id")), true
	case "assignment_queued":
		return "delegation queued", join(pre("→ ", metaStr(m, "target")), metaStr(m, "id")), true
	case "assignment_unqueued", "assignment_running":
		return "delegation running", join(metaStr(m, "target"), metaStr(m, "id")), true
	case "assignment_completed":
		return "delegation done", join(metaStr(m, "target"), truncate(metaStr(m, "result"), 100)), true
	case "assignment_failed":
		return "delegation failed", join(metaStr(m, "target"), metaStr(m, "error")), true
	case "assignment_cancelled":
		return "delegation cancelled", join(metaStr(m, "target"), metaStr(m, "id")), true
	case "escalation_created":
		return "escalation", join(pre("from ", metaStr(m, "from")), truncate(metaStr(m, "reason"), 120), metaStr(m, "id")), true
	case "escalation_resolved":
		return "escalation resolved", join(metaStr(m, "action"), metaStr(m, "resolution")), true
	case "escalation_expired", "escalation_cancelled":
		return strings.ReplaceAll(typ, "_", " "), join(metaStr(m, "id"), metaStr(m, "reason"), metaStr(m, "warning")), true
	case "peer_query_running":
		return "asking peer", join(metaStr(m, "from"), pre("→ ", metaStr(m, "target")), metaStr(m, "id")), true
	case "peer_query_completed":
		return "peer answered", join(metaStr(m, "target"), truncate(metaStr(m, "response"), 100)), true
	case "peer_query_failed":
		return "peer query failed", join(metaStr(m, "target"), metaStr(m, "error")), true
	case "port_expose_created":
		return "port exposed", join(metaStr(m, "url"), metaStr(m, "description"), pre("until ", metaStr(m, "expires_at"))), true
	}
	return "", "", false
}
