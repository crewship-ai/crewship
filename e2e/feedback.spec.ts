import { test, expect } from "@playwright/test"

// Feedback API end-to-end against a running daemon (dev VM or local
// `pnpm dev`). Uses Playwright's request fixture so we exercise the
// actual HTTP stack including CSRF + session cookie + the auth
// middleware, not just an in-process handler call. Covers the
// contract that the chat UI relies on: POST creates, GET lists,
// DELETE removes, signal enum is enforced, and the body cap fires before
// the per-field cap. Cross-user privacy is covered in the Go API tests.
//
// The test runs against PLAYWRIGHT_BASE_URL when set (dev VM mode)
// or the locally-spawned Next.js dev server otherwise. The frontend
// proxies /api/v1/* to the backend so we don't need a separate
// CREWSHIP_BACKEND_URL env.
//
// Fixture messages are persisted in the authenticated workspace without an LLM.
test.describe("Feedback API", () => {
  async function seedMessage(context: import("@playwright/test").BrowserContext, baseURL: string | undefined) {
    const workspaces = await (await context.request.get(`${baseURL}/api/v1/workspaces`)).json()
    const workspaceID: string = Array.isArray(workspaces) ? workspaces[0]?.id : workspaces.id
    expect(workspaceID).toBeTruthy()
    const response = await context.request.post(
      `${baseURL}/api/v1/e2e/fixtures/feedback-message?workspace_id=${encodeURIComponent(workspaceID)}`,
    )
    expect(response.status()).toBe(201)
    const { message_id } = await response.json()
    return message_id as string
  }

  test("POST creates a row + GET returns it + DELETE removes it", async ({ context, baseURL }) => {
    const messageID = await seedMessage(context, baseURL)

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
    const messageID = await seedMessage(context, baseURL)

    const first = await context.request.post(`${baseURL}/api/v1/feedback`, {
      data: { message_id: messageID, signal: "not_helpful", reason: "first" },
    })
    expect(first.status()).toBe(201)
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
