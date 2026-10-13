import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { ArtifactCreationControl } from "../artifact-creation-control"
import { apiFetch } from "@/lib/api-fetch"
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
afterEach(() => { cleanup(); vi.clearAllMocks() })
it("resolves the live run from the server and grants only after explicit consent, then revokes", async () => {
  let authorized = false
  const writes: { path: string; method: string; body: unknown }[] = []
  vi.mocked(apiFetch).mockImplementation(async (url, options) => {
    if (options?.method) {
      writes.push({ path: String(url), method: options.method, body: options.body })
      authorized = options.method === "POST"
      return new Response("{}")
    }
    return new Response(JSON.stringify({ run_id: "trusted-run", can_authorize: true, authorized, remaining: 1, revoked: false, expires_at: "2026-10-13T15:00:00Z" }))
  })
  render(<ArtifactCreationControl workspaceId="ws" sessionId="chat" isStreaming={false} />)
  const allow = await screen.findByRole("button", { name: "Allow artifact creation" })
  expect(writes).toHaveLength(0)
  fireEvent.click(allow)
  await screen.findByRole("button", { name: "Revoke artifact creation" })
  expect(writes[0]).toEqual({ path: "/api/v1/artifact-creation-authorizations/trusted-run?workspace_id=ws", method: "POST", body: "{}" })
  fireEvent.click(screen.getByRole("button", { name: "Revoke artifact creation" }))
  await waitFor(() => expect(writes).toHaveLength(2))
  expect(writes[1].method).toBe("DELETE")
})
it("does not expose permission controls for streaming without a trusted eligible run", async () => {
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({ run_id: "", can_authorize: false, authorized: false, remaining: 0, expires_at: "", revoked: false })))
  render(<ArtifactCreationControl workspaceId="ws" sessionId="chat" isStreaming />)
  await waitFor(() => expect(apiFetch).toHaveBeenCalled())
  expect(screen.queryByRole("button")).toBeNull()
})
it("rechecks a grant after a lost response rather than sending a second opt-in", async () => {
  let authorized = false
  let posts = 0
  vi.mocked(apiFetch).mockImplementation(async (_url, options) => {
    if (options?.method === "POST") { authorized = true; posts++; throw new Error("response lost") }
    return new Response(JSON.stringify({ run_id: "trusted-run", can_authorize: true, authorized, remaining: 1, revoked: false, expires_at: "" }))
  })
  render(<ArtifactCreationControl workspaceId="ws" sessionId="chat" isStreaming={false} />)
  fireEvent.click(await screen.findByRole("button", { name: "Allow artifact creation" }))
  await screen.findByRole("button", { name: "Revoke artifact creation" })
  expect(posts).toBe(1)
})
