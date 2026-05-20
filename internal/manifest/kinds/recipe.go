// Package kinds — kind: Recipe.
//
// Recipes are an INSTALL-ONLY reference kind. The server ships a fixed
// catalog of recipes baked into the binary (internal/recipes); the
// manifest can request an install but cannot create or edit catalog
// entries. The contrast with Project / Label / Milestone (full CRUD)
// is intentional: a recipe is a curated 1-click bundle (crew + MCP
// servers + credentials) and authoring belongs in source code review,
// not in user YAML.
//
// REST surface used by this kind:
//
//	GET   /api/v1/recipes                  catalog list (Export + drift)
//	GET   /api/v1/recipes/{slug}           catalog row, optionally with
//	                                       an `installed: bool` field
//	POST  /api/v1/recipes/{slug}/install   install the bundle
//
// There is deliberately NO DELETE endpoint at the time of writing. The
// manifest still accepts `install: false` so the YAML schema is
// symmetrical; on encountering an already-installed recipe with
// `install: false` the planner emits ActionUnchanged carrying a
// description that warns the operator the uninstall could not be
// performed. The decision tree is documented in detail on Plan().
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// RecipeSpec is the shape under `spec:` for kind: Recipe.
//
// The schema is intentionally small: a recipe is identified by
// metadata.slug (which must match a catalog slug) and the spec
// expresses intent ("be installed") plus optional inputs that get
// forwarded verbatim to the install endpoint's request body.
type RecipeSpec struct {
	// Install is the declarative desire. true = ensure installed,
	// false = ensure uninstalled (best-effort, see Plan for caveats).
	Install bool `yaml:"install" json:"install"`

	// Inputs are recipe-specific knobs forwarded to the install
	// endpoint. Today the backend's installRecipeRequest accepts
	// `credential_values` and `account_labels`; tomorrow the recipe
	// catalog may introduce more. Keeping Inputs as a generic map
	// lets the manifest layer stay forward-compatible without a code
	// change every time the install body grows.
	Inputs map[string]any `yaml:"inputs,omitempty" json:"inputs,omitempty"`
}

// RecipeDocument is the YAML envelope.
type RecipeDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       RecipeSpec           `yaml:"spec"       json:"spec"`
}

// RecipeRemote is the server-side snapshot Plan compares against. The
// `installed` field is treated as best-effort: the current backend's
// GET /api/v1/recipes/{slug} returns only catalog metadata, but the
// SPEC-2 contract reserves the `installed` JSON key. Recipes with no
// `installed` key in the response decode as false, which lines up with
// "treat as not-yet-installed and let the install flow be a no-op if
// the server short-circuits duplicates."
type RecipeRemote struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Installed bool   `json:"installed"`
}

// uninstallWarning is the description shown on the no-op plan item
// when the operator declared install: false against an already
// installed recipe. Surfaced via PlanItem.Description so it lands in
// dry-run output and the apply summary; not an error because the spec
// is the user's intent and the server simply has no DELETE verb yet.
const uninstallWarning = "recipe uninstall not supported by server"

// Validate enforces structural rules.
//
// The recipe catalog lives on the server and is not in WorkspaceContext
// today, so we deliberately keep Validate trivial: missing slug, and a
// crew-slug back-reference when the optional inputs.crew_slug is
// present. The catalog membership check ("does this slug exist?") is
// deferred to Plan, which has live HTTP access via the Client.
func (d *RecipeDocument) Validate(workspaceCtx internalapi.WorkspaceContext) error {
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("recipe: metadata.slug is required")
	}
	if d.Spec.Inputs != nil {
		if v, ok := d.Spec.Inputs["crew_slug"]; ok {
			s, isStr := v.(string)
			if !isStr {
				return fmt.Errorf(
					"recipe %q: inputs.crew_slug must be a string, got %T",
					d.Metadata.Slug, v,
				)
			}
			s = strings.TrimSpace(s)
			if s != "" && !workspaceCtx.HasCrew(s) {
				return fmt.Errorf(
					"recipe %q: inputs.crew_slug %q does not reference any declared or remote crew",
					d.Metadata.Slug, s,
				)
			}
		}
	}
	return nil
}

// Plan compares declared vs remote state and emits a single PlanItem.
//
// Decision table:
//
//	install=true, !installed  → ActionCreate, POST install with inputs body
//	install=true,  installed  → ActionUnchanged
//	install=false, installed  → ActionUnchanged + uninstall-warning
//	                            description (no DELETE endpoint exists)
//	install=false,!installed  → ActionUnchanged
//
// The "best-effort uninstall" path emits ActionUnchanged rather than
// ActionDelete because Apply's destructive-confirmation prompt
// reserves Delete for verbs that will actually run. Returning Delete
// with a nil Exec would either be a silent no-op (lying about progress
// to the user) or trigger the destructive prompt for an action that
// will be skipped — both worse than a labelled Unchanged.
func (d *RecipeDocument) Plan(ctx context.Context, c internalapi.Client, remote *RecipeRemote) ([]internalapi.PlanItem, error) {
	if d.Spec.Install {
		if remote != nil && remote.Installed {
			return []internalapi.PlanItem{{
				Kind:        "Recipe",
				Slug:        d.Metadata.Slug,
				Action:      internalapi.ActionUnchanged,
				Description: fmt.Sprintf("recipe %q is already installed", d.Metadata.Slug),
			}}, nil
		}
		body := d.toInstallBody()
		slug := d.Metadata.Slug
		return []internalapi.PlanItem{{
			Kind:        "Recipe",
			Slug:        slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("install recipe %q", slug),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				path := fmt.Sprintf("/api/v1/recipes/%s/install", slug)
				resp, err := c.Post(ctx, path, body)
				if err != nil {
					return fmt.Errorf("POST %s: %w", path, err)
				}
				return recipeCheckStatus(resp, "install recipe "+slug)
			},
		}}, nil
	}

	// install=false — uninstall intent. No DELETE endpoint exists at
	// time of writing, so the planner records intent and warns.
	if remote != nil && remote.Installed {
		return []internalapi.PlanItem{{
			Kind:        "Recipe",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("recipe %q: %s", d.Metadata.Slug, uninstallWarning),
		}}, nil
	}
	return []internalapi.PlanItem{{
		Kind:        "Recipe",
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionUnchanged,
		Description: fmt.Sprintf("recipe %q is not installed", d.Metadata.Slug),
	}}, nil
}

