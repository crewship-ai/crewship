package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Bounds for the session-init payload. Nothing downstream caps a journal
// payload's size — not the emitter, not the writer, not the column — so the
// cap has to live at the emit site. The CLI decides how many tools and MCP
// servers it reports, and a single run must not be able to write an
// arbitrarily large, permanently stored row.
const (
	sessionInitFieldMax = 256 // bytes kept per CLI-supplied scalar
	sessionInitListMax  = 32  // elements kept per list-shaped field
	// How many skipped-server names the summary names before it stops. The
	// summary is one line a human reads in the feed; the full (bounded) list
	// is in the payload.
	sessionInitSummaryNames = 3
)

// emitSessionInitSignal records the provenance of the agent CLI session that
// this run is about to happen inside — which binary answered, which model it
// resolved to, which credential path it took, and the tool/MCP inventory it
// started with. Emitted once per run from the session-init event, best-effort:
// a journal hiccup never affects the run.
//
// The field that makes this urgent rather than merely nice is
// mcp_server_errors. A --mcp-config entry that fails validation is SKIPPED and
// the run continues, exiting 0: an agent that lost crewship-memory that way
// looks perfectly healthy while being quietly less capable, and this event is
// the only place it is ever reported. So severity escalates to error when the
// list is non-empty — the same call sidecar.stale makes, and for the same
// reason: a capability silently degraded for the run happening right now.
//
// WHAT MAY NOT GO IN THE PAYLOAD. This is emitted from the tap that sits
// BEFORE the credential scrubber (stream → scrub → journalTap → user), so
// nothing copied out of the init metadata is scrubbed; and journal rows are
// hash-chained and append-only, so they cannot be redacted afterwards. A
// secret written here is written forever. Hence: only closed-category values,
// counts, and short identifiers — no free text. In particular
// mcp_server_errors[].message is operator-authored config text and is
// deliberately DROPPED; `name` plus the closed `type` category
// (unknown_type / url_missing_type / invalid_config / reserved_name …) say
// which server vanished and why-in-kind, which is what triage needs. The
// verbatim line is still available in this run's exec.output_chunk entry for
// anyone who needs to read it.
func (o *Orchestrator) emitSessionInitSignal(ctx context.Context, req AgentRunRequest, meta map[string]any) {
	payload := map[string]any{
		"agent_slug":  req.AgentSlug,
		"cli_adapter": req.CLIAdapter,
	}
	put := func(key, value string) {
		if value == "" {
			return
		}
		bounded, _ := truncateBytes(value, sessionInitFieldMax)
		payload[key] = bounded
	}
	model := initMetaString(meta, "model")
	put("model", model)
	put("cli_version", initMetaString(meta, "claude_code_version"))
	put("session_id", initMetaString(meta, "session_id"))
	put("permission_mode", initMetaString(meta, "permissionMode"))
	put("cwd", initMetaString(meta, "cwd"))
	// apiKeySource is upstream free text that names WHERE the credential came
	// from; safeAPIKeySource maps it onto a known set (see model_resolution.go)
	// so a path — or anything else the CLI decides to put there — is recorded
	// as "other" rather than quoted into a permanent row.
	put("api_key_source", safeAPIKeySource(initMetaString(meta, apiKeySourceMetaKey)))

	// Inventories are recorded as COUNTS, not lists: the names are long, they
	// are identical on every run of the same agent, and "14 tools" answers the
	// question a reader actually has ("did it start with what I configured?").
	// An absent key stays absent — an adapter that reports no inventory did not
	// report an EMPTY one, and "0 tools" would be a lie.
	putCount := func(key string, v any) {
		if n, ok := initMetaCount(v); ok {
			payload[key] = n
		}
	}
	putCount("tool_count", meta["tools"])
	putCount("capability_count", meta["capabilities"])
	putCount("skill_count", meta["skills"])

	servers := initMetaObjects(meta["mcp_servers"])
	if len(servers) > 0 {
		payload["mcp_server_count"] = len(servers)
		kept, truncated := boundInitObjects(servers, "name", "status")
		payload["mcp_servers"] = kept
		if truncated {
			payload["mcp_servers_truncated"] = true
		}
	}

	// Degradation is decided by PRESENCE, not by whether we understood the
	// shape. The adapter forwards this value unparsed, so this is the first
	// code with an opinion about it — and if a future CLI reports the skips as
	// an object keyed by server name, or as bare strings, decoding here yields
	// nothing and the run would be written at info with a healthy summary. "The
	// CLI told us it dropped a server" is the fact worth keeping; the names are
	// detail that can be missing.
	rawSkips, hasSkips := meta["mcp_server_errors"]
	degraded := hasSkips && !emptyJSONValue(rawSkips)
	skipped := initMetaObjects(rawSkips)
	if degraded {
		// Prefer the decoded count, fall back to counting array elements, and
		// leave the count out entirely rather than reporting a wrong one.
		if n := len(skipped); n > 0 {
			payload["mcp_server_error_count"] = n
		} else if n, ok := initMetaCount(rawSkips); ok {
			payload["mcp_server_error_count"] = n
		}
		// Drop entries that projected to nothing: an alarm listing {} names no
		// server, and a count with an empty list reads as a parser bug rather
		// than as "the CLI phrased this differently".
		kept, truncated := boundInitObjects(skipped, "name", "type")
		kept = dropEmptyObjects(kept)
		if len(kept) > 0 {
			payload["mcp_server_errors"] = kept
			if truncated {
				payload["mcp_server_errors_truncated"] = true
			}
		}
	}

	// Plugins get counts only. Plugin discovery is off under
	// --setting-sources "", so today these are always absent; when a per-agent
	// opt-out lands, a non-zero plugin_error_count is the same class of silent
	// capability loss — and a count carries no text to leak.
	putCount("plugin_count", meta["plugins"])
	putCount("plugin_error_count", meta["plugin_errors"])

	severity := journal.SeverityInfo
	summary := sessionInitSummary(req.AgentSlug, model, payload)
	if degraded {
		severity = journal.SeverityError
		summary = skippedServerSummary(req.AgentSlug, skipped, servers)
	}

	_, _ = o.getJournal().Emit(ctx, JournalEntry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		MissionID:   req.MissionID,
		Type:        string(journal.EntryRunSessionInit),
		Severity:    string(severity),
		ActorType:   "agent",
		ActorID:     req.AgentID,
		Summary:     summary,
		Payload:     payload,
		Refs:        map[string]any{"chat_id": req.ChatID},
	})
}

