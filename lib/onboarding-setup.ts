/**
 * buildOnboardingSetupBody — pure builder for the POST
 * /api/v1/onboarding/setup payload submitted by the web wizard
 * (app/(onboarding)/onboarding/page.tsx). Extracted from the page
 * component so the payload contract — including the explicit
 * `telemetry_opt_in` consent field the server persists via
 * crashreport.SetOptIn — is unit-testable without mounting the
 * wizard. `crewship setup` (cmd/crewship/cmd_setup.go) posts the
 * same shape to the same endpoint.
 */

export interface OnboardingSetupInput {
  workspaceName: string
  /** English language name stored verbatim in workspaces.preferred_language. */
  language: string
  /** Builtin crew template slug, "blank" for the single-agent path, or null. */
  crewSlug: string | null
  /** CLI adapter key (CLAUDE_CODE, GEMINI_CLI, …). */
  adapter: string
  /** Friendly adapter label, used to name the default agent on the blank path. */
  adapterLabel?: string
  /** LLM provider key matching the adapter (ANTHROPIC, GOOGLE, …). */
  provider?: string
  /** Env var name the credential is stored under (ANTHROPIC_API_KEY, …). */
  envVar?: string
  model: string
  apiKey: string
  /** True when the user picked "Pair my CLI" (drives how the human works, not the agents). */
  pairingMode: boolean
  /**
   * Explicit crash-reporting consent from the wizard's checkbox. Always
   * sent (true/false) — the wizard is the consent surface, so an answer
   * is always present. The server treats an omitted field (older
   * clients) as "keep the version-based default".
   */
  telemetryOptIn: boolean
  /**
   * Set once the setup agent's proposal card has actually been applied
   * (POST /onboarding/proposals/{id}/apply succeeded) — a real crew
   * already exists. When present, the server's applied_proposal_id
   * branch persists prefs/telemetry/completion and returns the crew the
   * proposal made real WITHOUT deploying anything else. Sending
   * crew_template_slug / crew_name / agent_name alongside it would make
   * the server deploy a SECOND crew, so this field and those three are
   * mutually exclusive by construction below.
   */
  appliedProposalId?: string
}

export function buildOnboardingSetupBody(input: OnboardingSetupInput): Record<string, unknown> {
  const { crewSlug, appliedProposalId } = input
  if (appliedProposalId) {
    // The crew already exists — Setup's job here is ONLY prefs +
    // telemetry + completion. No crew_template_slug, crew_name,
    // agent_name, or credential fields: those all feed the deploy path,
    // and this call must not deploy.
    return {
      workspace_name: input.workspaceName,
      preferred_language: input.language,
      applied_proposal_id: appliedProposalId,
      telemetry_opt_in: input.telemetryOptIn,
    }
  }
  const blank = crewSlug === "blank"
  return {
    workspace_name: input.workspaceName,
    preferred_language: input.language,
    crew_template_slug: crewSlug && !blank ? crewSlug : undefined,
    crew_name: blank ? "My Crew" : undefined,
    agent_name: blank ? `${input.adapterLabel ?? "Agent"} #1` : undefined,
    cli_adapter: input.adapter,
    llm_provider: input.provider,
    llm_model: input.model || undefined,
    credential_name: input.envVar,
    // API key is always sent — agents need it regardless of browser vs
    // CLI mode. Pairing mode just decides how the human drives Crewship.
    credential_value: input.apiKey,
    pairing_mode: input.pairingMode,
    telemetry_opt_in: input.telemetryOptIn,
  }
}
