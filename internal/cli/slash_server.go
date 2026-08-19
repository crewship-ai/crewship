package cli

// Server-driven slash command registration for the REPL
// (PRD-SLASH-CAPABILITIES-2026 §6.8).
//
// At REPL boot we fetch GET /api/v1/slash-commands?workspace_id=...
// and Register each returned entry as a slash command. Handler
// arguments are parsed as key=value pairs (single line) and POSTed
// to the matching public endpoint. Server-side capability recheck
// is the authoritative gate; the CLI's only job is shape-mapping.
//
// Wire shape (single-line invocation):
//
//   crewship › /routine name="Weekly digest" cron="0 7 * * MON" timezone=Europe/Prague
//
// Quoted values for spaces; unquoted for single-token values. This
// is a pragmatic compromise — full interactive prompting would be a
// better UX for fields like 'prompt' (skill body) or 'content'
// (memory write) that span multiple lines, but adding multi-line
// reads while keeping the cancel-on-ctx behaviour of Run() is more
// surface than this commit warrants. Power users can pipe
// `cat body.md | crewship run --skill-prompt -` instead.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ServerSlashCommand mirrors the JSON shape SlashCommandsHandler
// returns. We don't import the api package — circular deps and
// the wire shape is small enough that re-declaring it here keeps
// the CLI compilable standalone.
type ServerSlashCommand struct {
	ID         string             `json:"id"`
	Label      string             `json:"label"`
	LabelCS    string             `json:"label_cs,omitempty"`
	Icon       string             `json:"icon,omitempty"`
	Capability string             `json:"capability"`
	FormSchema []ServerSlashField `json:"form_schema,omitempty"`
}

// ServerSlashField mirrors the slashFormField wire shape.
type ServerSlashField struct {
	Name string `json:"name"`
	// Type is the widget the dashboard draws. The repl doesn't draw
	// anything, so it reads this only to pick a placeholder in errors —
	// what it converts on is ValueType.
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
	// ValueType is the JSON type the server expects back: string |
	// integer | number | boolean | array | object. Empty means string,
	// which is every field in the static catalog.
	ValueType string `json:"value_type,omitempty"`
}

// SlashHTTPClient is the minimal interface the loader needs. The
// real type at call site is *cli.Client; this interface keeps the
// loader unit-testable without spinning up the full HTTP wiring.
type SlashHTTPClient interface {
	Get(path string) (*http.Response, error)
	Post(path string, body interface{}) (*http.Response, error)
	GetWorkspaceID() string
}

// LoadServerSlashCommands fetches the capability-filtered slash
// catalog for the active workspace and registers each entry on
// the REPL. Returns the count loaded so the caller can surface
// "5 server actions available" in the boot banner.
//
// Failures are non-fatal — a network blip at REPL boot shouldn't
// prevent the user from chatting. The function logs to repl.Err
// and returns 0, no error. The user can manually refresh later via
// the /refresh meta-command (registered separately).
func LoadServerSlashCommands(ctx context.Context, repl *REPL, client SlashHTTPClient) int {
	if client == nil || repl == nil {
		return 0
	}
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		return 0
	}
	resp, err := client.Get("/api/v1/slash-commands?workspace_id=" + url.QueryEscape(wsID))
	if err != nil {
		fmt.Fprintf(repl.Err, "[slash] failed to fetch server actions: %v\n", err)
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(repl.Err, "[slash] server returned %d: %s\n", resp.StatusCode, string(body))
		return 0
	}
	var cmds []ServerSlashCommand
	if err := json.NewDecoder(resp.Body).Decode(&cmds); err != nil {
		fmt.Fprintf(repl.Err, "[slash] decode failed: %v\n", err)
		return 0
	}
	loaded := 0
	for _, cmd := range cmds {
		cmd := cmd // capture
		name := slashCommandName(cmd.ID)
		// Built-ins win, and say so. REPL.Register is a silent map
		// overwrite, so without this a routine slugged `exit` would take
		// /exit and the user would run somebody's accounting pack while
		// trying to leave the shell. Routine slugs match
		// `^[a-z0-9][a-z0-9_-]{0,63}$`, which `exit`, `help`, `clear` and
		// `agent` all satisfy — this is a name a workspace can really
		// pick, not a theoretical one.
		//
		// Same policy, and the same warning, as the file-based catalog in
		// cmd_slash.go: skip it and tell the operator, rather than
		// silently masking either side. The server-side collision guard
		// cannot cover this — it knows the four platform slash ids, and
		// nothing about a repl's built-ins.
		if _, taken := repl.Slash[name]; taken {
			fmt.Fprintf(repl.Err, "[slash] %s shadows a built-in command — skipping\n", name)
			continue
		}
		repl.Register(name, buildSlashHandler(cmd, client, repl.Out))
		loaded++
	}
	return loaded
}

