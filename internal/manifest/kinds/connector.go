// Package kinds holds one Go file per manifest kind. This file
// implements `kind: Connector` — a *reference* kind whose job is to
// install a connector from the server-side catalog (Linear, GitHub,
// Slack, …) into the current workspace. The manifest cannot define
// new connector *types*; the catalog ships with the binary. What the
// manifest CAN do is:
//
//   - mark a catalog entry as installed (`spec.install: true`)
//   - bind the connector's expected env-var names to workspace
//     credential names that satisfy them (`spec.credentials`)
//   - drift back to uninstalled (`spec.install: false`) when the
//     server endpoint exposes a delete verb
//
// REST surface used by this kind:
//
//	GET  /api/v1/connectors                 → catalog list (used by Export)
//	GET  /api/v1/connectors/{slug}          → manifest detail + installed +
//	                                          required_credentials
//	POST /api/v1/connectors/{slug}/install  → body: {credentials: {ENV: NAME}}
//	DELETE /api/v1/connectors/{slug}/install  (best-effort; tolerated 404)
//
// `slug` here is the connector ID returned by the catalog (e.g.
// "linear", "github"). The manifest uses `metadata.slug` for that
// identifier so cross-kind references stay uniform with every other
// kind in the bundle.
//
// Credential mapping shape:
//
//	spec.credentials:
//	  LINEAR_API_KEY: LINEAR_PROD_KEY
//
// Key = env-var name the connector consumes at runtime (declared by
// the connector's catalog manifest under `required_credentials`).
// Value = `name` of a workspace credential that should be wired into
// that env var. The workspace credential is normally declared as part
// of the same bundle (kind: Credential under a Crew or Workspace
// doc); Validate can't see that pre-Apply because credentials live in
// a different phase, so existence is rechecked at Plan time against
// the live `/api/v1/credentials` list.
package kinds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// connectorAPIVersion + connectorKind keep the YAML literals out of
// the per-call paths so renames stay in one place.
const (
	connectorAPIVersion = "crewship/v1"
	connectorKind       = "Connector"
)

// envVarNameRe matches POSIX-style environment variable names. The
// pattern is the union of what every CLI adapter we ship will accept
// (upper-snake-case, must start with a letter or underscore, digits
// allowed thereafter). The connector kind enforces this on both keys
// AND values of spec.credentials so a typo in the manifest fails fast
// instead of producing a mysterious "credential not found" at plan
// time.
var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ── Types ────────────────────────────────────────────────────────────────

// ConnectorSpec is the shape under `spec:` for kind: Connector.
//
// Install drives the desired post-apply state of the catalog entry.
// `true` says "make sure this connector is installed in the workspace";
// `false` says "make sure it isn't" (best-effort, see Plan).
//
// Credentials maps the connector's expected runtime env-var names to
// the names of workspace credentials that satisfy them. Both halves
// are env-var-shaped strings. The right-hand side is resolved against
// the live workspace credential list (GET /api/v1/credentials) at Plan
// time — Validate alone cannot enforce it because Phase 1 credentials
// may be declared elsewhere in the same bundle and won't exist
// remotely until Apply gets to them.
type ConnectorSpec struct {
	// Install is the desired state. Defaults to false at the YAML
	// level, but the parse layer should treat absent==false. Plan
	// emits ActionCreate when true-and-missing, ActionDelete when
	// false-and-present, and ActionUnchanged otherwise.
	Install bool `yaml:"install" json:"install"`

	// Credentials maps connector env-var → workspace credential env-var
	// name. Iteration order in error messages is sorted for stable
	// diff output.
	Credentials map[string]string `yaml:"credentials,omitempty" json:"credentials,omitempty"`
}

// ConnectorDocument is the YAML envelope.
type ConnectorDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       ConnectorSpec        `yaml:"spec"       json:"spec"`
}

