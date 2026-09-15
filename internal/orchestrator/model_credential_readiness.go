package orchestrator

// Model-credential readiness: will the credential that pays for this agent's
// model actually reach the CLI? (#2183, the question #2169 asked and #2177's
// "credentials.length === 0" guard could not answer.)
//
// The guard failed because it observed the wrong thing. "Some credential
// reaches this agent" is true for every agent in a workspace with one GitHub
// binding; "the model slot in the env is unfilled" is true for every healthy
// Claude Code agent, because the sidecar injects the key mid-flight and the
// env deliberately holds a dummy (apiKeyEnvVarsForAdapter). Neither predicate
// matches the mechanism.
//
// This file answers from the mechanism. It lives in this package, next to
// the selectors BuildEnvVarsSidecar, fileLogin, codexDefaultRoute and
// buildSidecarCreds, and calls the same classifiers they do —
// credentialOAuthKind, credTypeToProvider, credEnvDeliverable,
// apiKeyEnvVarsForAdapter, the adapter's AuthDelivery declaration — so the
// answer cannot drift from what the run does without the run drifting too.
// The API tier and the UI render it; they do not recompute it.
//
// It is advisory, and it says "unknown" rather than guess: a false "missing"
// on a working agent teaches an operator to ignore the report (the crew
// readiness family's rule, crew_credential_readiness.go). Everything it
// reports "missing" is a case where the run's own selector would find
// nothing.

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/llmroute"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// ModelCredentialState is the readiness verdict.
type ModelCredentialState string

const (
	// ModelCredentialReady: a credential the runtime delivers for the
	// adapter's provider is in the agent's delivery set.
	ModelCredentialReady ModelCredentialState = "ready"
	// ModelCredentialMissing: the adapter's provider is known and nothing in
	// the delivery set authenticates it. The first run fails at the first
	// model call.
	ModelCredentialMissing ModelCredentialState = "missing"
	// ModelCredentialUnknown: no opinion — an adapter this package does not
	// recognise, an OpenCode agent naming no provider, or a local endpoint
	// whose auth is the operator's business.
	ModelCredentialUnknown ModelCredentialState = "unknown"
)

// ModelCredentialDelivery names the channel the runtime uses for the
// matched credential. Reported so an operator reading "ready" for a Claude
// Code agent whose env shows a dummy ANTHROPIC_API_KEY knows that is the
// design, not the bug.
type ModelCredentialDelivery string

const (
	// DeliverySidecar: held in the sidecar CredStore and injected into the
	// outbound request by the reverse proxy. The env carries a dummy.
	DeliverySidecar ModelCredentialDelivery = "sidecar"
	// DeliveryLoginEnv: a subscription login written to the adapter's login
	// variable (Claude Code's CLAUDE_CODE_OAUTH_TOKEN).
	DeliveryLoginEnv ModelCredentialDelivery = "login_env"
	// DeliveryLoginFile: a subscription login rendered to the adapter's
	// login file under HOME (Codex's auth.json, Gemini's oauth_creds.json).
	DeliveryLoginFile ModelCredentialDelivery = "login_file"
	// DeliveryEnv: the real key written to the env, for a CLI that reaches
	// its upstream over a CONNECT tunnel the proxy cannot inject into.
	DeliveryEnv ModelCredentialDelivery = "env"
)

// ModelCredentialReport is the answer for one agent.
type ModelCredentialReport struct {
	State ModelCredentialState
	// Provider is the LLM provider the adapter pays through
	// (providerlogin.AdapterProvider), empty when State is unknown for want
	// of one.
	Provider string
	// CredentialID and Delivery describe the match when State is ready.
	CredentialID string
	Delivery     ModelCredentialDelivery
	// Notes carries the one-line reason for an unknown, and any caveat on a
	// ready/missing verdict, in operator-facing prose.
	Notes []string
}

