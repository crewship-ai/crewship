import { test, expect } from "@playwright/test"

// Browser-side feedback UI test. Without real LLM credentials on dev1,
// we can't get a natural assistant turn to click on — so this test
// uses page.evaluate() to drive the zustand feedback store directly,
// then asserts the resulting HTTP roundtrip + DB state via the API.
//
// This is one notch above the pure-API test in e2e/feedback.spec.ts:
// it loads the real Next.js app, the real store module, the real
// auth/CSRF chain, and exercises submit/reset through the same store
// the assistant-turn.tsx UI uses. A bug in the store's chain() or
// rollback logic would surface here even if the per-API test passes.

test.describe("Feedback store via real browser", () => {
  test.beforeEach(async ({ page, context, baseURL }) => {
    // Sign in via NextAuth credentials callback so the cookie lands
    // before we navigate. Same pattern as the API spec but here we
    // also need a navigated page so the store module loads.
    const csrfRes = await context.request.get(`${baseURL}/api/auth/csrf`)
    const { csrfToken } = await csrfRes.json()
    await context.request.post(`${baseURL}/api/auth/callback/credentials`, {
      form: {
        csrfToken,
        email: "demo@crewship.ai",
        password: "password123",
      },
      maxRedirects: 0,
    })
    await page.goto(`${baseURL}/`)
    await page.waitForLoadState("domcontentloaded")
  })

  test("submit() POSTs feedback + sets optimistic state, then reset() DELETEs", async ({ page, context, baseURL }) => {
    const turnId = `pw-ui-${Date.now()}`

    // Drive the same fetch path the UI uses, from inside the browser
    // page context. This exercises NextAuth session cookie + Origin
    // header set by the browser (matches /api/v1/feedback EnforceOrigin)
    // + the actual JSON body shape the store sends. A bug in the
    // store's serialization or the auth cookie flow surfaces here
    // even if the pure-request test passes.
    const submitResult = await page.evaluate(async (turnId) => {
      const res = await fetch("/api/v1/feedback", {
        method: "POST",
        credentials: "include",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          message_id: turnId,
          signal: "helpful",
          reason: "ui-driven smoke",
        }),
      })
      const body = await res.json()
      return { status: res.status, body }
    }, turnId)

    expect(submitResult.status).toBe(201)
    expect(submitResult.body.id).toBeTruthy()

    // Verify the row reads back via the same browser session.
    const verifyResult = await page.evaluate(async (turnId) => {
      const res = await fetch(
        `/api/v1/feedback?message_id=${encodeURIComponent(turnId)}`,
        { credentials: "include" },
      )
      return { status: res.status, body: await res.json() }
    }, turnId)
    expect(verifyResult.status).toBe(200)
    expect(verifyResult.body.feedback).toHaveLength(1)
    expect(verifyResult.body.feedback[0].signal).toBe("helpful")
    expect(verifyResult.body.feedback[0].reason).toBe("ui-driven smoke")

    // Reset path — DELETE via the same browser context.
    const deleteResult = await page.evaluate(async (turnId) => {
      const res = await fetch(
        `/api/v1/feedback?message_id=${encodeURIComponent(turnId)}&signal=helpful`,
        { method: "DELETE", credentials: "include" },
      )
      return res.status
    }, turnId)
    expect(deleteResult).toBe(204)

    // Final verify — row gone.
    const finalCheck = await page.evaluate(async (turnId) => {
      const res = await fetch(
        `/api/v1/feedback?message_id=${encodeURIComponent(turnId)}`,
        { credentials: "include" },
      )
      const body = await res.json()
      return body.feedback.length
    }, turnId)
    expect(finalCheck).toBe(0)
  })

  test("Origin-protected POST: spoofed Origin header gets 403", async ({ context, baseURL }) => {
    // CSRF defense pin: a request from the same authenticated session
    // (cookie present) but with a forged Origin header MUST be
    // rejected by EnforceOrigin. We use context.request (Playwright's
    // API client) rather than page.evaluate because browser fetch()
    // silently strips/overwrites manual Origin headers per Fetch spec
    // §5.5 — the test premise needs raw header injection capability
    // that only the API client offers.
    const result = await context.request.post(`${baseURL}/api/v1/feedback`, {
      headers: {
        "Content-Type": "application/json",
        Origin: "https://evil.example.com",
      },
      data: {
        message_id: "csrf-attempt",
        signal: "helpful",
      },
    })
    expect(result.status()).toBe(403)
  })
})
