package main

// `crewship provider route` — the sidecar's side of the provider table.
//
// `provider list` (cmd_provider.go) answers "which providers can THIS BINARY
// construct, and is the key in my environment" — the crewshipd question. This
// command answers the other half: when an agent's CLI talks to 127.0.0.1:9119,
// which path does each provider live on, where does the sidecar forward it, and
// what does it put the credential into on the way out.
//
// Those two tables are deliberately different sets (see internal/llmroute):
// registering a provider here does not make crewshipd able to call it, and
// vice versa. Printing them from one command would suggest a symmetry that does
// not exist and would send an operator looking for a key in the wrong place.
//
// Local by construction — the descriptor table is compiled in, so no server, no
// token and no workspace. Same reasoning as `provider list`: the questions it
// answers ("is my OpenAI-compatible endpoint even routed?", "which header does
// my key land in?") are asked while something is broken.
//
// There is no GET /api/v1/provider-routes behind it and none is wanted: the CLI
// contract runs one way, so route-roles.txt and cli_route_contract_test.go are
// untouched by a local command.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/llmroute"
)

var providerRouteCmd = &cobra.Command{
	Use:   "route",
	Short: "Inspect how the sidecar routes each provider and where its credential lands",
}

// providerRouteSlot is one place the sidecar writes the token. Kept structured
// rather than pre-rendered because an agent parsing --format json is checking
// "does my key go into a header named X", and a formatted string makes it guess.
type providerRouteSlot struct {
	Placement string `json:"placement"` // "header" or "query"
	Name      string `json:"name"`
	// Prefix is what precedes the token in the slot's value — "Bearer " for an
	// Authorization header, empty for x-api-key. Never the token itself: this
	// command never sees a credential.
	Prefix string `json:"prefix,omitempty"`
}

// providerRouteAuthRule is one token-shape branch. TokenPrefix empty is the
// default rule, and llmroute guarantees exactly one of those, last — so a
// reader can take the last entry as "what happens to an ordinary key".
type providerRouteAuthRule struct {
	TokenPrefix string              `json:"token_prefix,omitempty"`
	Slots       []providerRouteSlot `json:"slots"`
}

// providerRouteRow is one descriptor as the CLI renders it. The json tags name
// the concept rather than echoing llmroute's Go field names, so a rename there
// is not a breaking change for a script.
//
// RequiresCredential and UpstreamFromCredential are two facts, not one: the
// three built-in providers forward to a fixed host with no credential attached
// (that is today's Anthropic OAuth path and it must keep working), while a
// provider whose upstream comes OUT of the credential has nowhere to send a
// request without one.
type providerRouteRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`

	// PathPrefix is the path an agent's CLI is pointed at on 127.0.0.1:9119.
	PathPrefix  string `json:"path_prefix"`
	StripPrefix bool   `json:"strip_prefix"`

	// Upstream is the fixed dial target, base path included. Empty exactly when
	// UpstreamFromCredential — the endpoint is operator data and this command
	// deliberately does not read credentials to fill it in.
	Upstream               string `json:"upstream,omitempty"`
	UpstreamFromCredential bool   `json:"upstream_from_credential"`
	RequiresCredential     bool   `json:"requires_credential"`

	// LedgerProvider is the key cost_ledger rows are written under; BodyCodec
	// is which response shape usage is parsed from. They differ for OpenRouter,
	// which speaks OpenAI's body shape but bills on its own rate card.
	LedgerProvider string `json:"ledger_provider"`
	BodyCodec      string `json:"body_codec,omitempty"`

	// Priced is false when LedgerProvider has no row in the rate card, which
	// means every call through this route bills $0. That is the honest outcome
	// for a bring-your-own endpoint and the operator is meant to see it here
	// rather than discover it in an empty spend report.
	Priced bool `json:"priced"`

	Auth          []providerRouteAuthRule `json:"auth"`
	StaticHeaders map[string]string       `json:"static_headers,omitempty"`

	KeyEnvVars []string `json:"key_env_vars,omitempty"`

	// ForwardProxyHosts are the upstream hostnames the sidecar ALSO recognises
	// on its forward-proxy path (an agent dialling api.openai.com directly).
	// Empty for a provider added after the reverse proxy became descriptor-
	// driven — see internal/llmroute for why that asymmetry is deliberate.
	ForwardProxyHosts []string `json:"forward_proxy_hosts,omitempty"`
}

// providerRouteListResult is the `provider route list` document.
type providerRouteListResult struct {
	Routes []providerRouteRow `json:"routes"`
}

var providerRouteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the provider routes the sidecar serves",
	Long: `List every provider the agent sidecar can proxy, in declaration order.

Each row is one descriptor from internal/llmroute: the path an agent's CLI is
pointed at inside the container, where the sidecar forwards it, and which header
or query parameter the credential is written into.

"UPSTREAM" reads "from credential" for a provider whose endpoint is supplied by
the credential itself (a self-hosted or vendor OpenAI-compatible gateway). Those
routes refuse the request when no credential is loaded, rather than forwarding
it unauthenticated.

