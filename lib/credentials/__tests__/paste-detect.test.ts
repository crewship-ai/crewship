import { describe, expect, it } from "vitest"

import { detectPastedValue } from "../paste-detect"

// Shapes only — these are prefixes followed by filler, not real keys.
const STRIPE_LIKE = "sk_live_" + "x".repeat(24)
const ANTHROPIC_LIKE = "sk-ant-" + "y".repeat(24)

describe("paste detection", () => {
  it("sk_live_ → Stripe/API key/STRIPE_SECRET_KEY; sk-ant- suggests AI provider", () => {
    const stripe = detectPastedValue(STRIPE_LIKE)
    expect(stripe).toMatchObject({ provider: "STRIPE", itemType: "TOKEN", aiProvider: false, suggestedName: "Stripe secret key" })
    expect(stripe?.envVar).toMatch(/^STRIPE_/)

    const anthropic = detectPastedValue(ANTHROPIC_LIKE)
    expect(anthropic).toMatchObject({ provider: "ANTHROPIC", aiProvider: true })
  })

  it("says nothing about a value it does not recognise or a short fragment", () => {
    expect(detectPastedValue("hello world, not a key")).toBeNull()
    expect(detectPastedValue("sk_")).toBeNull()
    expect(detectPastedValue("")).toBeNull()
  })
})