// ModelCredentialReadiness classifies an agent's delivered credentials
// against its adapter. creds is the agent's delivery set in delivery order
// (the API tier's loadDeliveredCredentials); PlainValue may be empty — the
// classifiers this consults decide by type, provider and variable name, the
// way resolveEnvVar documents, and a handle-only credential is skipped the
// way every runtime selector skips an empty value.
func ModelCredentialReadiness(adapter, llmProvider, llmModel string, creds []Credential) ModelCredentialReport {
	report := ModelCredentialReport{State: ModelCredentialUnknown, Notes: []string{}}

	if _, known := adapterRegistry[adapter]; !known {
		report.Notes = append(report.Notes, "adapter "+strings.TrimSpace(adapter)+" is not one the runtime recognises; no opinion")
		return report
	}

	// OpenCode is bring-your-own across models.dev; the provider is whatever
	// the model string's prefix names, else the agent's llm_provider — the
	// same precedence resolveRoutedProvider applies.
	want := providerlogin.AdapterProvider(adapter, llmProvider)
	if adapter == "OPENCODE" {
		if strings.HasPrefix(llmModel, localModelPrefix) {
			report.Notes = append(report.Notes, "model targets the operator's local endpoint; whether it needs a credential is the endpoint's business")
			return report
		}
		if prefix, _, ok := strings.Cut(llmModel, "/"); ok && prefix != "" {
			want = providerlogin.Canonical(prefix)
		}
	}
	if want == "" {
		report.Notes = append(report.Notes, "the agent names no model provider; nothing to check against")
		return report
	}
	report.Provider = want

	// The sidecar CredStore is the delivery for the three proxy-injected
	// adapters (apiKeyEnvVarsForAdapter returns nil for them: the env keeps a
	// dummy). A CONNECT-tunnelled adapter gets the real key in its env, and
	// only under the variable it reads. OpenCode is both: routed to the proxy
	// for a /llm/… provider, env for the rest.
	allowed := apiKeyEnvVarsForAdapter(adapter)
	envVar := providerlogin.DeliveryFor(want, providerlogin.ModeAPIKey).Target
	if len(allowed) > 0 && envVar == "" && !proxyRoutableProvider(want) {
		report.Notes = append(report.Notes, "provider "+want+" has no key variable this runtime knows; no opinion")
		return report
	}
	login := getAdapter(adapter).AuthDelivery()

	for _, cred := range creds {
		if cred.HandleOnly {
			// Every delivery selector reads PlainValue == "" as "not
			// delivered"; a handle-only value is empty by construction.
			continue
		}
		if kind := credentialOAuthKind(cred); kind != oauthNone {
			// A login is never a proxy credential (credTypeToProvider keeps
			// it out of the CredStore); it pays only for the adapter whose
			// AuthDelivery declares that kind — loginCredentialFor / fileLogin.
			if kind == login.Kind && credEnvDeliverable(cred) {
				report.State = ModelCredentialReady
				report.CredentialID = cred.ID
				report.Delivery = DeliveryLoginEnv
				if login.FileDelivered() {
					report.Delivery = DeliveryLoginFile
				}
				return report
			}
			continue
		}
		if len(allowed) > 0 {
			// BuildEnvVarsSidecar's allowed-override loop: the variable must
			// be one the adapter reads AND the type must have a delivery
			// channel. gemini-cli reads either Google spelling; the runtime
			// mirrors the value into both.
			if _, ok := allowed[cred.EnvVarName]; ok && credEnvDeliverable(cred) && sameKeyVariable(cred.EnvVarName, envVar) {
				report.State = ModelCredentialReady
				report.CredentialID = cred.ID
				report.Delivery = DeliveryEnv
				return report
			}
			// The routed exception: an OpenCode run whose provider owns a
			// /llm/… route is served from the CredStore instead.
			if adapter == "OPENCODE" && proxyRoutableProvider(want) && credTypeToProvider(cred) == want {
				report.State = ModelCredentialReady
				report.CredentialID = cred.ID
				report.Delivery = DeliverySidecar
				return report
			}
			continue
		}
		// Proxy-injected adapters: what buildSidecarCreds loads under the
		// adapter's provider is what the reverse proxy will inject.
		if credTypeToProvider(cred) == want {
			report.State = ModelCredentialReady
			report.CredentialID = cred.ID
			report.Delivery = DeliverySidecar
			return report
		}
	}

	report.State = ModelCredentialMissing
	report.Notes = append(report.Notes, "no credential in this agent's delivery authenticates "+want+" for "+adapter)
	return report
}

// proxyRoutableProvider reports whether provider owns a reserved /llm/…
// sidecar route — the OpenCode routed path.
func proxyRoutableProvider(provider string) bool {
	s, ok := llmroute.Lookup(provider)
	return ok && proxyRoutable(s)
}

// sameKeyVariable is the Google twin rule from BuildEnvVarsSidecar: a key
// under GOOGLE_API_KEY is mirrored into GEMINI_API_KEY and vice versa, so
// either spelling fills the other.
func sameKeyVariable(have, want string) bool {
	if have == want {
		return true
	}
	google := map[string]bool{"GOOGLE_API_KEY": true, "GEMINI_API_KEY": true}
	return google[have] && google[want]
}