// slashCommandName is the word a user types, derived from the catalog id.
//
// For the platform catalog the two are the same ("issue" → /issue). A
// per-routine entry carries a `routine.run:` prefix that exists to tell
// the dispatcher what kind of thing it is, and typing it would be
// absurd: the command is the routine's slug, so
// `routine.run:msn-etn-podklady` is offered as /msn-etn-podklady.
func slashCommandName(id string) string {
	if slug, ok := routineSlugFromSlashID(id); ok {
		return slug
	}
	return id
}

// routineSlugFromSlashID splits a per-routine catalog id into its slug,
// reporting false for any other id. The single place either client
// decides "this entry runs a routine" — by reading the prefix the server
// put there, never by guessing from the shape of a name.
func routineSlugFromSlashID(id string) (string, bool) {
	if !strings.HasPrefix(id, slashRoutineIDPrefix) {
		return "", false
	}
	slug := strings.TrimPrefix(id, slashRoutineIDPrefix)
	if slug == "" {
		return "", false
	}
	return slug, true
}

// slashRoutineIDPrefix mirrors the server constant of the same name
// (internal/api/slash_routine_catalog.go). Re-declared rather than
// imported for the reason ServerSlashCommand is: the CLI does not import
// the api package.
const slashRoutineIDPrefix = "routine.run:"

// buildSlashHandler returns the REPLHandler that parses the user's
// args, builds the JSON body via slashCommandPayload, and POSTs to
// the matching public endpoint. Errors are surfaced inline so the
// user sees them right after the prompt.
//
// `out` is the REPL's standard-output writer (repl.Out). We thread
// it through explicitly rather than reaching for os.Stdout so
// embedded REPLs (test, future TUI host) capture the success line
// in their own buffer instead of leaking it to the process stdout.
func buildSlashHandler(cmd ServerSlashCommand, client SlashHTTPClient, out io.Writer) REPLHandler {
	if out == nil {
		// Defence: never panic on a nil Out (some test harnesses
		// build REPLs without setting it). Fall back to os.Stdout
		// so the operator at least sees the confirmation; this is
		// the previously default behaviour for completeness.
		out = os.Stdout
	}
	name := slashCommandName(cmd.ID)
	return func(ctx context.Context, args []string) (bool, error) {
		values, err := parseKeyValueArgs(args)
		if err != nil {
			return true, err
		}
		// Required-field check at the client so the user sees
		// "name required" inline instead of round-tripping for a 400.
		for _, f := range cmd.FormSchema {
			if f.Required && values[f.Name] == "" {
				if f.Default != "" {
					values[f.Name] = f.Default
					continue
				}
				return true, fmt.Errorf("required field %q is missing — try /%s %s=<value> …", f.Name, name, f.Name)
			}
		}
		// Apply defaults for unspecified optional fields.
		for _, f := range cmd.FormSchema {
			if _, ok := values[f.Name]; !ok && f.Default != "" {
				values[f.Name] = f.Default
			}
		}
		body, err := slashCommandPayload(cmd, values)
		if err != nil {
			return true, err
		}
		endpoint, err := slashCommandEndpoint(cmd.ID, client.GetWorkspaceID())
		if err != nil {
			return true, err
		}
		resp, err := client.Post(endpoint, body)
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return true, fmt.Errorf("/%s failed: %s — %s", name, resp.Status, string(respBody))
		}
		// Success — surface a short confirmation via repl.Out so
		// embedded REPLs (tests, TUI host) capture it.
		fmt.Fprintf(out, "[/%s] ✓\n", name)
		return true, nil
	}
}