Runs entirely offline: the descriptor table is compiled into this binary, so
this works with no config file, no token and no reachable server. It reports the
routing table, never a credential value — nothing here reads the vault.

Examples:
  crewship provider route list
  crewship provider route list --format json | jq '.routes[] | select(.upstream_from_credential)'
  crewship provider route show OPENROUTER`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		res := providerRouteListResult{Routes: buildProviderRouteRows(llmroute.Specs())}
		return resolvedFormatter(cmd).Auto(res, providerRouteListHeaders, providerRouteListRows(res.Routes))
	},
}

var providerRouteShowCmd = &cobra.Command{
	Use:   "show <provider>",
	Short: "Show one provider route in full",
	Long: `Print one provider's sidecar route: path prefix, upstream, every auth slot
the credential is written into, the static headers added on the way out, and the
key the calls are billed under.

The provider id is the value stored in a credential's provider column and is
matched case-insensitively.

Examples:
  crewship provider route show openrouter
  crewship provider route show OPENAI_COMPAT --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := canonRouteProviderID(args[0])
		spec, ok := llmroute.Lookup(id)
		if !ok {
			// ExitNotFound, matching `model list --provider <typo>`: the flag was
			// well-formed, the thing it named does not exist.
			return cli.WithExitCode(
				fmt.Errorf("unknown provider route %q (known: %s)", args[0], strings.Join(routeProviderIDs(), ", ")),
				cli.ExitNotFound)
		}
		row := buildProviderRouteRow(spec)
		return resolvedFormatter(cmd).AutoDetail(row, providerRouteDetailPairs(row))
	},
}

// canonRouteProviderID widens the accepted spelling to match how the id is
// written everywhere else an operator meets it: `credential create --provider`
// takes it in any case, so `route show` must too.
func canonRouteProviderID(id string) string {
	return strings.ToUpper(strings.TrimSpace(id))
}

// routeProviderIDs is the id list used in error messages, in declaration order
// so it reads the same as the table.
func routeProviderIDs() []string {
	specs := llmroute.Specs()
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.ID)
	}
	return out
}

// buildProviderRouteRows renders the descriptor table in DECLARATION order.
// Same reasoning as buildProviderRows: the order is a property of the table
// (the built-in providers first, additions after) and re-sorting it here would
// make two surfaces reading one table disagree.
func buildProviderRouteRows(specs []llmroute.Spec) []providerRouteRow {
	rows := make([]providerRouteRow, 0, len(specs))
	for _, s := range specs {
		rows = append(rows, buildProviderRouteRow(s))
	}
	return rows
}

func buildProviderRouteRow(s llmroute.Spec) providerRouteRow {
	row := providerRouteRow{
		ID:                     s.ID,
		DisplayName:            s.DisplayName,
		PathPrefix:             s.PathPrefix,
		StripPrefix:            s.StripPrefix,
		Upstream:               routeUpstream(s),
		UpstreamFromCredential: s.UpstreamFromCredential,
		RequiresCredential:     s.RequireCredential,
		LedgerProvider:         s.LedgerProvider,
		BodyCodec:              s.BodyCodec,
		Priced:                 providerIsPriced(s.LedgerProvider),
		StaticHeaders:          s.StaticHeaders,
		KeyEnvVars:             append([]string(nil), s.KeyEnvVars...),
		ForwardProxyHosts:      append([]string(nil), s.Hosts...),
	}
	for _, rule := range s.AuthRules {
		out := providerRouteAuthRule{TokenPrefix: rule.TokenPrefix}
		for _, slot := range rule.Slots {
			out.Slots = append(out.Slots, providerRouteSlot{
				Placement: string(slot.Placement),
				Name:      slot.Name,
				Prefix:    slot.Prefix,
			})
		}
		row.Auth = append(row.Auth, out)
	}
	return row
}

// routeUpstream renders the fixed dial target. Empty for a credential-supplied
// upstream — printing a placeholder host there would be a guess, and this is a
// command an operator reads to find out where their traffic actually goes.
func routeUpstream(s llmroute.Spec) string {
	if s.UpstreamFromCredential || s.UpstreamHost == "" {
		return ""
	}
	return s.UpstreamHost + s.UpstreamBasePath
}

// providerRouteListHeaders is the table's header row, package-level so the
// test that pins "every row has one cell per column" reads the same list the
// command prints.
var providerRouteListHeaders = []string{"PROVIDER", "PATH", "UPSTREAM", "AUTH", "CREDENTIAL", "BILLED AS"}

// providerRouteListRows renders rows for the table view. Column 0 is the
// provider id — what --format quiet prints, and the value that goes straight
// back into `credential create --provider`.
func providerRouteListRows(rows []providerRouteRow) [][]string {
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.ID,
			r.PathPrefix,
			routeUpstreamCell(r),
			routeAuthCell(r),
			routeCredentialCell(r),
			routeBilledCell(r),
		})
	}
	return out
}

// routeUpstreamCell is the human rendering of the upstream. "from credential"
// rather than a dash: the dash means "we do not know", and here we know
// exactly — the endpoint is in the credential the operator pasted.
func routeUpstreamCell(r providerRouteRow) string {
	if r.UpstreamFromCredential {
		return "from credential"
	}
	return dashIfEmpty(r.Upstream)
}

