import { describe, expect, it } from "vitest"

import { buildOnboardingSetupBody, type OnboardingSetupInput } from "@/lib/onboarding-setup"

const base: OnboardingSetupInput = {
  workspaceName: "Acme Engineering",
  language: "Czech",
  crewSlug: "software-development",
  adapter: "CLAUDE_CODE",
  adapterLabel: "Claude Code",
  provider: "ANTHROPIC",
  envVar: "ANTHROPIC_API_KEY",
  model: "claude-sonnet-4-6",
  apiKey: "sk-ant-oat01-fake",
  pairingMode: true,
  telemetryOptIn: true,
}

describe("buildOnboardingSetupBody", () => {
  it("always carries the explicit telemetry consent answer", () => {
    // The wizard is a consent surface: both yes AND no must reach the
    // server as concrete booleans (the server only keeps its
    // version-based default when the field is omitted entirely).
    expect(buildOnboardingSetupBody({ ...base, telemetryOptIn: true })).toMatchObject({
      telemetry_opt_in: true,
    })
    expect(buildOnboardingSetupBody({ ...base, telemetryOptIn: false })).toMatchObject({
      telemetry_opt_in: false,
    })
  })

  it("routes template slugs into crew_template_slug and leaves blank-path fields unset", () => {
    const body = buildOnboardingSetupBody(base)
    expect(body.crew_template_slug).toBe("software-development")
    expect(body.crew_name).toBeUndefined()
    expect(body.agent_name).toBeUndefined()
  })

  it("expands the blank template into crew_name + adapter-derived agent_name", () => {
    const body = buildOnboardingSetupBody({ ...base, crewSlug: "blank" })
    expect(body.crew_template_slug).toBeUndefined()
    expect(body.crew_name).toBe("My Crew")
    expect(body.agent_name).toBe("Claude Code #1")
  })

  it("preserves the credential + pairing contract from the wizard", () => {
    const body = buildOnboardingSetupBody({ ...base, pairingMode: false })
    expect(body).toMatchObject({
      workspace_name: "Acme Engineering",
      preferred_language: "Czech",
      cli_adapter: "CLAUDE_CODE",
      llm_provider: "ANTHROPIC",
      llm_model: "claude-sonnet-4-6",
      credential_name: "ANTHROPIC_API_KEY",
      credential_value: "sk-ant-oat01-fake",
      pairing_mode: false,
    })
  })

  it("omits an empty model so the server applies the adapter default", () => {
    const body = buildOnboardingSetupBody({ ...base, model: "" })
    expect(body.llm_model).toBeUndefined()
  })
})
