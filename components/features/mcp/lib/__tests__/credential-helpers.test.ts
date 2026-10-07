import { describe, expect, it } from "vitest"
import { deriveCredentialName, isCredentialRef } from "../credential-helpers"

describe("MCP credential references", () => {
  it.each(["${TOKEN}", "${GITHUB_TOKEN2}", "${_PRIVATE}", "${A}"])("accepts a complete reference %s", (value) => {
    expect(isCredentialRef(value)).toBe(true)
  })

  it.each(["", "TOKEN", "${token}", "${2TOKEN}", "${TOKEN-NAME}", "${}", "$TOKEN", " ${TOKEN}", "${TOKEN} suffix", "prefix ${TOKEN}", "${TOKEN}${OTHER}", "${TOKEN\n}"])("keeps a non-reference literal %s", (value) => {
    expect(isCredentialRef(value)).toBe(false)
  })

  it.each(["\n", "\r", "\r\n", "\u2028", "\u2029"])("rejects a reference followed by a line terminator %j", (suffix) => {
    expect(isCredentialRef("${TOKEN}" + suffix)).toBe(false)
  })

  it.each([["GITHUB_TOKEN", "github-token"], ["GOOGLE_OAUTH_CLIENT_ID", "google-oauth-client-id"], ["_PRIVATE2", "-private2"], ["TOKEN", "token"]])("maps environment key %s to credential name %s", (key, name) => {
    expect(deriveCredentialName(key)).toBe(name)
  })
})