// routeAuthCell summarises the DEFAULT rule's slots — the last one, which
// llmroute guarantees exists — and reports the count of token-shape branches
// ahead of it rather than rendering them all into one cell. `route show` prints
// them in full.
func routeAuthCell(r providerRouteRow) string {
	if len(r.Auth) == 0 {
		return dashIfEmpty("")
	}
	def := r.Auth[len(r.Auth)-1]
	cell := strings.Join(routeSlotStrings(def.Slots), " + ")
	if n := len(r.Auth) - 1; n > 0 {
		cell += fmt.Sprintf(" (+%d by token prefix)", n)
	}
	return dashIfEmpty(cell)
}

// routeSlotStrings renders slots as "header Authorization: Bearer" /
// "query key". The prefix is shown because it is the difference between a
// working request and a 401 that reads like a bad key.
func routeSlotStrings(slots []providerRouteSlot) []string {
	out := make([]string, 0, len(slots))
	for _, s := range slots {
		text := s.Placement + " " + s.Name
		if s.Prefix != "" {
			text += ": " + strings.TrimSpace(s.Prefix)
		}
		out = append(out, text)
	}
	return out
}

// routeCredentialCell says what happens when the CredStore has nothing for this
// provider. "optional" is not "unauthenticated": those routes pass the agent's
// own request through untouched, which is how the Anthropic OAuth token — which
// the sidecar never holds — still reaches the upstream.
func routeCredentialCell(r providerRouteRow) string {
	if r.RequiresCredential {
		return "required"
	}
	return "optional"
}

// routeBilledCell is the ledger key, flagged when nothing prices it. An
// operator who does not see "(unpriced)" here and then sees $0 in a spend
// report has no way to tell a cheap month from a missing rate row.
func routeBilledCell(r providerRouteRow) string {
	if !r.Priced {
		return r.LedgerProvider + " (unpriced)"
	}
	return r.LedgerProvider
}

// providerIsPriced asks paymaster whether this ledger key resolves to a real
// rate anywhere in its chain. It reuses explainRate (cmd_model_price.go) so the
// answer is paymaster's own, not a second opinion: a "none" source is exactly
// the case where cost_ledger rows come out at $0.
func providerIsPriced(ledgerProvider string) bool {
	if ledgerProvider == "" {
		return false
	}
	return explainRate(ledgerProvider, priceProbeModel).Source != rateFromNone
}

// providerRouteDetailPairs is the `route show` rendering. Ordered as the
// request travels: what the agent dials, where it goes, what is added to it,
// and what it costs.
func providerRouteDetailPairs(r providerRouteRow) [][]string {
	pairs := [][]string{
		{"Provider", r.ID},
		{"Name", r.DisplayName},
		{"Agent path", r.PathPrefix},
		{"Prefix", routeStripCell(r)},
		{"Upstream", routeUpstreamCell(r)},
		{"Credential", routeCredentialCell(r)},
	}
	for _, rule := range r.Auth {
		// The rule that selects on a token prefix is labelled with it: an
		// operator whose OAuth token went into the wrong header needs to see
		// that the provider has two branches and which one their value takes.
		label := "Auth"
		switch {
		case rule.TokenPrefix != "":
			label = fmt.Sprintf("Auth (token %s…)", rule.TokenPrefix)
		case len(r.Auth) > 1:
			label = "Auth (default)"
		}
		pairs = append(pairs, []string{label, strings.Join(routeSlotStrings(rule.Slots), " + ")})
	}
	if len(r.StaticHeaders) > 0 {
		names := make([]string, 0, len(r.StaticHeaders))
		for k := range r.StaticHeaders {
			names = append(names, k)
		}
		// Map iteration order is random and this line lands in bug reports.
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, k := range names {
			parts = append(parts, k+": "+r.StaticHeaders[k])
		}
		pairs = append(pairs, []string{"Static headers", strings.Join(parts, ", ")})
	}
	pairs = append(pairs, []string{"Billed as", routeBilledCell(r)})
	if r.BodyCodec != "" {
		pairs = append(pairs, []string{"Usage parsed as", r.BodyCodec})
	}
	if len(r.KeyEnvVars) > 0 {
		pairs = append(pairs, []string{"Key env vars", strings.Join(r.KeyEnvVars, ", ")})
	}
	if len(r.ForwardProxyHosts) > 0 {
		pairs = append(pairs, []string{"Forward-proxy hosts", strings.Join(r.ForwardProxyHosts, ", ")})
	}
	return pairs
}

// routeStripCell spells out what happens to the path prefix, because "strip:
// false" reads as a missing feature rather than as Anthropic's "/v1/messages
// IS the upstream path".
func routeStripCell(r providerRouteRow) string {
	if r.StripPrefix {
		return "stripped before forwarding"
	}
	return "forwarded verbatim"
}

func init() {
	providerRouteCmd.AddCommand(providerRouteListCmd)
	providerRouteCmd.AddCommand(providerRouteShowCmd)
	providerCmd.AddCommand(providerRouteCmd)
}
