package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/codexauth"
	"github.com/crewship-ai/crewship/internal/geminiauth"
	"github.com/crewship-ai/crewship/internal/provider"
)

// AuthDelivery declares how one CLI's binary reads the account that pays for
// its model (docs/prd/provider-logins.md §5.2). The survey in §3.1 found only
// two shapes across every coding-agent CLI — a token in an environment
// variable, or a file at a fixed path under the CLI's HOME — so one
// declaration per adapter covers all of them, and the delivery code reads
// the declaration instead of switching on the adapter name in five places.
//
// A declaration says WHERE, never WHAT: the value that lands there is still
// chosen by the credential selectors in exec_env.go, gated by credpolicy the
// same way as every other delivery.
type AuthDelivery struct {
	// Env is the variable the CLI reads its account credential from — the
	// login for Claude Code (CLAUDE_CODE_OAUTH_TOKEN), the API key for the
	// CLIs whose only headless credential is a key (CURSOR_API_KEY,
	// FACTORY_API_KEY). Empty when the CLI has no such variable.
	Env string

	// File is the login file relative to the agent's HOME, for a CLI that
	// reads its subscription login only from disk (".codex/auth.json",
	// ".gemini/oauth_creds.json"). Empty when the CLI has no file form.
	File string

	// Kind is the login this adapter's subscription path takes — which
	// AI_CLI_TOKEN provider it is. oauthNone for a CLI with no subscription
	// path of its own.
	Kind oauthKind

	// Render produces File's content from the stored login WITHOUT any
	// rotating material: the refresh token is replaced by a placeholder the
	// CLI accepts but cannot use, so a container can never rotate — and
	// thereby invalidate — a login shared with other containers. nil when
	// File is empty.
	Render func(value string, now time.Time) ([]byte, error)

	// PlanLabel names the subscription the login pays with ("ChatGPT Plus",
	// "Google AI Pro") for the Paymaster's flat-rate tag. nil when Kind is
	// oauthNone.
	PlanLabel func(value string) string
}

// FileDelivered reports whether this adapter's login lands on disk.
func (d AuthDelivery) FileDelivered() bool { return d.File != "" }

// The declarations. Kept together rather than spread over the adapter files
// so the table can be read — and tested — as one statement of "where every
// CLI's login goes".

func (claudeCodeAdapter) AuthDelivery() AuthDelivery {
	return AuthDelivery{Env: claudeOAuthTokenEnv, Kind: oauthAnthropic, PlanLabel: anthropicPlanLabel}
}

func (codexAdapter) AuthDelivery() AuthDelivery {
	return AuthDelivery{
		File: codexauth.FileRel,
		Kind: oauthOpenAI,
		Render: func(value string, now time.Time) ([]byte, error) {
			f, err := codexauth.Parse(value)
			if err != nil {
				return nil, err
			}
			return codexauth.Render(f, now)
		},
		PlanLabel: codexauth.PlanLabel,
	}
}

func (geminiAdapter) AuthDelivery() AuthDelivery {
	return AuthDelivery{
		Env:  "GEMINI_API_KEY",
		File: geminiauth.FileRel,
		Kind: oauthGoogle,
		Render: func(value string, _ time.Time) ([]byte, error) {
			f, err := geminiauth.Parse(value)
			if err != nil {
				return nil, err
			}
			return geminiauth.Render(f)
		},
		PlanLabel: geminiauth.PlanLabel,
	}
}

func (cursorAdapter) AuthDelivery() AuthDelivery { return AuthDelivery{Env: "CURSOR_API_KEY"} }

func (droidAdapter) AuthDelivery() AuthDelivery { return AuthDelivery{Env: "FACTORY_API_KEY"} }

// OpenCode is BYOK across its provider env vars (apiKeyEnvVarsForAdapter) and
// has no subscription login of its own; its auth.json file form is a later
// increment (PRD §7 P-C).
func (opencodeAdapter) AuthDelivery() AuthDelivery { return AuthDelivery{} }

func (unknownAdapter) AuthDelivery() AuthDelivery { return AuthDelivery{} }

// claudeOAuthTokenEnv is the one variable Claude Code reads a subscription
// login from; it ignores the same token in ANTHROPIC_API_KEY.
const claudeOAuthTokenEnv = "CLAUDE_CODE_OAUTH_TOKEN"

// anthropicPlanLabel is the label the Claude Code OAuth path has always
// carried. A setup-token names no plan, so it stays the fixed string it was.
func anthropicPlanLabel(string) string { return "Anthropic Max" }