// keyValuePattern matches `key=value` and `key="value with spaces"`.
// Quoted form supports spaces and = inside the value; bare form
// stops at the first whitespace.
var keyValuePattern = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)=(?:"([^"]*)"|(\S+))`)

// parseKeyValueArgs walks the args slice (already split on
// whitespace by the REPL) and reconstructs key=value pairs that may
// contain quoted spaces. We re-join + re-parse with a regex because
// the REPL's strings.Fields split breaks `key="a b"` into ["key=\"a",
// "b\""] — losing the structure we need.
//
// Rejection rules:
//
//   - At least one key=value match is required when args are
//     non-blank. A line that's all garbage (no = signs) errors.
//   - Bytes that didn't match any kv pair are an error too. The
//     previously implementation silently dropped them, which hid
//     typos like `crons=0 7 * * MON` (missing `e` → no match → no
//     warning, server gets `cron_expr=""` and 400s confusingly).
//     Now we sum the matched-span lengths and compare against the
//     joined input length (after collapsing inter-token whitespace
//     introduced by strings.Join); any positive remainder means
//     there's content the user typed that the parser didn't
//     understand.
func parseKeyValueArgs(args []string) (map[string]string, error) {
	if len(args) == 0 {
		return map[string]string{}, nil
	}
	joined := strings.Join(args, " ")
	out := map[string]string{}
	matchIdx := keyValuePattern.FindAllStringSubmatchIndex(joined, -1)
	if len(matchIdx) == 0 && strings.TrimSpace(joined) != "" {
		return nil, fmt.Errorf("could not parse args — use key=value or key=\"quoted value\" form")
	}

	// Walk matches in order, capturing each kv pair AND the gap
	// before it. A non-whitespace gap means the user typed something
	// the regex didn't recognise — surface it.
	var leftover strings.Builder
	cursor := 0
	for _, idx := range matchIdx {
		start, end := idx[0], idx[1]
		gap := joined[cursor:start]
		if strings.TrimSpace(gap) != "" {
			leftover.WriteString(strings.TrimSpace(gap))
			leftover.WriteString(" ")
		}
		// idx[2..3] = key, idx[4..5] = quoted val, idx[6..7] = bare val
		key := joined[idx[2]:idx[3]]
		var val string
		if idx[4] >= 0 {
			val = joined[idx[4]:idx[5]]
		} else if idx[6] >= 0 {
			val = joined[idx[6]:idx[7]]
		}
		out[key] = val
		cursor = end
	}
	if cursor < len(joined) {
		tail := strings.TrimSpace(joined[cursor:])
		if tail != "" {
			leftover.WriteString(tail)
		}
	}
	if leftover.Len() > 0 {
		return nil, fmt.Errorf("unparseable args: %q — use key=value or key=\"quoted value\" form", strings.TrimSpace(leftover.String()))
	}
	return out, nil
}

