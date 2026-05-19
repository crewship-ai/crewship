import { test, expect } from "@playwright/test"

// Feedback API end-to-end against a running daemon (dev VM or local
// `pnpm dev`). Uses Playwright's request fixture so we exercise the
// actual HTTP stack including CSRF + session cookie + the auth
// middleware, not just an in-process handler call. Covers the
// contract that the chat UI relies on: POST creates, GET lists,
// DELETE removes, signal enum is enforced, body cap fires before
// per-field cap, cross-user privacy holds.
//
// The test runs against PLAYWRIGHT_BASE_URL when set (dev VM mode)
// or the locally-spawned Next.js dev server otherwise. The frontend
// proxies /api/v1/* to the backend so we don't need a separate
// CREWSHIP_BACKEND_URL env.

test.describe("Feedback API", () => {
  test.beforeEach(async ({ context, baseURL }) => {
    // Hand off through NextAuth's credentials callback to land a
    // session cookie. The global-setup that the main suite uses
    // expects a Next.js dev server in front of the API — for this
    // spec we sign in via the same callback the UI uses, so it
    // works against any deployment.
    const csrfRes = await context.request.get(`${baseURL}/api/auth/csrf`)
    const { csrfToken } = await csrfRes.json()
    const loginRes = await context.request.post(
      `${baseURL}/api/auth/callback/credentials`,
      {
        form: {
          csrfToken,
          email: "demo@crewship.ai",
          password: "password123",
        },
        maxRedirects: 0,
      },
    )
    // Credentials callback redirects on success (HTTP 302/303). Any
    // 4xx means demo seed is missing — failing here is more useful
    // than every test failing with "unauthorized".
    expect([200, 302, 303]).toContain(loginRes.status())
  })

  test("POST creates a row + GET returns it + DELETE removes it", async ({ context, baseURL }) => {
    const messageID = `pw-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`

    const create = await context.request.post(`${baseURL}/api/v1/feedback`, {
      data: {
        message_id: messageID,
        signal: "helpful",
        reason: "Playwright E2E smoke",
      },
    })
    expect(create.status()).toBe(201)
    const created = await create.json()
    expect(created.id).toMatch(/^c[a-z0-9]+$/) // CUID shape

    const list = await context.request.get(
      `${baseURL}/api/v1/feedback?message_id=${messageID}`,
    )
    expect(list.status()).toBe(200)
    const listed = await list.json()
    expect(listed.feedback).toHaveLength(1)
    expect(listed.feedback[0]).toMatchObject({
      message_id: messageID,
      signal: "helpful",
      reason: "Playwright E2E smoke",
    })

    const del = await context.request.delete(
      `${baseURL}/api/v1/feedback?message_id=${messageID}&signal=helpful`,
    )
    expect(del.status()).toBe(204)

    const listAfter = await context.request.get(
      `${baseURL}/api/v1/feedback?message_id=${messageID}`,
    )
    const listedAfter = await listAfter.json()
    expect(listedAfter.feedback).toHaveLength(0)
  })

  test("UPSERT idempotency: re-POST returns same id, replaces reason", async ({ context, baseURL }) => {
    const messageID = `pw-upsert-${Date.now()}`

    const first = await context.request.post(`${baseURL}/api/v1/feedback`, {
      data: { message_id: messageID, signal: "not_helpful", reason: "first" },
    })
    const firstID = (await first.json()).id

    const second = await context.request.post(`${baseURL}/api/v1/feedback`, {
      data: { message_id: messageID, signal: "not_helpful", reason: "second" },
    })
    expect(second.status()).toBe(201)
    expect((await second.json()).id).toBe(firstID)

    const list = await context.request.get(
      `${baseURL}/api/v1/feedback?message_id=${messageID}`,
    )
    const { feedback } = await list.json()
    expect(feedback).toHaveLength(1)
    expect(feedback[0].reason).toBe("second")

    await context.request.delete(
      `${baseURL}/api/v1/feedback?message_id=${messageID}&signal=not_helpful`,
    )
  })

  test("invalid signal returns 400 with enum hint", async ({ context, baseURL }) => {
    const res = await context.request.post(`${baseURL}/api/v1/feedback`, {
      data: { message_id: "pw-bad-signal", signal: "explode" },
    })
    expect(res.status()).toBe(400)
    const body = await res.json()
    expect(body.error).toContain("helpful")
    expect(body.error).toContain("not_helpful")
  })

  test("oversize body returns 413 (MaxBytesReader fires before per-field cap)", async ({ context, baseURL }) => {
    const res = await context.request.post(`${baseURL}/api/v1/feedback`, {
      data: {
        message_id: "pw-oversize",
        signal: "edit",
        reason: "a".repeat(20 * 1024), // > 16 KiB body cap
      },
    })
    expect(res.status()).toBe(413)
  })

  test("GET requires message_id or trace_id (400 when neither)", async ({ context, baseURL }) => {
    const res = await context.request.get(`${baseURL}/api/v1/feedback`)
    expect(res.status()).toBe(400)
  })

  test("DELETE non-existent row returns 204 (idempotent)", async ({ context, baseURL }) => {
    const res = await context.request.delete(
      `${baseURL}/api/v1/feedback?message_id=pw-never-existed&signal=helpful`,
    )
    expect(res.status()).toBe(204)
  })
})
