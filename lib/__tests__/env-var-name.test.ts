import { describe, it, expect } from "vitest"
import { isValidEnvVarName, suggestEnvVarName } from "@/lib/env-var-name"

describe("isValidEnvVarName", () => {
  it.each([
    "STRIPE_API_KEY",
    "GH_TOKEN",
    "_INTERNAL",
    "A",
    "AWS_ACCESS_KEY_ID",
    "KEY2",
  ])("accepts %s", (name) => {
    expect(isValidEnvVarName(name)).toBe(true)
  })

  it.each([
    "stripe_api_key", // lowercase
    "Stripe_Key",     // mixed case
    "2FA_TOKEN",      // leading digit
    "MY KEY",         // space
    "MY-KEY",         // dash
    "MY.KEY",         // dot
    "KEY!",           // punctuation
    "",               // empty
    " STRIPE ",       // surrounding whitespace is not the caller's job to hide
  ])("rejects %j", (name) => {
    expect(isValidEnvVarName(name)).toBe(false)
  })
})

describe("suggestEnvVarName", () => {
  it("uppercases a lowercase name", () => {
    expect(suggestEnvVarName("stripe_api_key")).toBe("STRIPE_API_KEY")
  })

  it("converts spaces, dashes and dots to underscores", () => {
    expect(suggestEnvVarName("stripe api-key.prod")).toBe("STRIPE_API_KEY_PROD")
  })

  it("strips characters that can never appear in an env var name", () => {
    expect(suggestEnvVarName("my key! (prod)")).toBe("MY_KEY_PROD")
  })

  it("collapses runs of separators", () => {
    expect(suggestEnvVarName("my --  key")).toBe("MY_KEY")
  })

  it("prefixes a leading digit with an underscore", () => {
    expect(suggestEnvVarName("2fa token")).toBe("_2FA_TOKEN")
  })

  it("trims leading/trailing separators produced by normalisation", () => {
    expect(suggestEnvVarName("  (stripe key)  ")).toBe("STRIPE_KEY")
  })

  it("returns null when nothing salvageable remains", () => {
    expect(suggestEnvVarName("!!!")).toBeNull()
    expect(suggestEnvVarName("")).toBeNull()
    expect(suggestEnvVarName("   ")).toBeNull()
  })

  it("returns the input unchanged when already valid", () => {
    expect(suggestEnvVarName("GH_TOKEN")).toBe("GH_TOKEN")
  })

  it("always returns a valid name or null (property check)", () => {
    const samples = [
      "stripe key", "žluťoučký kůň", "9lives", "a.b.c", "hello-world",
      "___", "-", "ANTHROPIC_API_KEY", "foo__bar", "x y z 1 2 3",
    ]
    for (const s of samples) {
      const out = suggestEnvVarName(s)
      if (out !== null) expect(isValidEnvVarName(out)).toBe(true)
    }
  })
})