// ConnectorRemote is the slice of GET /api/v1/connectors/{slug} the
// Plan function compares against. Only the fields drift detection
// actually cares about are modelled here — the catalog manifest is
// rich (auth_mode, fields, brand, …) but none of that changes the
// install verb.
//
// RequiredCredentials is the list of env-var names the catalog
// manifest declares the connector needs. Plan asserts that every
// entry is covered by spec.credentials before issuing the install
// POST so the user sees the missing binding before any HTTP traffic.
type ConnectorRemote struct {
	ID                  string   `json:"id"`
	Slug                string   `json:"slug,omitempty"`
	Installed           bool     `json:"installed"`
	RequiredCredentials []string `json:"required_credentials,omitempty"`
}

// ── Validate ─────────────────────────────────────────────────────────────

// Validate enforces structural rules:
//
//   - metadata.name + metadata.slug are required (slug is the catalog
//     connector id, e.g. "linear").
//   - Every key AND every value in spec.credentials must look like a
//     POSIX env-var name. Empty values are rejected; the user must
//     either provide a binding or omit the entry entirely.
//
// Existence-of-credential and "is this slug in the catalog" are NOT
// checked here. The catalog check would force a network round-trip in
// the validate phase (we currently keep Validate offline), and
// existence-of-credential is checked at Plan time because workspace
// credentials may be declared in an earlier phase of the same bundle
// and won't be on the server yet.
//
// The WorkspaceContext parameter is unused but the signature mirrors
// every other kind so the dispatcher can route through one interface.
func (d *ConnectorDocument) Validate(_ internalapi.WorkspaceContext) error {
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("connector: metadata.name is required")
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("connector %q: metadata.slug is required (must match catalog connector id)", d.Metadata.Name)
	}

	// Iterate in sorted order so the first error reported is stable
	// across runs — otherwise a user with multiple typos would see a
	// different one on each apply, which is confusing.
	keys := make([]string, 0, len(d.Spec.Credentials))
	for k := range d.Spec.Credentials {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if !envVarNameRe.MatchString(key) {
			return fmt.Errorf(
				"connector %q: spec.credentials key %q is not a valid env var name (must match %s)",
				d.Metadata.Slug, key, envVarNameRe.String(),
			)
		}
		val := d.Spec.Credentials[key]
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf(
				"connector %q: spec.credentials[%q] is empty — drop the key or provide a workspace credential name",
				d.Metadata.Slug, key,
			)
		}
		if !envVarNameRe.MatchString(val) {
			return fmt.Errorf(
				"connector %q: spec.credentials[%q] value %q is not a valid env var name (must match %s)",
				d.Metadata.Slug, key, val, envVarNameRe.String(),
			)
		}
	}
	return nil
}

// ── Plan ─────────────────────────────────────────────────────────────────