// toInstallBody returns the POST body for /api/v1/recipes/{slug}/install.
// We forward d.Spec.Inputs verbatim. Today the backend expects keys
// like `credential_values` and `account_labels`; the manifest treats
// the inputs map as a passthrough so new keys work without recompile.
// nil inputs return an empty object, which the install handler tolerates
// (its readJSON unmarshals into a zero installRecipeRequest).
func (d *RecipeDocument) toInstallBody() map[string]any {
	if d.Spec.Inputs == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(d.Spec.Inputs))
	for k, v := range d.Spec.Inputs {
		out[k] = v
	}
	return out
}

// ExportRecipes returns one RecipeDocument per installed recipe in the
// workspace. Catalog entries that are not installed are skipped — the
// purpose of export is round-trip of state, and listing every
// uninstalled recipe in the catalog would bloat output with
// `install: false` no-ops for entries the user never touched.
//
// At time of writing the catalog response shape doesn't carry an
// installed flag, so this function decodes the list into RecipeRemote
// structs and filters by `installed == true`. Once the backend
// surfaces installed-state on the list endpoint this becomes a pure
// filter; until then `ExportRecipes` returns an empty slice in
// practice, which is correct ("nothing to round-trip yet").
func ExportRecipes(ctx context.Context, c internalapi.Client) ([]*RecipeDocument, error) {
	resp, err := c.Get(ctx, "/api/v1/recipes")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/recipes: %w", err)
	}
	if err := recipeCheckStatus(resp, "list recipes"); err != nil {
		return nil, err
	}
	body, err := recipeReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/recipes body: %w", err)
	}
	var rows []RecipeRemote
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode /api/v1/recipes: %w", err)
	}

	var out []*RecipeDocument
	for _, row := range rows {
		if !row.Installed {
			continue
		}
		out = append(out, &RecipeDocument{
			APIVersion: "crewship/v1",
			Kind:       "Recipe",
			Metadata: internalapi.Metadata{
				Name: row.Slug, // catalog rows have no separate human name on the list shape
				Slug: row.Slug,
			},
			Spec: RecipeSpec{
				Install: true,
			},
		})
	}
	return out, nil
}

// LookupRecipeRemote fetches the catalog entry for one declared
// recipe and returns the snapshot Plan diffs against. Returns nil + nil
// for 404 (recipe slug not in catalog) so the caller can surface a
// "no such recipe" plan error rather than a transport failure. The
// `installed` JSON key is optional in the response — absent means
// false.
func LookupRecipeRemote(ctx context.Context, c internalapi.Client, d *RecipeDocument) (*RecipeRemote, error) {
	path := fmt.Sprintf("/api/v1/recipes/%s", d.Metadata.Slug)
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if resp != nil && resp.StatusCode == 404 {
		return nil, nil
	}
	if err := recipeCheckStatus(resp, "get recipe "+d.Metadata.Slug); err != nil {
		return nil, err
	}
	body, err := recipeReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s body: %w", path, err)
	}
	var row RecipeRemote
	if err := json.Unmarshal(body, &row); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	// Ensure slug is set even if the response omits it — the catalog
	// handler returns the full Recipe struct which has slug, but
	// other potential implementations might leave it blank.
	if row.Slug == "" {
		row.Slug = d.Metadata.Slug
	}
	return &row, nil
}

// ── helpers ────────────────────────────────────────────────────────────────
//
// These helpers are intentionally kind-prefixed so that recipe.go is
// self-contained and survives whatever conventions other kinds in the
// same package settle on. Once the package agrees on shared helpers
// (e.g. via a util.go) these can collapse onto the shared names.

// recipeCheckStatus wraps non-2xx responses in an error decorated with
// the operation name. nil-resp safety lets test mocks return zero
// values without panicking.
func recipeCheckStatus(resp *internalapi.Response, op string) error {
	if resp == nil {
		return fmt.Errorf("%s: nil response", op)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := recipeReadAll(resp.Body)
		return fmt.Errorf("%s: HTTP %d: %s", op, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// recipeReadAll consumes the body reader; tolerates nil so that mock
// responses without bodies stay cheap to construct.
func recipeReadAll(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(r)
}