// sessionInitSummary describes the healthy session in one line, naming only
// what the adapter actually reported — a gemini/codex init carries a model and
// nothing else, and padding that out with zeroes would read as an agent that
// started with no tools.
func sessionInitSummary(slug, model string, payload map[string]any) string {
	parts := make([]string, 0, 3)
	if model != "" {
		parts = append(parts, model)
	}
	if n, ok := payload["tool_count"].(int); ok {
		parts = append(parts, fmt.Sprintf("%d tools", n))
	}
	if n, ok := payload["mcp_server_count"].(int); ok {
		parts = append(parts, fmt.Sprintf("%d MCP servers", n))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%s session started — the CLI reported no provenance", slug)
	}
	return fmt.Sprintf("%s session on %s", slug, strings.Join(parts, ", "))
}

// skippedServerSummary is the line that has to be unmistakable: the run is
// about to proceed and exit 0 without a server it was configured with, and the
// feed is where anyone finds out.
func skippedServerSummary(slug string, skipped, inventory []map[string]any) string {
	names := make([]string, 0, sessionInitSummaryNames+1)
	for _, s := range skipped {
		label := skippedServerLabel(s)
		if label == "" {
			// Nothing readable in this entry, so it contributes nothing to a
			// line whose whole job is naming what was lost. It used to become
			// "(unnamed)", which meant len(names) was never 0 once the value
			// decoded into objects — and the honest branch below could never
			// run. A report whose keys the CLI renamed rendered as "1 of 1
			// configured MCP servers were SKIPPED ((unnamed))": a sentence that
			// promises a name, delivers a placeholder, and is worse than
			// admitting we could not read the report.
			continue
		}
		if len(names) == sessionInitSummaryNames {
			names = append(names, "…")
			break
		}
		names = append(names, label)
	}
	if len(names) == 0 {
		// Degradation is decided by the presence of the report, not by our
		// ability to read it, so we can land here knowing the CLI dropped
		// something and knowing nothing else. Say that. The counted form would
		// otherwise render "0 of N were SKIPPED ()" — a line that contradicts
		// its own severity and reads as a broken alert, which is how an alert
		// stops being read.
		return fmt.Sprintf("%s session DEGRADED — the CLI reported skipped MCP servers in a shape this build cannot read; the run continues without them", slug)
	}
	return fmt.Sprintf("%s session DEGRADED — %d of %d configured MCP servers were SKIPPED at startup (%s); the run continues without them",
		slug, len(skipped), configuredServerCount(skipped, inventory), strings.Join(names, ", "))
}

// configuredServerCount is the denominator of "N of M configured MCP servers
// were SKIPPED" — how many servers this session was set up with, counting each
// one once.
//
// It cannot be len(inventory)+len(skipped). The CLI lists a server it failed to
// load in BOTH arrays: once under mcp_servers carrying a failed status, once
// under mcp_server_errors carrying the reason. Adding the lists inflated the
// total by exactly the failures the number exists to put in perspective — a
// session with two servers, one of them dead, read as "1 of 3", understating
// the loss on the one line an operator uses to judge how degraded the session
// is. So a skip is added only when the inventory does not already contain it.
//
// An unnamed skip is counted as its own server: we cannot match it against the
// inventory, and over-counting the denominator by one understates the damage,
// which is the safer direction to be wrong in for a line that has to be trusted.
func configuredServerCount(skipped, inventory []map[string]any) int {
	known := make(map[string]struct{}, len(inventory))
	for _, s := range inventory {
		if name := boundedInitField(s, "name"); name != "" {
			known[name] = struct{}{}
		}
	}
	total := len(inventory)
	for _, s := range skipped {
		name := boundedInitField(s, "name")
		if name == "" {
			total++
			continue
		}
		if _, ok := known[name]; !ok {
			total++
			// A CLI that repeats the same skipped server must not add it twice.
			known[name] = struct{}{}
		}
	}
	return total
}