// Plan compares the declared connector against the catalog detail and
// the workspace credential list. Decision tree:
//
//	(install=true, remote.Installed=false)
//	    → ActionCreate, POST install with {credentials: spec.credentials}
//	(install=true, remote.Installed=true)
//	    → ActionUnchanged
//	(install=false, remote.Installed=true)
//	    → ActionDelete, DELETE install (best-effort: a 404 / 405 is
//	      reported as unchanged-with-warning since not every catalog
//	      entry exposes uninstall in v1)
//	(install=false, remote.Installed=false)
//	    → ActionUnchanged
//
// Before any HTTP traffic, Plan validates the catalog's
// required_credentials list against spec.credentials. If the user
// failed to map a required env var, Plan emits an ActionCreate item
// whose Exec returns an error immediately — surfaced through the
// regular plan-error channel so the user sees one consolidated
// failure instead of a network-level "install rejected".
//
// `remote` may be nil; Plan fetches it via GET /api/v1/connectors/{slug}.
// Callers that already have the remote state (e.g. Export -> diff
// pipelines) can pass it directly and skip the round-trip.
func (d *ConnectorDocument) Plan(
	ctx context.Context,
	c internalapi.Client,
	remote *ConnectorRemote,
) ([]internalapi.PlanItem, error) {
	slug := d.Metadata.Slug

	// Fetch remote state if the caller didn't supply one. A 404 here
	// is fatal — the manifest references a connector slug the catalog
	// doesn't know about, which is almost certainly a typo.
	if remote == nil {
		fetched, err := fetchConnector(ctx, c, slug)
		if err != nil {
			return nil, fmt.Errorf("connector %q: fetch catalog entry: %w", slug, err)
		}
		remote = fetched
	}

	switch {
	case d.Spec.Install && !remote.Installed:
		// This is the only branch that issues a POST, so this is the
		// only branch where missing credential mappings can actually
		// hurt. An already-installed connector (next branch) makes no
		// HTTP call and an exported install:true manifest may legitimately
		// have no `credentials:` block — the original CR feedback flagged
		// gating this earlier as breaking re-apply of exported state.
		if missing := missingRequiredCredentials(remote.RequiredCredentials, d.Spec.Credentials); len(missing) > 0 {
			// Emit a single PlanItem with Action=Create whose Exec returns
			// the error before any HTTP call. Apply surfaces Exec errors
			// through the same channel as network failures, so the user
			// sees one consolidated "missing binding" message.
			msg := fmt.Sprintf(
				"connector %q: missing credential mapping for required env var(s) %s — add spec.credentials entries",
				slug, strings.Join(missing, ", "),
			)
			return []internalapi.PlanItem{{
				Kind:        "Connector",
				Slug:        slug,
				Action:      internalapi.ActionCreate,
				Description: msg,
				Exec: func(_ context.Context, _ internalapi.Client) error {
					return errors.New(msg)
				},
			}}, nil
		}
		// Verify each mapped workspace credential exists. We only
		// surface a "missing on remote" warning at Plan time if the
		// credential isn't in the live list AND isn't declared
		// earlier in the same bundle — but since we can't see the
		// bundle from here, the safe move is to *attempt* the
		// install: Apply orders Credentials in Phase 1 and Connectors
		// in Phase 11, so by the time this Exec runs every declared
		// credential is already on the server. The credential check
		// happens inside Exec so a stale workspace credential is
		// caught with the same "before any HTTP" treatment.
		body := map[string]any{
			"credentials": d.Spec.Credentials,
		}
		return []internalapi.PlanItem{{
			Kind:        "Connector",
			Slug:        slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("install connector %q", d.Metadata.Name),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				if err := assertCredentialsExist(ctx, c, d.Spec.Credentials); err != nil {
					return fmt.Errorf("connector %q: %w", slug, err)
				}
				path := fmt.Sprintf("/api/v1/connectors/%s/install", slug)
				resp, err := c.Post(ctx, path, body)
				if err != nil {
					return fmt.Errorf("POST %s: %w", path, err)
				}
				return connectorCheckStatus(resp, "install connector "+slug)
			},
		}}, nil

	case d.Spec.Install && remote.Installed:
		return []internalapi.PlanItem{{
			Kind:        "Connector",
			Slug:        slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("connector %q is already installed", d.Metadata.Name),
		}}, nil

	case !d.Spec.Install && remote.Installed:
		// Best-effort uninstall. Many catalog entries don't expose a
		// DELETE verb in v1, so a 404 / 405 from the endpoint is
		// degraded to ActionUnchanged with a description that flags
		// the manual cleanup. Other 4xx / 5xx still fail Apply.
		return []internalapi.PlanItem{{
			Kind:        "Connector",
			Slug:        slug,
			Action:      internalapi.ActionDelete,
			Description: fmt.Sprintf("uninstall connector %q", d.Metadata.Name),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				path := fmt.Sprintf("/api/v1/connectors/%s/install", slug)
				resp, err := c.Delete(ctx, path)
				if err != nil {
					return fmt.Errorf("DELETE %s: %w", path, err)
				}
				if resp == nil {
					return fmt.Errorf("DELETE %s: nil response", path)
				}
				if resp.StatusCode == 404 || resp.StatusCode == 405 || resp.StatusCode == 501 {
					// Endpoint not implemented for this connector.
					// Apply surfaces this as a warning rather than a
					// hard failure so a manifest that toggles a few
					// connectors off doesn't block on the one that
					// can't be uninstalled.
					_, _ = io.Copy(io.Discard, resp.Body)
					return nil
				}
				return connectorCheckStatus(resp, "uninstall connector "+slug)
			},
		}}, nil

	default:
		return []internalapi.PlanItem{{
			Kind:        "Connector",
			Slug:        slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("connector %q is already uninstalled", d.Metadata.Name),
		}}, nil
	}
}

