import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

const api = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

import { UserSessions } from "../person-sessions"
import type { AdminUser } from "@/app/(dashboard)/admin/types"

// #3342 — "Sign out everywhere" asks in the page; Cancel signs nobody out.

const user = { id: "usr_ana", email: "ana@example.test", full_name: "Ana Novak" } as AdminUser
const sessions = {
  sessions: [{ id: "s1", created_at: "2026-10-01T10:00:00Z", last_used_at: "2026-10-01T10:00:00Z", expires_at: "2026-11-01T10:00:00Z", user_agent: "Mozilla/5.0 (Macintosh; Mac OS X) Chrome/120", ip: "10.0.0.2" }],
  cli_tokens: [],
}
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
const revokeAll = () => api.mock.calls.filter(([url, init]) => init?.method === "POST" && String(url).includes("/sessions/revoke-all"))

async function answer(label: string) {
  const dialog = await screen.findByRole("alertdialog", { name: "Sign Ana Novak out of every device?" })
  fireEvent.click(within(dialog).getByRole("button", { name: label }))
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument())
  return dialog
}

beforeEach(() => {
  api.mockReset()
  api.mockImplementation(async (_url: string, init?: RequestInit) => (init?.method === "POST" ? json({}) : json(sessions)))
})

describe("Sign out everywhere", () => {
  it("asks first; Cancel signs nobody out, confirm revokes every session", async () => {
    const onChanged = vi.fn()
    render(<UserSessions user={user} workspaceId="ws" isMe={false} onChanged={onChanged} />)
    fireEvent.click(await screen.findByRole("button", { name: "Sign out everywhere" }))
    const dialog = await answer("Cancel")
    expect(dialog).toHaveTextContent("Their password stays the same")
    expect(revokeAll()).toHaveLength(0)

    fireEvent.click(screen.getByRole("button", { name: "Sign out everywhere" }))
    await answer("Sign out everywhere")
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(String(revokeAll()[0][0])).toBe("/api/v1/admin/users/usr_ana/sessions/revoke-all?workspace_id=ws")
  })
})
