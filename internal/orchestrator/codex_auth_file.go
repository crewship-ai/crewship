package orchestrator

import (
	"context"
	"errors"
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

func DeliverProviderLogin(ctx context.Context, container provider.ContainerProvider, containerID, agentSlug, adapter string, login Credential, logger *slog.Logger) error {
	return syncLoginFile(ctx, container, containerID, AgentRunRequest{AgentSlug: agentSlug, CLIAdapter: adapter, Credentials: []Credential{login}}, logger)
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
// A setup-token names no plan, so the legacy AI_CLI_TOKEN keeps the label it
// has always had; a PROVIDER_LOGIN whose owner recorded the plan shows it.
func anthropicPlanLabel(login Credential) string {
	if login.isProviderLogin() {
		if plan := login.part(providerlogin.PartPlan); plan != "" {
			return providerlogin.PlanLabel("ANTHROPIC", plan)
		}
	}
	return "Anthropic Max"
}