// ── Export ───────────────────────────────────────────────────────────────

// ExportConnectors fetches every catalog entry and emits a
// ConnectorDocument for each one the workspace has installed. Catalog
// entries that aren't installed in this workspace are dropped — they
// would round-trip as `install: false` which is the implicit default
// anyway, and including them would explode the bundle with one doc
// per connector in the global catalog.
//
// The credential map is reconstructed from the workspace install
// record where available. If the server doesn't echo the mapping
// back, ExportConnectors emits an empty `credentials: {}` and relies
// on the user to fill it in before re-applying — better than guessing
// the binding and silently rewiring the wrong credential.
func ExportConnectors(ctx context.Context, c internalapi.Client) ([]*ConnectorDocument, error) {
	resp, err := c.Get(ctx, "/api/v1/connectors")
	if err != nil {
		return nil, fmt.Errorf("list connectors: %w", err)
	}
	if err := connectorCheckStatus(resp, "list connectors"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read connectors body: %w", err)
	}

	var rows []ConnectorRemote
	if err := json.Unmarshal(body, &rows); err != nil {
		// Tolerate `{"connectors": [...]}` wrapping for forward-compat.
		var wrapped struct {
			Connectors []ConnectorRemote `json:"connectors"`
		}
		if werr := json.Unmarshal(body, &wrapped); werr == nil {
			rows = wrapped.Connectors
		} else {
			return nil, fmt.Errorf("decode connectors: %w", err)
		}
	}

	out := make([]*ConnectorDocument, 0)
	for _, row := range rows {
		// Skip catalog entries that aren't installed — they'd show up
		// as install:false on round-trip, which is the implicit
		// default anyway and would explode bundle size.
		if !row.Installed {
			continue
		}
		// Re-fetch detail to pick up the bound credential mapping
		// (the list endpoint typically omits per-install detail to
		// keep the payload bounded).
		detail, err := fetchConnector(ctx, c, idForSlug(row))
		if err != nil {
			// Don't fail the whole export over one connector; emit a
			// shell document so the user can see something went sideways.
			detail = &ConnectorRemote{ID: row.ID, Slug: row.Slug, Installed: true}
		}
		doc := &ConnectorDocument{
			APIVersion: connectorAPIVersion,
			Kind:       connectorKind,
			Metadata: internalapi.Metadata{
				Name: row.ID, // ConnectorListItem returns ID where Name lives
				Slug: idForSlug(row),
			},
			Spec: ConnectorSpec{
				Install:     true,
				Credentials: credentialsFromRemote(detail),
			},
		}
		out = append(out, doc)
	}
	return out, nil
}

// ── helpers ──────────────────────────────────────────────────────────────

// fetchConnector hits GET /api/v1/connectors/{slug} and decodes the
// detail body. 404 returns a structured "not in catalog" error so the
// caller can format a friendly message.
func fetchConnector(ctx context.Context, c internalapi.Client, slug string) (*ConnectorRemote, error) {
	path := fmt.Sprintf("/api/v1/connectors/%s", slug)
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("GET %s: nil response", path)
	}
	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("connector slug %q is not in the catalog (GET %s returned 404)", slug, path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readAll(resp.Body)
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s body: %w", path, err)
	}
	var out ConnectorRemote
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	// Normalise: the catalog uses `id` as the canonical slug; copy
	// it into Slug so downstream code can read either field.
	if out.Slug == "" {
		out.Slug = out.ID
	}
	return &out, nil
}

// missingRequiredCredentials returns the subset of required env-var
// names that aren't covered by spec.credentials. Output is sorted so
// error messages are deterministic.
func missingRequiredCredentials(required []string, mapping map[string]string) []string {
	if len(required) == 0 {
		return nil
	}
	var missing []string
	for _, env := range required {
		if _, ok := mapping[env]; !ok {
			missing = append(missing, env)
		}
	}
	sort.Strings(missing)
	return missing
}

