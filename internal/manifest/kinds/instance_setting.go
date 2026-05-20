// Package kinds contains the per-kind document types, validation,
// plan, and export logic for SPEC-2 manifest kinds. Each kind lives
// in its own file. This file implements InstanceSetting.
//
// InstanceSetting is structurally unusual compared to the other
// SPEC-2 kinds: metadata.name and metadata.slug are advisory only
// (purely human-facing — they don't identify a server entity). The
// real identity lives field-level inside spec.settings, where each
// map entry corresponds to one row in the `app_settings` table.
// That's why Plan iterates spec.settings and emits one PlanItem per
// key, instead of one PlanItem per document.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// InstanceSettingSpec is the shape under `spec:` for kind:
// InstanceSetting. The single field is a flat map of key→value
// where each entry maps 1:1 to an `app_settings` row.
//
// Values may contain ${ENV_VAR} placeholders; those are resolved at
// Plan time against the process environment. A missing variable is
// a hard error — Plan refuses to emit silent empty-string writes.
type InstanceSettingSpec struct {
	Settings map[string]string `yaml:"settings" json:"settings"`
}

// InstanceSettingDocument is the top-level YAML envelope.
type InstanceSettingDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       InstanceSettingSpec  `yaml:"spec"       json:"spec"`
}

// InstanceSettingRemote is the full current server-side state, one
// entry per `app_settings` row. Plan compares each declared key
// against this map; ApplyReplace consults it to enumerate keys to
// delete.
//
// The remote value for sensitive keys is returned by the backend as
// the placeholder "***" — Plan treats that placeholder as "unknown"
// and will emit an Update PlanItem so the desired value is written
// (server-side idempotency keeps a no-op write cheap).
type InstanceSettingRemote map[string]string

// protectedInstanceKeys is the whitelist of keys ApplyReplace must
// never delete. These are written by the bootstrap path or by
// schema migrations and removing them would brick the instance. The
// list must stay in lockstep with the backend handler's protected-
// key list — the backend is the ultimate gatekeeper, but mirroring
// the list here lets Plan emit a clean "skipped (protected)" item
// instead of an opaque server error.
var protectedInstanceKeys = map[string]struct{}{
	"instance.bootstrap_at":  {},
	"instance.first_user_id": {},
	"schema.version":         {},
}

// IsProtectedInstanceKey reports whether the given key is on the
// protected whitelist. Exported so the apply layer (and tests) can
// share the single source of truth.
func IsProtectedInstanceKey(key string) bool {
	_, ok := protectedInstanceKeys[key]
	return ok
}

// sensitiveKeyPrefixes mirrors the backend handler's sensitive-key
// detection. The manifest layer only uses this for an opt-in
// authorial warning: declaring `smtp.password: hunter2` inline is
// almost always a mistake — the value will end up checked into
// source control. We don't block it (some legitimate workflows pipe
// the secret through sealed-secrets or similar), but Validate
// returns the slice of warnings so the CLI can surface them.
var sensitiveKeyPrefixes = []string{
	"smtp.password",
	"oauth.",
	"webhook.",
}

// sensitiveKeySuffixes catches keys that match the sensitive shape
// regardless of where in the dotted path the sensitive word lives
// (e.g. `oauth.github.client_secret` ends with `client_secret`).
var sensitiveKeySuffixes = []string{
	".password",
	".secret",
	".client_secret",
	".api_key",
	".token",
}

// envPlaceholder matches ${VAR_NAME}. The grammar is intentionally
// strict — only ASCII letters, digits and underscores, no defaults
// or fallbacks (`${X:-y}` is rejected). Keeping it strict means a
// typo like `${SMTP HOST}` fails loudly at Plan time instead of
// silently being treated as literal text.
var envPlaceholder = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// SensitiveValueWarning is returned by Validate when a value that
// looks sensitive (by key name) is supplied as a literal string
// rather than as a ${ENV_VAR} reference. The caller decides whether
// to surface it as a warning or a hard error — Validate itself
// never blocks on these.
type SensitiveValueWarning struct {
	Key     string
	Message string
}

// Validate checks structural rules for the document. No semantic
// rules apply to key/value content at validate time — the server
// owns key-name and value-format policy. The one author-facing
// signal Validate emits is a list of sensitive-key warnings so the
// CLI can prompt the user to rewrap literals as ${ENV_VAR}.
//
// workspaceCtx is unused: InstanceSetting has no cross-kind FKs.
// The argument is kept for signature uniformity with the other
// kinds so the wiring layer doesn't need a special case.
func (d *InstanceSettingDocument) Validate(workspaceCtx internalapi.WorkspaceContext) error {
	if d.APIVersion != "" && d.APIVersion != "crewship/v1" {
		return fmt.Errorf("InstanceSetting: unsupported apiVersion %q (want crewship/v1)", d.APIVersion)
	}
	if d.Kind != "" && d.Kind != "InstanceSetting" {
		return fmt.Errorf("InstanceSetting: unexpected kind %q", d.Kind)
	}
	if d.Spec.Settings == nil {
		// An empty manifest is legal in ApplyReplace mode (it means
		// "delete every non-protected key"). Don't error here.
		return nil
	}
	for key := range d.Spec.Settings {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("InstanceSetting: empty key in spec.settings")
		}
	}
	return nil
}

