import { describe, it, expect } from "vitest"
import { integrationLabel, extractMissingIntegrations } from "@/lib/integration-labels"

describe("integrationLabel", () => {
  it("maps known brand slugs to canonical casing", () => {
    expect(integrationLabel("github")).toBe("GitHub")
    expect(integrationLabel("gitlab")).toBe("GitLab")
    expect(integrationLabel("slack")).toBe("Slack")
    expect(integrationLabel("hubspot")).toBe("HubSpot")
    expect(integrationLabel("openai")).toBe("OpenAI")
  })

  it("is case-insensitive and trims input", () => {
    expect(integrationLabel("  GitHub ")).toBe("GitHub")
    expect(integrationLabel("SLACK")).toBe("Slack")
  })

  it("title-cases unknown slugs, splitting on - and _", () => {
    expect(integrationLabel("hasura")).toBe("Hasura")
    expect(integrationLabel("google-calendar")).toBe("Google Calendar")
    expect(integrationLabel("some_new_app")).toBe("Some New App")
  })

  it("returns empty string for empty input", () => {
    expect(integrationLabel("")).toBe("")
  })
})

describe("extractMissingIntegrations", () => {
  it("pulls the missing_integrations extension member from a 422 body", () => {
    const body = {
      type: "about:blank",
      title: "Missing integration",
      status: 422,
      detail: "The executing crew is not connected to: slack",
      missing_integrations: ["slack", "github"],
    }
    expect(extractMissingIntegrations(body)).toEqual(["slack", "github"])
  })

  it("de-dupes, trims, and drops non-string entries", () => {
    const body = { missing_integrations: ["slack", " slack ", "", 42, "github"] }
    expect(extractMissingIntegrations(body)).toEqual(["slack", "github"])
  })

  it("returns [] when the field is absent or malformed", () => {
    expect(extractMissingIntegrations({})).toEqual([])
    expect(extractMissingIntegrations({ missing_integrations: "slack" })).toEqual([])
    expect(extractMissingIntegrations(null)).toEqual([])
    expect(extractMissingIntegrations("nope")).toEqual([])
  })
})
