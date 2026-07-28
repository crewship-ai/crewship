import { describe, it, expect } from "vitest"
import { credentialTypeLabel, extractMissingCredentials } from "@/lib/credential-labels"
import { extractProblemDetail } from "@/lib/problem-details"

describe("credentialTypeLabel", () => {
  it("maps known vault types to human labels", () => {
    expect(credentialTypeLabel("api_key")).toBe("API key")
    expect(credentialTypeLabel("ai_cli_token")).toBe("CLI token")
    expect(credentialTypeLabel("oauth2")).toBe("OAuth credential")
  })

  it("is case-insensitive and trims input", () => {
    expect(credentialTypeLabel("  API_KEY ")).toBe("API key")
  })

  it("title-cases unknown types, splitting on - and _", () => {
    expect(credentialTypeLabel("webhook_secret")).toBe("Webhook Secret")
  })

  it("returns empty string for empty input", () => {
    expect(credentialTypeLabel("")).toBe("")
  })
})

describe("extractMissingCredentials", () => {
  it("pulls the missing_credentials extension member from a 422 body", () => {
    const body = {
      status: 422,
      detail: 'routine requires credential of type "api_key" not present in the vault for crew "Ops"',
      missing_credentials: ["api_key"],
    }
    expect(extractMissingCredentials(body)).toEqual(["api_key"])
  })

  it("de-dupes, trims, and drops non-string entries", () => {
    const body = { missing_credentials: ["api_key", " api_key ", "", 7, "stripe"] }
    expect(extractMissingCredentials(body)).toEqual(["api_key", "stripe"])
  })

  it("returns [] when the field is absent or malformed", () => {
    expect(extractMissingCredentials({})).toEqual([])
    expect(extractMissingCredentials({ missing_credentials: "api_key" })).toEqual([])
    expect(extractMissingCredentials(null)).toEqual([])
  })
})

describe("extractProblemDetail", () => {
  it("returns the detail string when present", () => {
    expect(extractProblemDetail({ detail: "boom" })).toBe("boom")
  })

  it("returns undefined when absent or non-string", () => {
    expect(extractProblemDetail({})).toBeUndefined()
    expect(extractProblemDetail({ detail: 42 })).toBeUndefined()
    expect(extractProblemDetail(null)).toBeUndefined()
  })
})
