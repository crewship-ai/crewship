import { test, expect } from "@playwright/test"

// Browser-side feedback transport test. A gated fixture creates a real
// assistant message without an LLM, then page.evaluate() drives the
// authenticated browser fetch path and checks the HTTP roundtrip.
//
// This exercises the Next.js app and real auth/CSRF chain. It does not
// click an assistant turn or call the zustand store; those need separate
// UI coverage.
//
// A gated E2E fixture supplies a real assistant message for the HTTP roundtrip.
test.describe("Feedback HTTP via real browser", () => {
  test.beforeEach(async ({ page, baseURL }) => {
    await page.goto(`${baseURL}/`)
    await page.waitForLoadState("domcontentloaded")
  })

  test("browser POSTs feedback and then DELETEs it", async ({ page, context, baseURL }) => {
    const workspaces = await (await context.request.get(`${baseURL}/api/v1/workspaces`)).json()
    const workspaceID: string = Array.isArray(workspaces) ? workspaces[0]?.id : workspaces.id
    expect(workspaceID).toBeTruthy()
    const fixture = await context.request.post(
      `${baseURL}/api/v1/e2e/fixtures/feedback-message?workspace_id=${encodeURIComponent(workspaceID)}`,
    )
    expect(fixture.status()).toBe(201)
    const { message_id: turnId } = await fixture.json()

    // Browser fetch exercises the session cookie and Origin header that
    // /api/v1/feedback's EnforceOrigin checks.
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
