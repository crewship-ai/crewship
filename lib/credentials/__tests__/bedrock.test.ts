import { describe, expect, it } from "vitest"

import {
  BEDROCK_PROFILES,
  BEDROCK_REGIONS,
  bedrockAccountModels,
  bedrockAgentModel,
  bedrockModelFor,
  bedrockModelLabel,
  bedrockNativeModel,
} from "../bedrock"

describe("bedrockModelLabel", () => {
  it.each([
    ["eu.anthropic.claude-opus-5-5", "Claude Opus 5.5 · EU"],
    ["amazon-bedrock/eu.anthropic.claude-sonnet-5-5", "Claude Sonnet 5.5 · EU"],
    ["global.anthropic.claude-haiku-5-5", "Claude Haiku 5.5 · Global"],
    ["eu.anthropic.claude-haiku-4-5-20251001-v1:0", "Claude Haiku 4.5 · EU"],
    ["eu.amazon.nova-pro-v1:0", "Nova Pro · EU"],
    ["anthropic.claude-sonnet-4-6", "Claude Sonnet 4.6"],
    ["us.meta.llama4-maverick-17b-instruct-v1:0", "Llama4 Maverick 17b Instruct · US"],
  ])("%s → %s", (id, label) => {
    expect(bedrockModelLabel(id)).toBe(label)
  })

  it("returns an empty string for an empty id", () => {
    expect(bedrockModelLabel("")).toBe("")
  })
})

describe("agent model ids", () => {
  it("adds and strips the OpenCode provider prefix", () => {
    expect(bedrockAgentModel("eu.anthropic.claude-opus-5-5")).toBe("amazon-bedrock/eu.anthropic.claude-opus-5-5")
    expect(bedrockAgentModel("amazon-bedrock/eu.anthropic.claude-opus-5-5")).toBe("amazon-bedrock/eu.anthropic.claude-opus-5-5")
    expect(bedrockNativeModel("amazon-bedrock/eu.anthropic.claude-opus-5-5")).toBe("eu.anthropic.claude-opus-5-5")
    expect(bedrockNativeModel("eu.anthropic.claude-opus-5-5")).toBe("eu.anthropic.claude-opus-5-5")
  })
})

describe("bedrockModelFor", () => {
  const seat = { login: { bedrock: { region: "eu-central-1", models: ["eu.anthropic.claude-sonnet-5-5", "eu.anthropic.claude-opus-5-5"], auth_method: "bedrock_api_key" as const, eu_only: true } } }

  it("keeps the agent's model when the account permits it", () => {
    expect(bedrockModelFor(seat, "amazon-bedrock/eu.anthropic.claude-opus-5-5")).toBe("eu.anthropic.claude-opus-5-5")
  })

  it("falls back to the account's first model otherwise", () => {
    expect(bedrockModelFor(seat, "claude-opus-5-5")).toBe("eu.anthropic.claude-sonnet-5-5")
    expect(bedrockModelFor(seat, "")).toBe("eu.anthropic.claude-sonnet-5-5")
  })

  it("returns an empty string for an account without models", () => {
    expect(bedrockModelFor({ login: null }, "x")).toBe("")
    expect(bedrockAccountModels({ login: { bedrock: undefined } })).toEqual([])
  })
})

describe("catalog", () => {
  it("offers only regions the server has a runtime endpoint for, EU first", () => {
    // internal/bedrock/account.go Endpoint() — a region outside this list is refused there.
    const server = ["ap-northeast-1", "ap-northeast-2", "ap-northeast-3", "ap-south-1", "ap-south-2", "ap-southeast-1", "ap-southeast-2", "ca-central-1", "eu-central-1", "eu-central-2", "eu-north-1", "eu-south-1", "eu-south-2", "eu-west-1", "eu-west-2", "eu-west-3", "sa-east-1", "us-east-1", "us-east-2", "us-west-2"]
    expect([...BEDROCK_REGIONS.map((r) => r.id)].sort()).toEqual(server)
    const firstNonEU = BEDROCK_REGIONS.findIndex((r) => !r.eu)
    expect(BEDROCK_REGIONS.slice(firstNonEU).every((r) => !r.eu)).toBe(true)
  })

  it("lists unique profile ids the server's model pattern accepts", () => {
    expect(new Set(BEDROCK_PROFILES).size).toBe(BEDROCK_PROFILES.length)
    for (const id of BEDROCK_PROFILES) expect(id).toMatch(/^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/)
    expect(BEDROCK_PROFILES.some((id) => id.startsWith("eu."))).toBe(true)
  })
})
