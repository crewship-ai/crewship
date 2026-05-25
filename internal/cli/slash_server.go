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
	"net/http"
	"net/url"
	"os"
	"regexp"
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
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Default  string `json:"default,omitempty"`
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
	for _, cmd := range cmds {
		cmd := cmd // capture
		repl.Register(cmd.ID, buildSlashHandler(cmd, client))
	}
	return len(cmds)
}

// buildSlashHandler returns the REPLHandler that parses the user's
// args, builds the JSON body via slashCommandPayload, and POSTs to
// the matching public endpoint. Errors are surfaced inline so the
// user sees them right after the prompt.
func buildSlashHandler(cmd ServerSlashCommand, client SlashHTTPClient) REPLHandler {
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
				return true, fmt.Errorf("required field %q is missing — try /%s %s=<value> …", f.Name, cmd.ID, f.Name)
			}
		}
		// Apply defaults for unspecified optional fields.
		for _, f := range cmd.FormSchema {
			if _, ok := values[f.Name]; !ok && f.Default != "" {
				values[f.Name] = f.Default
			}
		}
		body := slashCommandPayload(cmd.ID, values)
		endpoint, err := slashCommandEndpoint(cmd.ID, client.GetWorkspaceID())
		if err != nil {
			return true, err
		}
		resp, err := client.Post(endpoint, body)
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 400 {
			return true, fmt.Errorf("/%s failed: %s — %s", cmd.ID, resp.Status, string(out))
		}
		// Success — surface a short confirmation. The user is in a
		// chat REPL; a long JSON dump would clutter the conversation.
		fmt.Fprintf(os.Stdout, "[/%s] ✓\n", cmd.ID)
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
func parseKeyValueArgs(args []string) (map[string]string, error) {
	if len(args) == 0 {
		return map[string]string{}, nil
	}
	joined := strings.Join(args, " ")
	out := map[string]string{}
	matches := keyValuePattern.FindAllStringSubmatch(joined, -1)
	if len(matches) == 0 && strings.TrimSpace(joined) != "" {
		return nil, fmt.Errorf("could not parse args — use key=value or key=\"quoted value\" form")
	}
	for _, m := range matches {
		key := m[1]
		val := m[2]
		if val == "" {
			val = m[3]
		}
		out[key] = val
	}
	return out, nil
}

// slashCommandEndpoint maps slash command id → public API endpoint.
// Mirror of components/features/chat/composer/slash-action-modal.tsx
// (endpointForCommand). One-place-changes whenever a new slash
// command lands — keep these two in sync.
func slashCommandEndpoint(id, workspaceID string) (string, error) {
	ws := url.PathEscape(workspaceID)
	switch id {
	case "routine":
		return "/api/v1/workspaces/" + ws + "/pipeline-schedules", nil
	case "skill":
		return "/api/v1/workspaces/" + ws + "/skills/generate", nil
	case "credential":
		return "/api/v1/credentials?workspace_id=" + url.QueryEscape(workspaceID), nil
	case "issue":
		return "/api/v1/issues?workspace_id=" + url.QueryEscape(workspaceID), nil
	case "remember":
		return "/api/v1/memory/write?workspace_id=" + url.QueryEscape(workspaceID), nil
	default:
		return "", fmt.Errorf("unknown slash command id: %s", id)
	}
}

// slashCommandPayload reshapes the flat key=value map into the body
// shape the matching handler expects. Mirror of buildPayload in
// slash-action-modal.tsx.
func slashCommandPayload(id string, values map[string]string) any {
	switch id {
	case "routine":
		return map[string]any{
			"name":      values["name"],
			"cron_expr": values["cron"],
			"timezone":  values["timezone"],
		}
	case "skill":
		return map[string]any{
			"slug":   values["slug"],
			"prompt": values["prompt"],
		}
	case "credential":
		return map[string]any{
			"name":  values["name"],
			"type":  values["type"],
			"value": values["value"],
		}
	case "issue":
		return map[string]any{
			"title":       values["title"],
			"description": values["description"],
			"priority":    values["priority"],
		}
	case "remember":
		return map[string]any{
			"content": values["content"],
			"scope":   values["scope"],
		}
	default:
		// Fall through: pass the raw values map. The server will
		// 400 if the shape is wrong; better than fabricating a
		// payload for an action we don't know.
		out := make(map[string]any, len(values))
		for k, v := range values {
			out[k] = v
		}
		return out
	}
}