// loginEnvVar returns the variable an env-delivered login of this kind is
// written to, or "" when that kind is not env-delivered.
func loginEnvVar(kind oauthKind) string {
	if kind == oauthAnthropic {
		return claudeCodeAdapter{}.AuthDelivery().Env
	}
	return ""
}

// loginCredentialFor returns the FIRST deliverable login of the given kind on
// the run. Two logins of one kind on one agent is a configuration the pool
// work (PRD §5.4) will give meaning to; today the first wins, the way the
// Claude selector always has.
func loginCredentialFor(req AgentRunRequest, kind oauthKind) (Credential, bool) {
	if kind == oauthNone {
		return Credential{}, false
	}
	for _, cred := range req.Credentials {
		if credentialOAuthKind(cred) != kind || cred.PlainValue == "" || !credEnvDeliverable(cred) {
			continue
		}
		return cred, true
	}
	return Credential{}, false
}

// fileLogin returns the adapter's file delivery and the login that fills it,
// when this run's adapter reads its login from disk AND the run carries one.
func fileLogin(req AgentRunRequest) (AuthDelivery, Credential, bool) {
	d := getAdapter(req.CLIAdapter).AuthDelivery()
	if !d.FileDelivered() {
		return d, Credential{}, false
	}
	cred, ok := loginCredentialFor(req, d.Kind)
	return d, cred, ok
}

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

// syncLoginFile makes the adapter's login file reflect this run's
// credentials: written (rendered, placeholder refresh token) when the run
// carries a login of the adapter's kind, REMOVED when it does not, and a
// no-op for an adapter with no file form.
//
// A file-delivered login (#2428) is delivered here, in the preflight next to
// writeCredentialFiles, and mirrors its posture — the file lands before the
// CLI starts, a run that cannot write it does not start, and revoking the
// credential removes it (credential_reconcile.go). It is deliberately NOT a
// branch of buildCredFileScript: that script mounts under /secrets/<agent>
// and hands the agent a path; these CLIs want the file at a path of their
// own choosing under HOME, rendered — not copied — so the refresh token
// stays behind. Same channel as the MCP config that already lives in those
// directories (writeFileViaContainer, 0600).
//
// The removal is not tidiness. HOME sits on a persistent volume (the V2
// finding in PRD-CREDENTIALS-V2), so a login delivered last week is still
// there this week after the operator unassigned it — and the CLI would keep
// paying for runs with a seat nobody meant it to use. Revoke already deletes
// the file through the credential's own path; unassign has no such hook, so
// the run start is where the file and the assignment are reconciled.
//
// A write failure is fatal to the run, for the reason writeCredentialFiles
// gives for file-mounted credentials: a CLI without its login does not fail
// closed, it falls through to the dummy API key and spends the run on a 401
// that blames the key.
func syncLoginFile(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	req AgentRunRequest,
	logger *slog.Logger,
) error {
	d, login, ok := fileLogin(req)
	if !d.FileDelivered() {
		return nil
	}
	if !ok {
		return removeLoginFile(ctx, container, containerID, req.AgentSlug, d.File, logger)
	}
	body, err := d.Render(login.PlainValue, time.Now())
	if err != nil {
		return fmt.Errorf("%s login %s: %w", req.CLIAdapter, login.ID, err)
	}
	if err := writeFileViaContainer(ctx, container, containerID, agentHomeDir(req.AgentSlug), d.File, string(body), containerFileSecret, logger); err != nil {
		return err
	}
	if logger != nil {
		logger.Info("login delivered",
			"agent_slug", req.AgentSlug, "cli_adapter", req.CLIAdapter, "credential_id", login.ID,
			"plan", d.PlanLabel(login.PlainValue), "path", d.File)
	}
	return nil
}

// removeLoginFile deletes a previously delivered login. Best-effort in the
// sense that a missing file is success (`rm -f`), but an exec failure is
// still reported: a login that should be gone and is not is the case the
// caller must hear about.
func removeLoginFile(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	agentSlug string,
	fileRel string,
	logger *slog.Logger,
) error {
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", "rm -f " + shellEscape(fileRel)},
		WorkingDir:  agentHomeDir(agentSlug),
		User:        "1001:1001",
	}
	if err := runOrBatch(ctx, container, "rm:"+fileRel, cfg); err != nil {
		return fmt.Errorf("remove %s: %w", fileRel, err)
	}
	if logger != nil {
		logger.Debug("login not on this run; stale file removed if present", "agent_slug", agentSlug, "path", fileRel)
	}
	return nil
}
