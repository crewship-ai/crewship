package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/codexauth"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

// Codex's ChatGPT-subscription login is delivered as a FILE, not an env var,
// because that is the only way the Codex binary reads one (#2428,
// docs/prd/provider-logins.md §3.2). This file is the delivery step: it sits
// next to writeCredentialFiles in the preflight and mirrors its posture — the
// file lands before the CLI starts, a run that cannot write it does not start,
// and revoking the credential removes it (credential_reconcile.go).
//
// It is deliberately NOT a branch of buildCredFileScript. That script mounts
// under /secrets/<agent> and hands the agent a path; Codex wants the file at a
// path of its own choosing under $CODEX_HOME, rendered — not copied — so the
// refresh token stays behind. Same channel as the MCP config that already
// lives in that directory (writeFileViaContainer, 0600).

// codexHomeDir is the CODEX_HOME an agent runs with: its own HOME's .codex.
// Codex would default to the same place; naming it pins the login and the MCP
// config to one directory even if HOME ever moves.
func codexHomeDir(agentSlug string) string {
	return agentHomeDir(agentSlug) + "/" + codexauth.HomeRel
}

// agentHomeDir is the HOME baseAgentEnv gives every agent.
func agentHomeDir(agentSlug string) string {
	return "/crew/agents/" + agentSlug
}

// codexLoginCredential returns the ChatGPT login this run carries, if any.
// Like the Anthropic OAuth selector it takes the FIRST deliverable one: two
// logins on one agent is a configuration the pool work (PRD §5.4) will give
// meaning to; today the first wins, the way the Claude selector already does.
func codexLoginCredential(req AgentRunRequest) (Credential, bool) {
	for _, cred := range req.Credentials {
		if credentialOAuthKind(cred) != oauthOpenAI || cred.PlainValue == "" || !credEnvDeliverable(cred) {
			continue
		}
		return cred, true
	}
	return Credential{}, false
}

// syncCodexAuthFile makes $CODEX_HOME/auth.json reflect this run's
// credentials: written (rendered, placeholder refresh token) when the run
// carries a ChatGPT login, REMOVED when it does not.
//
// The removal is not tidiness. HOME sits on a persistent volume (the V2
// finding in PRD-CREDENTIALS-V2), so a login delivered last week is still
// there this week after the operator unassigned it — and Codex would keep
// paying for runs with a seat nobody meant it to use. Revoke already deletes
// the file through the credential's own path; unassign has no such hook, so
// the run start is where the file and the assignment are reconciled.
//
// A write failure is fatal to the run, for the reason writeCredentialFiles
// gives for file-mounted credentials: Codex without its login does not fail
// closed, it falls through to the dummy OPENAI_API_KEY and spends the run on
// a 401 that blames the key.
func syncCodexAuthFile(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	logger *slog.Logger,
) error {
	login, ok := codexLoginCredential(req)
	if !ok {
		return removeCodexAuthFile(ctx, container, containerID, req.AgentSlug, logger)
	}
	f, err := codexFileFor(login)
	if err != nil {
		return fmt.Errorf("codex login %s: %w", login.ID, err)
	}
	body, err := codexauth.Render(f, time.Now())
	if err != nil {
		return fmt.Errorf("codex login %s: render: %w", login.ID, err)
	}
	if err := writeFileViaContainer(ctx, container, containerID, agentHomeDir(req.AgentSlug), codexauth.FileRel, string(body), containerFileSecret, logger); err != nil {
		return err
	}
	if logger != nil {
		logger.Info("codex login delivered",
			"agent_slug", req.AgentSlug, "credential_id", login.ID,
			"plan", codexPlanLabel(login), "path", codexauth.FileRel)
	}
	return nil
}

// DeliverCodexLogin renders one ChatGPT login into a running crew container
// for one agent — the re-render after a central refresh (PRD provider-logins
// §5.3, §10.4). The API tier calls it with the credential it just rotated;
// the run start calls syncCodexAuthFile with the whole delivery set. Both
// go through the same renderer, so the file a refresh writes is the file a
// run start would have written.
func DeliverCodexLogin(ctx context.Context, container provider.ContainerProvider, containerID, agentSlug string, login Credential, logger *slog.Logger) error {
	req := AgentRunRequest{AgentSlug: agentSlug, CLIAdapter: "CODEX_CLI", Credentials: []Credential{login}}
	if _, ok := codexLoginCredential(req); !ok {
		return fmt.Errorf("credential %s is not a deliverable ChatGPT login", login.ID)
	}
	return syncCodexAuthFile(ctx, container, containerID, req, logger)
}

// codexFileFor builds the auth.json Codex will read from either shape a login
// arrives in (PRD provider-logins §10.2): a PROVIDER_LOGIN carries the access
// token as its value and id_token / account_id as parts; the legacy
// AI_CLI_TOKEN carries the whole file. Codex refuses a file missing any of
// the three, so a login that cannot be rendered is refused here, before the
// run starts on the dummy key.
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

// codexPlanLabel is the flat-rate plan label for a ChatGPT login. A
// PROVIDER_LOGIN carries the plan as a part; the legacy blob and a bare
// access token both yield it from the token's claim.
func codexPlanLabel(login Credential) string {
	if login.isProviderLogin() {
		if plan := login.part(providerlogin.PartPlan); plan != "" {
			return providerlogin.PlanLabel(codexauth.ProviderID, plan)
		}
	}
	return codexauth.PlanLabel(login.PlainValue)
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

// removeCodexAuthFile deletes a previously delivered login. Best-effort in
// the sense that a missing file is success (`rm -f`), but an exec failure is
// still reported: a login that should be gone and is not is the case the
// caller must hear about.
func removeCodexAuthFile(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	agentSlug string,
	logger *slog.Logger,
) error {
	path := shellEscape(codexauth.FileRel)
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", "rm -f " + path},
		WorkingDir:  agentHomeDir(agentSlug),
		User:        "1001:1001",
	}
	if err := runOrBatch(ctx, container, "rm:"+codexauth.FileRel, cfg); err != nil {
		return fmt.Errorf("remove %s: %w", codexauth.FileRel, err)
	}
	if logger != nil {
		logger.Debug("codex login not on this run; stale auth.json removed if present", "agent_slug", agentSlug)
	}
	return nil
}