// Warnings inspects spec.settings for keys that look sensitive but
// carry a literal (non-${ENV}) value. The CLI presents these to the
// user; nothing blocks the apply. Returns nil when no warnings.
func (d *InstanceSettingDocument) Warnings() []SensitiveValueWarning {
	if len(d.Spec.Settings) == 0 {
		return nil
	}
	// Stable order so test assertions don't flake on map iteration.
	keys := make([]string, 0, len(d.Spec.Settings))
	for k := range d.Spec.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []SensitiveValueWarning
	for _, k := range keys {
		v := d.Spec.Settings[k]
		if !looksSensitive(k) {
			continue
		}
		if isEnvOnlyReference(v) {
			continue
		}
		out = append(out, SensitiveValueWarning{
			Key:     k,
			Message: fmt.Sprintf("key %q looks sensitive; prefer ${ENV_VAR} interpolation over a literal value", k),
		})
	}
	return out
}

func looksSensitive(key string) bool {
	for _, p := range sensitiveKeyPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	for _, s := range sensitiveKeySuffixes {
		if strings.HasSuffix(key, s) {
			return true
		}
	}
	return false
}

// isEnvOnlyReference reports whether v is exactly one ${VAR}
// placeholder with no surrounding characters. We only suppress the
// sensitive-value warning for "pure" references — a value like
// "prefix-${X}" still gets the warning because part of it is
// literal.
func isEnvOnlyReference(v string) bool {
	v = strings.TrimSpace(v)
	loc := envPlaceholder.FindStringIndex(v)
	if loc == nil {
		return false
	}
	return loc[0] == 0 && loc[1] == len(v)
}

