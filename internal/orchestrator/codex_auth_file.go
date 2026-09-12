package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/codexauth"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

func codexLoginCredential(req AgentRunRequest) (Credential, bool) {
	return loginCredentialFor(req, oauthOpenAI)
}

// DeliverCodexLogin refreshes the derivative through the same path as run startup.
func DeliverCodexLogin(ctx context.Context, container provider.ContainerProvider, containerID, agentSlug string, login Credential, logger *slog.Logger) error {
	return DeliverProviderLogin(ctx, container, containerID, agentSlug, "CODEX_CLI", login, logger)
}

// DeliverProviderLogin re-renders a refreshed provider login into EVERY LIVE
// RUN of agentSlug in this container.
//
// Before E0 there was one HOME per agent and so one file to update. There is
// now one per run, and this fans out across them — because the whole point of
// the refresher is to reach a run that is ALREADY EXECUTING and whose CLI would
// otherwise keep presenting the token that was just rotated out from under it.
// Writing a single copy to the agent's shared directory would put it where
// nothing reads: a long Codex or Gemini run would carry on with the stale token
// until its OAuth failed mid-run, and the failure would look like a provider
// problem rather than a delivery one.
//
// No live run is a normal answer and a clean no-op, not an error. A run that
// has not started yet renders its own login file at preflight (syncLoginFile,
// next to writeCredentialFiles) from the current credential, so there is
// genuinely nothing to do — which is also what the one caller already assumes
// ("the next run start writes the file", provider_login_refresh.go).
//
// Errors are joined rather than short-circuited: with two runs live, a failure
// to reach one must not silently skip the other.
func DeliverProviderLogin(ctx context.Context, container provider.ContainerProvider, containerID, agentSlug, adapter string, login Credential, logger *slog.Logger) error {
	runIDs := liveRunIDsForAgent(containerID, agentSlug)
	if len(runIDs) == 0 {
		if logger != nil {
			logger.Debug("provider login re-render: no live run for this agent, nothing to update",
				"agent_slug", agentSlug, "container_id", containerID, "adapter", adapter)
		}
		return nil
	}
	var errs []error
	for _, runID := range runIDs {
		req := AgentRunRequest{
			AgentSlug:   agentSlug,
			RunID:       runID,
			CLIAdapter:  adapter,
			Credentials: []Credential{login},
		}
		if err := syncLoginFile(ctx, container, containerID, req, logger); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", runID, err))
		}
	}
	return errors.Join(errs...)
}

func codexFileFor(login Credential) (codexauth.File, error) {
	if !login.isProviderLogin() {
		return codexauth.Parse(login.PlainValue)
	}
	f := codexauth.File{Tokens: codexauth.Tokens{
		AccessToken: strings.TrimSpace(login.PlainValue),
		IDToken:     strings.TrimSpace(login.part(providerlogin.PartIDToken)),
		AccountID:   strings.TrimSpace(login.part(providerlogin.PartAccountID)),
	}}
	switch {
	case f.Tokens.AccessToken == "":
		return codexauth.File{}, errors.New("login has no access token")
	case f.Tokens.IDToken == "":
		return codexauth.File{}, errors.New("login has no id_token part — Codex refuses a file without it; re-import the auth.json")
	case f.Tokens.AccountID == "":
		return codexauth.File{}, errors.New("login has no account_id part; re-import the auth.json")
	}
	return f, nil
}

// anthropicPlanLabel is the flat-rate plan label for a Claude Code login.
// A setup-token names no plan; only an explicitly recorded plan may be shown.
func anthropicPlanLabel(login Credential) string {
	if login.isProviderLogin() {
		if plan := login.part(providerlogin.PartPlan); plan != "" {
			return providerlogin.PlanLabel("ANTHROPIC", plan)
		}
	}
	return "Claude (plan unknown)"
}
