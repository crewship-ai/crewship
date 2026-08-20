package main

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/modelcatalog"
)

// providerCmd groups LLM-provider discovery. Everything under it is LOCAL —
// no token, no workspace, no server call — because both questions it answers
// are compiled into the binary: the provider registry
// (internal/llm/registry.go) says which providers this build can construct,
// and the embedded models.dev snapshot (internal/modelcatalog) says what they
// serve and what it costs.
//
// That matters for who reads it. An operator asks "is my key even set?" while
// the server is still refusing to start, and an agent asks "what may I put in
// llm_model?" before it has a workspace. A command that needed a login to
// answer either would be useless at exactly the moment it is wanted.
//
// There is no GET /api/v1/providers behind this and none is needed: the CLI
// contract runs one way — every endpoint gets a command, not every command an
// endpoint — so cli_route_contract_test.go never sees a local command.
var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "Inspect the LLM providers this build can talk to",
}

// providerRow is one provider as the CLI renders it. The json tags are the
// contract an agent parses, so they name the concept rather than echoing the
// Go field.
//
// KeyRequired and KeySet are two facts, not one: Ollama needs no credential at
// all, and collapsing that into key_set=false would read as "your key is
// missing" for a provider that is working perfectly.
type providerRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	// Registered is false for a provider that exists only in the model
	// catalog — listable and priceable here, but with no codec in this build,
	// so nothing can actually call it.
	Registered bool `json:"registered"`

	Codec string `json:"codec,omitempty"`
	Auth  string `json:"auth,omitempty"`

	KeyEnv      string `json:"key_env,omitempty"`
	KeyRequired bool   `json:"key_required"`
	KeySet      bool   `json:"key_set"`

	// Endpoint is the address this provider resolves to right now: BaseEnv if
	// the operator set it, BaseDefault otherwise. Any userinfo is redacted —
	// a self-hosted endpoint may carry credentials in the URL and this command
	// is routinely pasted into an issue.
	Endpoint    string `json:"endpoint,omitempty"`
	EndpointEnv string `json:"endpoint_env,omitempty"`

	CatalogID       string `json:"catalog_id,omitempty"`
	CatalogModels   int    `json:"catalog_models"`
	DefaultAuxModel string `json:"default_aux_model,omitempty"`
}

// providerListResult is the `provider list` document.
type providerListResult struct {
	Providers []providerRow `json:"providers"`
}

var providerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the LLM providers in the registry, and whether their key is set",
	Long: `List every LLM provider this build can construct.

Runs entirely offline: the registry and the model catalog are both compiled
into the binary, so this works with no config file, no token and no reachable
server.

"KEY" reports only whether the environment variable is set, never its value.
A provider that needs no credential (a local runtime) shows "-".

With --all, providers that exist only in the embedded models.dev catalog are
listed too. Those have no REGISTRY row, so they cannot back an evaluator slot
and 'crewship provider list' cannot report an endpoint or key for them — but
some are still reachable: an OpenAI-compatible backend can be called through
'crewship provider check --provider <preset>', or through the openai codec with
--base-url. Run 'crewship provider check --help' for the preset names.

Examples:
  crewship provider list
  crewship provider list --all
  crewship provider list --format json | jq '.providers[] | select(.key_set | not)'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		all, _ := cmd.Flags().GetBool("all")

		// A corrupt snapshot must not take this command down: the registry
		// half of the answer is still valid and is the half an operator
		// debugging a missing key came for. Warn on stderr, report zero
		// catalog models, carry on. (--all has nothing to add, so it is the
		// one case that is worth failing.)
		catalogErr := modelcatalog.DefaultErr()
		if catalogErr != nil {
			if all {
				return cli.WithExitCode(
					fmt.Errorf("embedded model catalog is unreadable: %w", catalogErr), cli.ExitGeneric)
			}
			cli.PrintWarning("embedded model catalog is unreadable; model counts will read 0")
		}

		res := providerListResult{Providers: buildProviderRows(modelcatalog.Default(), all, os.LookupEnv)}

		f := resolvedFormatter(cmd)
		headers := []string{"PROVIDER", "NAME", "CODEC", "AUTH", "KEY ENV", "KEY", "ENDPOINT", "MODELS"}
		return f.Auto(res, headers, providerListRows(res.Providers))
	},
}

// lookupEnvFunc is os.LookupEnv, injected so the tests can describe an
// environment instead of mutating the one the test binary runs in — t.Setenv
// forbids the parallel tests the rest of this package uses.
type lookupEnvFunc func(string) (string, bool)

// buildProviderRows renders the registry (and, with all, the catalog-only
// providers) into rows.
//
// Registry rows come first in DECLARATION order, matching
// llm.RegisteredProviders and therefore the console's provider picker — the
// order is load-bearing there and re-sorting it here would make two surfaces
// that read one table disagree. Catalog-only rows follow, sorted, because they
// have no declaration order to preserve.
func buildProviderRows(cat modelcatalog.Catalog, all bool, lookupEnv lookupEnvFunc) []providerRow {
	specs := llm.RegisteredProviderSpecs()
	rows := make([]providerRow, 0, len(specs))
	seen := make(map[string]bool, len(specs))

	for _, spec := range specs {
		seen[spec.ID] = true
		row := providerRow{
			ID:              spec.ID,
			DisplayName:     spec.DisplayName,
			Registered:      true,
			Codec:           string(spec.Codec),
			Auth:            string(spec.Auth),
			KeyEnv:          spec.KeyEnv,
			KeyRequired:     spec.KeyEnv != "",
			EndpointEnv:     spec.BaseEnv,
			CatalogID:       catalogIDForSpec(spec),
			DefaultAuxModel: spec.DefaultAuxModel,
		}
		if row.KeyRequired {
			row.KeySet = envIsSet(lookupEnv, spec.KeyEnv)
		}
		row.Endpoint = redactURL(resolveProviderEndpoint(spec, lookupEnv))
		row.CatalogModels = len(cat.Models(row.CatalogID))
		rows = append(rows, row)
	}
	if !all {
		return rows
	}

	extra := make([]string, 0)
	for _, id := range cat.Providers() {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		models := cat.Models(id)
		rows = append(rows, providerRow{
			ID:            id,
			DisplayName:   catalogDisplayName(cat, id),
			Registered:    false,
			CatalogID:     id,
			CatalogModels: len(models),
		})
	}
	return rows
}

// catalogIDForSpec resolves a registry row to its models.dev provider id.
// An empty CatalogID means "same as ID" (see llm.ProviderSpec) — which for a
// provider the catalog carries nothing for, such as Ollama, simply resolves to
// an id that is not in the catalog and yields no models either way.
func catalogIDForSpec(spec llm.ProviderSpec) string {
	if spec.CatalogID != "" {
		return spec.CatalogID
	}
	return spec.ID
}

// catalogIDFor is catalogIDForSpec over a bare provider id, falling back to the
// id itself for a provider that has no registry row.
func catalogIDFor(id string) string {
	if spec, ok := llm.LookupProvider(id); ok {
		return catalogIDForSpec(spec)
	}
	return canonProviderID(id)
}

// canonProviderID applies the same widening llm.LookupProvider does, so
// "ANTHROPIC" from the API enum and "anthropic" from a flag are one provider.
func canonProviderID(id string) string { return strings.ToLower(strings.TrimSpace(id)) }

func catalogDisplayName(cat modelcatalog.Catalog, id string) string {
	if p, ok := cat[canonProviderID(id)]; ok && strings.TrimSpace(p.Name) != "" {
		return p.Name
	}
	return id
}

// envIsSet treats a variable set to whitespace as unset. An empty
// ANTHROPIC_API_KEY authenticates nothing, so reporting it as "set" would send
// an operator looking in the wrong place.
func envIsSet(lookupEnv lookupEnvFunc, name string) bool {
	v, ok := lookupEnv(name)
	return ok && strings.TrimSpace(v) != ""
}

// resolveProviderEndpoint mirrors the builder's precedence for a spec with no
// caller-supplied base: BaseEnv if the operator set it, else BaseDefault.
func resolveProviderEndpoint(spec llm.ProviderSpec, lookupEnv lookupEnvFunc) string {
	if spec.BaseEnv != "" {
		if v, ok := lookupEnv(spec.BaseEnv); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return spec.BaseDefault
}

// redactURL strips any userinfo from a URL, leaving everything else byte-
// identical. An operator-set endpoint may be https://user:token@host, and this
// command's output is the kind that gets pasted into a bug report.
//
// Unparseable input is returned unchanged: it is an operator's own string, it
// cannot have been a URL with credentials in it if url.Parse choked on it, and
// silently blanking it would hide the typo that is the actual problem.
func redactURL(raw string) string {
	if raw == "" || !strings.Contains(raw, "@") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = url.User("redacted")
	return u.String()
}

// providerListRows renders rows for the table view. Column 0 is the provider
// id, which is what --format quiet prints — the value you feed straight back
// into `crewship model list --provider`.
func providerListRows(rows []providerRow) [][]string {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.ID,
			r.DisplayName,
			dashIfEmpty(r.Codec),
			dashIfEmpty(r.Auth),
			dashIfEmpty(r.KeyEnv),
			providerKeyCell(r),
			dashIfEmpty(r.Endpoint),
			strconv.Itoa(r.CatalogModels),
		})
	}
	return out
}

// providerKeyCell is the human rendering of the (KeyRequired, KeySet) pair.
// "not needed" rather than a dash for a keyless provider: the dash means "we
// do not know", and for Ollama we know exactly — it takes no credential.
func providerKeyCell(r providerRow) string {
	switch {
	case !r.Registered:
		return dashIfEmpty("")
	case !r.KeyRequired:
		return "not needed"
	case r.KeySet:
		return "set"
	default:
		return "unset"
	}
}

func init() {
	providerListCmd.Flags().Bool("all", false,
		"Also list providers that exist only in the embedded model catalog (no codec in this build)")
	providerCmd.AddCommand(providerListCmd)

	// Self-registered here rather than from main.go's init, the same way
	// cmd_admin.go and cmd_page.go do it. Cross-file init order is not a
	// hazard: rootCmd is a package-level var, so it exists before any init
	// body runs.
	rootCmd.AddCommand(providerCmd)
}