// slashCommandEndpoint maps slash command id → public API endpoint.
// Mirror of components/features/chat/composer/slash-action-modal.tsx
// (endpointForCommand). One-place-changes whenever a new slash
// command lands — keep these two in sync.
func slashCommandEndpoint(id, workspaceID string) (string, error) {
	ws := url.PathEscape(workspaceID)
	// Per-routine entries first: their id carries the slug, so they can't
	// be a case in the switch below. The prefix is what identifies them —
	// the server put it there for this, and reading it beats inferring an
	// endpoint from a routine name that could be anything.
	if slug, ok := routineSlugFromSlashID(id); ok {
		return "/api/v1/workspaces/" + ws + "/pipelines/" + url.PathEscape(slug) + "/run", nil
	}
	switch id {
	case "routine":
		return "/api/v1/workspaces/" + ws + "/pipeline-schedules", nil
	case "skill":
		return "/api/v1/workspaces/" + ws + "/skills/generate", nil
	case "credential":
		return "/api/v1/credentials?workspace_id=" + url.QueryEscape(workspaceID), nil
	case "issue":
		return "/api/v1/issues?workspace_id=" + url.QueryEscape(workspaceID), nil
	// "remember" intentionally absent — see catalog note in
	// slash_commands_handler.go.
	default:
		return "", fmt.Errorf("unknown slash command id: %s", id)
	}
}

// slashCommandPayload reshapes the flat key=value map into the body
// shape the matching handler expects. Mirror of buildPayload in
// slash-action-modal.tsx — the two MUST stay in sync, including the
// fallback defaults for optional fields. The UI applies "UTC" /
// "SECRET" / "none" defaults when the form-schema field is unset;
// the CLI used to ship those values as empty strings, which made
// `/routine` / `/credential` / `/issue` behave subtly differently
// between the dashboard and the REPL when the user omitted the
// optional value.
//
// Return type is map[string]any (not bare any) so callers don't
// type-assert. JSON marshalling treats both shapes identically; the
// typed return surfaces shape mistakes at compile time.
//
// Takes the whole command rather than its id because a per-routine
// entry needs the form schema: its fields carry the JSON type each value
// has to be restored to before the routine's steps see it. The error
// return exists for the same reason — a value the user
// typed can be un-restorable (a malformed JSON object), and that is
// worth saying at the prompt rather than shipping as a string and
// letting the server 400 with something less useful.
func slashCommandPayload(cmd ServerSlashCommand, values map[string]string) (map[string]any, error) {
	if _, ok := routineSlugFromSlashID(cmd.ID); ok {
		inputs, err := routineInputsFromValues(cmd.FormSchema, values)
		if err != nil {
			return nil, err
		}
		return map[string]any{"inputs": inputs}, nil
	}
	// nonEmptyOr returns the first non-empty value (or the fallback)
	// — used to apply UI-parity defaults inline.
	nonEmptyOr := func(v, fallback string) string {
		if v == "" {
			return fallback
		}
		return v
	}
	switch cmd.ID {
	case "routine":
		return map[string]any{
			"name":      values["name"],
			"cron_expr": values["cron"],
			"timezone":  nonEmptyOr(values["timezone"], "UTC"),
		}, nil
	case "skill":
		return map[string]any{
			"slug":   values["slug"],
			"prompt": values["prompt"],
		}, nil
	case "credential":
		return map[string]any{
			"name":  values["name"],
			"type":  nonEmptyOr(values["type"], "SECRET"),
			"value": values["value"],
		}, nil
	case "issue":
		return map[string]any{
			"title":       values["title"],
			"description": values["description"],
			"priority":    nonEmptyOr(values["priority"], "none"),
		}, nil
	default:
		// Fall through: pass the raw values map. The server will
		// 400 if the shape is wrong; better than fabricating a
		// payload for an action we don't know.
		out := make(map[string]any, len(values))
		for k, v := range values {
			out[k] = v
		}
		return out, nil
	}
}