// skippedServerLabel names one skipped server for the summary line: the name
// the CLI gave, qualified by the failure category when it gave one — and the
// category alone when it named no server, which at least says what KIND of
// failure took a capability away. Empty when the entry carried neither, which
// is the caller's signal to leave it out rather than pad it: the same fallback
// order the `run get` / `run list` renderers use for these entries.
func skippedServerLabel(s map[string]any) string {
	name := boundedInitField(s, "name")
	kind := boundedInitField(s, "type")
	switch {
	case name == "":
		return kind
	case kind == "":
		return name
	default:
		return name + ": " + kind
	}
}

// boundInitObjects projects each object down to the named keys ONLY — an allowlist,
// so a field the CLI adds later (or a free-text one it already has) cannot ride
// along into a permanent row — and caps the list length. Returns the kept
// slice and whether anything was cut; the caller records the true total
// separately so a cut list never understates what happened.
func boundInitObjects(objs []map[string]any, keys ...string) ([]map[string]any, bool) {
	truncated := len(objs) > sessionInitListMax
	if truncated {
		objs = objs[:sessionInitListMax]
	}
	out := make([]map[string]any, 0, len(objs))
	for _, o := range objs {
		kept := make(map[string]any, len(keys))
		for _, k := range keys {
			if v := boundedInitField(o, k); v != "" {
				kept[k] = v
			}
		}
		out = append(out, kept)
	}
	return out, truncated
}

// boundedInitField reads a string field and bounds its length. Non-string values
// are dropped rather than formatted: a field whose shape changed is a field we
// no longer understand, and guessing is how unexpected content gets persisted.
func boundedInitField(obj map[string]any, key string) string {
	s, ok := obj[key].(string)
	if !ok || s == "" {
		return ""
	}
	bounded, _ := truncateBytes(s, sessionInitFieldMax)
	return bounded
}

func initMetaString(meta map[string]any, key string) string {
	s, _ := meta[key].(string)
	return s
}

// initMetaCount counts a list-shaped metadata value without caring how the
// adapter typed it: the Claude adapter passes some fields through as raw JSON
// (their shape is not a published promise) while others arrive as []string.
// Returns ok=false for anything that is not a countable list, so the caller can
// leave the key off entirely rather than record a made-up zero.
func initMetaCount(v any) (int, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case []string:
		return len(t), true
	case []any:
		return len(t), true
	case []map[string]any:
		return len(t), true
	case []json.RawMessage:
		return len(t), true
	case json.RawMessage:
		return countInitJSONArray(t)
	case []byte:
		return countInitJSONArray(t)
	}
	return 0, false
}

func countInitJSONArray(raw []byte) (int, bool) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return 0, false
	}
	return len(elems), true
}

// initMetaObjects normalises a list of JSON objects out of whatever the
// adapter put on the event — []json.RawMessage (mcp_servers), a raw array
// (mcp_server_errors), or already-decoded maps. Anything unparseable yields no
// objects: this drives an operator-facing alert, and a half-understood shape is
// better reported as "nothing" than as a guess.
func initMetaObjects(v any) []map[string]any {
	switch t := v.(type) {
	case nil:
		return nil
	case []map[string]any:
		return t
	case []json.RawMessage:
		out := make([]map[string]any, 0, len(t))
		for _, raw := range t {
			var obj map[string]any
			if json.Unmarshal(raw, &obj) == nil {
				out = append(out, obj)
			}
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, e := range t {
			if obj, ok := e.(map[string]any); ok {
				out = append(out, obj)
			}
		}
		return out
	case json.RawMessage:
		return decodeInitObjectArray(t)
	case []byte:
		return decodeInitObjectArray(t)
	}
	return nil
}

func decodeInitObjectArray(raw []byte) []map[string]any {
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// dropEmptyObjects removes projected objects that kept no field. boundInitObjects
// keeps a key only when it finds a non-empty string, so an entry whose fields the
// CLI renamed — or typed as something other than a string — projects to {}. Those
// are removed rather than stored: the count still says how many servers were
// skipped, and a list of blanks only makes the report look broken.
func dropEmptyObjects(objs []map[string]any) []map[string]any {
	out := objs[:0]
	for _, o := range objs {
		if len(o) > 0 {
			out = append(out, o)
		}
	}
	return out
}