// resolveEnv replaces every ${VAR} in v with os.LookupEnv(VAR).
// Returns an error if any referenced variable is unset — Plan never
// silently writes an empty string in place of a missing secret.
//
// lookup is injectable for tests; pass nil to use os.LookupEnv. A
// future refactor will surface the lookup through internalapi.Client
// (so the manifest package can centralise it for all kinds) — until
// then this is the only env-aware kind.
func resolveEnv(v string, lookup func(string) (string, bool)) (string, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if !strings.Contains(v, "${") {
		return v, nil
	}
	// Pre-flight: every literal `${` opener in the input must be the
	// start of a well-formed placeholder. The previous implementation
	// silently passed `${X:-y}` / `${BAD NAME}` / unbalanced `${X`
	// through because ReplaceAllStringFunc only fires on matches —
	// the loud-failure contract documented at the top of the file
	// required this explicit pass.
	rest := v
	for {
		i := strings.Index(rest, "${")
		if i < 0 {
			break
		}
		// Anchor envPlaceholder at the opener position.
		loc := envPlaceholder.FindStringIndex(rest[i:])
		if loc == nil || loc[0] != 0 {
			snippet := rest[i:]
			if end := strings.Index(snippet, "}"); end >= 0 && end <= 32 {
				snippet = snippet[:end+1]
			} else if len(snippet) > 32 {
				snippet = snippet[:32] + "…"
			}
			return "", fmt.Errorf("InstanceSetting value contains malformed placeholder %q — only ${ENV_VAR_NAME} is supported (no `${VAR:-default}`, no spaces, no nested braces)", snippet)
		}
		rest = rest[i+loc[1]:]
	}
	var firstErr error
	out := envPlaceholder.ReplaceAllStringFunc(v, func(match string) string {
		if firstErr != nil {
			return match
		}
		// match is "${X}"; strip the wrapper.
		name := match[2 : len(match)-1]
		val, ok := lookup(name)
		if !ok {
			firstErr = fmt.Errorf("environment variable %q referenced in InstanceSetting value is not set", name)
			return match
		}
		return val
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// PlanInstanceSettingsOptions tunes Plan behaviour for ApplyReplace
// and dependency injection in tests. The standard per-kind Plan
// signature in SPEC-2 doesn't carry mode info; InstanceSetting needs
// it because ApplyReplace prunes remote-only keys. Keeping the
// extra dimension in a struct (instead of a positional bool) leaves
// room for additional knobs without breaking callers.
type PlanInstanceSettingsOptions struct {
	// Replace = true means ApplyReplace mode: emit Delete PlanItems
	// for every key present remotely but absent from spec.settings,
	// skipping protected keys.
	Replace bool

	// EnvLookup overrides os.LookupEnv for ${ENV} interpolation.
	// Tests use this to avoid mutating the process environment. nil
	// falls back to os.LookupEnv.
	EnvLookup func(string) (string, bool)
}

// Plan compares the document against the server's current state
// and returns the ordered list of PlanItems Apply will execute.
//
// For each key in spec.settings:
//   - Resolve ${ENV_VAR} placeholders. Missing env vars → error.
//   - Look up the current remote value. Different (or missing) →
//     emit an Update PlanItem that PUTs the resolved value.
//   - Identical → emit an Unchanged PlanItem.
//
// In ApplyReplace mode, additionally enumerate every key in remote
// that is NOT in spec.settings. Skip protected keys (mirroring the
// backend whitelist) and emit a Delete PlanItem for the rest.
//
// `remote` may be nil; passing nil makes Plan fetch the full
// settings list itself via GET /api/v1/instance/settings. Passing a
// pre-fetched map (the normal path from BuildPlan) avoids the extra
// round-trip when several kinds share the same client cache.
func (d *InstanceSettingDocument) Plan(
	ctx context.Context,
	c internalapi.Client,
	remote *InstanceSettingRemote,
	opts PlanInstanceSettingsOptions,
) ([]internalapi.PlanItem, error) {
	// Fetch remote if not supplied. The lazy path is used by `apply
	// --file foo.yaml` where only this kind is declared; the eager
	// path is used when a full bundle has already populated a cache.
	if remote == nil {
		fetched, err := fetchInstanceSettings(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("InstanceSetting: fetch remote state: %w", err)
		}
		remote = &fetched
	}

	// Stable iteration order over the declared map keeps plan output
	// deterministic. Plan output is what users see in `apply
	// --dry-run`; non-deterministic ordering would make diffs noisy.
	declaredKeys := make([]string, 0, len(d.Spec.Settings))
	for k := range d.Spec.Settings {
		declaredKeys = append(declaredKeys, k)
	}
	sort.Strings(declaredKeys)

	items := make([]internalapi.PlanItem, 0, len(declaredKeys))
	for _, key := range declaredKeys {
		rawValue := d.Spec.Settings[key]
		resolvedValue, err := resolveEnv(rawValue, opts.EnvLookup)
		if err != nil {
			return nil, fmt.Errorf("InstanceSetting key %q: %w", key, err)
		}

		currentValue, exists := (*remote)[key]
		// The backend masks sensitive values as "***" on read; treat
		// that as "unknown" and always emit an update. The server's
		// PUT handler is idempotent so a redundant write of the same
		// value is cheap.
		looksMasked := exists && currentValue == "***"

		if exists && !looksMasked && currentValue == resolvedValue {
			items = append(items, internalapi.PlanItem{
				Kind:        "InstanceSetting",
				Slug:        key,
				Action:      internalapi.ActionUnchanged,
				Description: fmt.Sprintf("instance setting %q already matches", key),
			})
			continue
		}

		// Capture loop vars for the closure.
		k, v := key, resolvedValue
		items = append(items, internalapi.PlanItem{
			Kind:        "InstanceSetting",
			Slug:        k,
			Action:      internalapi.ActionUpdate,
			Description: fmt.Sprintf("set instance setting %q", k),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				return putInstanceSetting(ctx, c, k, v)
			},
		})
	}

	if opts.Replace {
		// Prune: any remote key not in spec.settings becomes a
		// delete — unless it's protected. Sorting again keeps
		// deletion order deterministic.
		remoteKeys := make([]string, 0, len(*remote))
		for k := range *remote {
			remoteKeys = append(remoteKeys, k)
		}
		sort.Strings(remoteKeys)

		declaredSet := make(map[string]struct{}, len(declaredKeys))
		for _, k := range declaredKeys {
			declaredSet[k] = struct{}{}
		}

		for _, key := range remoteKeys {
			if _, declared := declaredSet[key]; declared {
				continue
			}
			if IsProtectedInstanceKey(key) {
				// Protected keys aren't silently swallowed — surface
				// them as Unchanged so the user sees the skip in
				// dry-run output and can't accuse the tool of
				// hiding work.
				items = append(items, internalapi.PlanItem{
					Kind:        "InstanceSetting",
					Slug:        key,
					Action:      internalapi.ActionUnchanged,
					Description: fmt.Sprintf("instance setting %q is protected; skipped in ApplyReplace", key),
				})
				continue
			}
			k := key
			items = append(items, internalapi.PlanItem{
				Kind:        "InstanceSetting",
				Slug:        k,
				Action:      internalapi.ActionDelete,
				Description: fmt.Sprintf("delete instance setting %q (not declared in manifest)", k),
				Exec: func(ctx context.Context, c internalapi.Client) error {
					return deleteInstanceSetting(ctx, c, k)
				},
			})
		}
	}

	return items, nil
}

// ExportInstanceSettings reads every key from the server and folds
// them into a single InstanceSettingDocument. Sensitive values come
// back as "***" placeholders — we keep them in the export because
// dropping them would round-trip wrong (a re-apply would emit a
// delete for the omitted key in ApplyReplace mode). Users are
// expected to hand-edit the export to replace "***" with the
// appropriate ${ENV_VAR} reference before checking it in.
func ExportInstanceSettings(ctx context.Context, c internalapi.Client) ([]*InstanceSettingDocument, error) {
	remote, err := fetchInstanceSettings(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("InstanceSetting: export fetch: %w", err)
	}
	if len(remote) == 0 {
		// No rows → no document. Returning an empty doc would
		// produce a useless `spec: {settings: {}}` blob in
		// `crewship export`; nil keeps the export clean.
		return nil, nil
	}
	settings := make(map[string]string, len(remote))
	for k, v := range remote {
		settings[k] = v
	}
	doc := &InstanceSettingDocument{
		APIVersion: "crewship/v1",
		Kind:       "InstanceSetting",
		Metadata: internalapi.Metadata{
			Name: "Instance settings",
			Slug: "instance-settings",
			Description: "Exported from " + c.WorkspaceID() +
				" — sensitive values are masked as ***; replace with ${ENV_VAR} before applying.",
		},
		Spec: InstanceSettingSpec{Settings: settings},
	}
	return []*InstanceSettingDocument{doc}, nil
}

// ---------- HTTP helpers ----------

// fetchInstanceSettings does the GET /api/v1/instance/settings call
// and decodes the response. The handler ships either a flat
// map[string]string body or a {settings: {...}} wrapper depending
// on which iteration of the handler is deployed; decode both.
func fetchInstanceSettings(ctx context.Context, c internalapi.Client) (InstanceSettingRemote, error) {
	resp, err := c.Get(ctx, "/api/v1/instance/settings")
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return InstanceSettingRemote{}, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		// Handler not yet deployed; treat as empty rather than a
		// hard failure so dry-run still produces a useful plan.
		return InstanceSettingRemote{}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body := readErrBody(resp.Body)
		return nil, fmt.Errorf("GET /api/v1/instance/settings: status %d: %s", resp.StatusCode, body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(data) == 0 {
		return InstanceSettingRemote{}, nil
	}
	// Try the flat map shape first.
	var flat map[string]string
	if err := json.Unmarshal(data, &flat); err == nil && flat != nil {
		return InstanceSettingRemote(flat), nil
	}
	// Fall back to {settings: {...}} wrapper.
	var wrapped struct {
		Settings map[string]string `json:"settings"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.Settings != nil {
		return InstanceSettingRemote(wrapped.Settings), nil
	}
	// Fall back to array-of-rows shape: [{key, value}, ...].
	var rows []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &rows); err == nil {
		out := make(InstanceSettingRemote, len(rows))
		for _, r := range rows {
			out[r.Key] = r.Value
		}
		return out, nil
	}
	return nil, fmt.Errorf("decode instance settings: unknown response shape (first bytes: %q)", firstBytes(data, 80))
}

// putInstanceSetting upserts one key. The body shape mirrors the
// SPEC-2 contract: {"value": "..."}.
func putInstanceSetting(ctx context.Context, c internalapi.Client, key, value string) error {
	resp, err := c.Put(ctx, "/api/v1/instance/settings/"+url.PathEscape(key), map[string]any{"value": value})
	if err != nil {
		return fmt.Errorf("PUT instance setting %q: %w", key, err)
	}
	if resp == nil {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("PUT /api/v1/instance/settings/%s: status %d: %s",
			key, resp.StatusCode, readErrBody(resp.Body))
	}
	// Drain body to release the connection.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}

// deleteInstanceSetting removes one key. Used by ApplyReplace
// pruning. A 404 is a no-op (the key was deleted between Plan and
// Apply by another actor).
func deleteInstanceSetting(ctx context.Context, c internalapi.Client, key string) error {
	resp, err := c.Delete(ctx, "/api/v1/instance/settings/"+url.PathEscape(key))
	if err != nil {
		return fmt.Errorf("DELETE instance setting %q: %w", key, err)
	}
	if resp == nil {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("DELETE /api/v1/instance/settings/%s: status %d: %s",
			key, resp.StatusCode, readErrBody(resp.Body))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}

func readErrBody(r io.Reader) string {
	if r == nil {
		return ""
	}
	b, _ := io.ReadAll(io.LimitReader(r, 4<<10))
	return strings.TrimSpace(string(b))
}

func firstBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