// routineInputsFromValues turns the form's strings back into the typed
// `inputs` map the run endpoint validates.
//
// This is the return leg of the translation the server does on the way
// out (internal/api/slash_routine_catalog.go). It matters that it is a
// real conversion and not a pass-through: inputs reach a `code` step
// with their ORIGINAL types (the CEL runner exposes them as the `inputs`
// map for typed arithmetic), so a routine declaring
// `{"name":"limit","type":"integer"}` and evaluating `inputs.limit > 20`
// FAILS the run when 42 arrives as the string "42". Nothing catches it
// earlier — run-time input validation does not exist, so the declared
// type is honoured by whatever consumes the value and by nothing before
// it.
//
// Two rules beyond the type mapping:
//
//   - An empty value is OMITTED, never sent as "". The routine's own
//     default then applies, server-side, which is the only place that
//     knows what it is. Sending "" would override a default with a blank
//     — for msn-etn-podklady that turns "the previous month" into an
//     empty period.
//
//     A BOOLEAN is the exception and always sends. Its control in the
//     dashboard is a checkbox with two states, whose unticked value IS
//     the empty string, so treating that as "unset" would let a
//     `default: true` overrule somebody who had just unticked the box.
//     The repl matches the browser here rather than being subtly
//     stricter — the same command has to mean the same thing on both.
//
//   - A field the schema doesn't declare is passed through as a string.
//     The user typed it deliberately; the server is entitled to reject
//     an input the routine doesn't declare, and it says so better than a
//     silent client-side drop would.
func routineInputsFromValues(fields []ServerSlashField, values map[string]string) (map[string]any, error) {
	byName := make(map[string]ServerSlashField, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	out := make(map[string]any, len(values))
	for name, raw := range values {
		f, declared := byName[name]
		if raw == "" && f.ValueType != "boolean" {
			continue
		}
		if !declared {
			out[name] = raw
			continue
		}
		v, err := coerceRoutineInput(f.ValueType, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}

// coerceRoutineInput parses one form string into the JSON type the
// routine declared for it.
//
// An unknown or empty value_type yields the string unchanged: a catalog
// entry from a server newer than this build, or any field in the static
// catalog, is a string and always was.
func coerceRoutineInput(valueType, raw string) (any, error) {
	switch valueType {
	case "integer":
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a whole number", raw)
		}
		return n, nil
	case "number":
		n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		// ParseFloat accepts "NaN", "Inf" and "-Inf". None of them
		// survive json.Marshal, so letting them through here trades a
		// clear message at the prompt for an opaque marshalling failure
		// at POST time — and the browser rejects them (Number.isFinite),
		// so accepting them would also be a divergence.
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, fmt.Errorf("%q is not a number", raw)
		}
		return n, nil
	case "boolean":
		return parseSlashBool(raw)
	case "array", "object":
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, fmt.Errorf("%q is not valid JSON", raw)
		}
		// Unmarshal accepts any JSON document, so a `42` typed into a
		// field declared as an object parses fine and then reaches the
		// routine as a number, where it fails — or worse, quietly does
		// not. Check the shape here, where the error can name the field
		// the user is looking at.
		switch valueType {
		case "array":
			if _, ok := v.([]any); !ok {
				return nil, fmt.Errorf("%q is valid JSON but not an array — try [\"a\",\"b\"]", raw)
			}
		case "object":
			if _, ok := v.(map[string]any); !ok {
				return nil, fmt.Errorf("%q is valid JSON but not an object — try {\"key\":\"value\"}", raw)
			}
		}
		return v, nil
	default:
		return raw, nil
	}
}

// parseSlashBool is the boolean vocabulary, spelled once.
//
// It is deliberately NOT strconv.ParseBool. That function accepts "t",
// "T" and "TRUE" and rejects "yes" and "on"; lib/routine-inputs.ts
// accepts "yes"/"on"/"off" and rejects "t". Left alone, the two clients
// disagreed about the same routine — `/pack dry=yes` worked in chat and
// errored in the repl, `/pack dry=t` the other way round — while the
// comment on routineInputsFromValues claimed they matched. A user is
// told one command; it has to mean one thing.
//
// "" is what an unticked checkbox sends and means false: a checkbox has
// no third state to mean "leave this to the routine's default" with.
func parseSlashBool(raw string) (any, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off", "":
		return false, nil
	default:
		return nil, fmt.Errorf("%q is not true or false", raw)
	}
}