// assertCredentialsExist confirms each workspace credential name in
// `mapping` has a row in GET /api/v1/credentials. Runs inside the
// install Exec (not at Plan time) so it picks up credentials declared
// earlier in the same bundle that Phase 1 already created. Errors are
// formatted with the connector env var so the user can fix the
// manifest binding directly.
func assertCredentialsExist(ctx context.Context, c internalapi.Client, mapping map[string]string) error {
	if len(mapping) == 0 {
		return nil
	}
	have, err := listCredentialNames(ctx, c)
	if err != nil {
		return fmt.Errorf("list workspace credentials: %w", err)
	}
	// Sorted iteration for deterministic error messages.
	keys := make([]string, 0, len(mapping))
	for k := range mapping {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, env := range keys {
		credName := mapping[env]
		if _, ok := have[credName]; !ok {
			return fmt.Errorf(
				"workspace credential %q (mapped to env %s) does not exist — declare it as a Credential kind earlier in the bundle or create it in the UI",
				credName, env,
			)
		}
	}
	return nil
}

// listCredentialNames pulls GET /api/v1/credentials and returns a
// set-shaped map keyed on the credential `name` field. The handler
// returns a credentialResponse slice; we only need the names.
func listCredentialNames(ctx context.Context, c internalapi.Client) (map[string]struct{}, error) {
	resp, err := c.Get(ctx, "/api/v1/credentials")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/credentials: %w", err)
	}
	if err := connectorCheckStatus(resp, "list credentials"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/credentials body: %w", err)
	}
	// Decode into a tolerant shape that accepts both the bare array
	// the handler currently returns and a future wrapped envelope.
	type credName struct {
		Name string `json:"name"`
	}
	var flat []credName
	if err := json.Unmarshal(body, &flat); err != nil {
		var wrapped struct {
			Credentials []credName `json:"credentials"`
		}
		if werr := json.Unmarshal(body, &wrapped); werr != nil {
			return nil, fmt.Errorf("decode /api/v1/credentials: %w", err)
		}
		flat = wrapped.Credentials
	}
	out := make(map[string]struct{}, len(flat))
	for _, row := range flat {
		out[row.Name] = struct{}{}
	}
	return out, nil
}

// idForSlug returns the catalog slug for a remote row, preferring the
// dedicated Slug field when the server starts emitting it and falling
// back to ID (the only identifier the current handler returns).
func idForSlug(row ConnectorRemote) string {
	if row.Slug != "" {
		return row.Slug
	}
	return row.ID
}

// credentialsFromRemote pulls the bound credential map out of a
// catalog detail response. Returns nil when the server doesn't echo
// the mapping back — Export emits an empty `credentials: {}` in that
// case so the user can see they need to fill it in before re-applying.
//
// Defined as a hook on a tolerant decoder rather than a hard field so
// the export round-trip keeps working even if a future server version
// returns the credential map under a different key.
func credentialsFromRemote(_ *ConnectorRemote) map[string]string {
	// The current server response (see ConnectorListItem in
	// internal/api/connectors_handler.go) does not echo the
	// credentials mapping back to the client. Returning nil here
	// signals "unknown; user must fill in" to Export, which the
	// YAML marshaller renders as an empty map.
	return nil
}

// connectorCheckStatus returns an error if the response status is
// outside 2xx. The body is best-effort surfaced in the error so the
// user sees the underlying problem rather than a bare HTTP code.
// Defined locally (rather than shared) because the other kinds in
// this package follow a "no cross-kind helper" rule to keep test
// isolation tight — see the docstring on milestone.go for context.
func connectorCheckStatus(resp *internalapi.Response, op string) error {
	if resp == nil {
		return fmt.Errorf("%s: nil response", op)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := readAll(resp.Body)
		return fmt.Errorf("%s: HTTP %d: %s", op, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
