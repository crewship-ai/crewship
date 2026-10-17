// Sign out everywhere used the browser's own confirm() — one sentence in the
// browser's chrome, untestable and outside the page (POP-02). It is the
// shared confirmation now, and a refusal keeps it open.
import { readFileSync, readdirSync, statSync } from "node:fs"
import path from "node:path"
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, cleanup, waitFor, within } from "@testing-library/react"

const h = vi.hoisted(() => ({ apiFetch: vi.fn(), toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: (...a: unknown[]) => h.apiFetch(...a) }))
vi.mock("sonner", () => ({ toast: h.toast }))

import { UserSessions } from "../person-sessions"
import type { AdminUser } from "@/app/(dashboard)/admin/types"

const user = { id: "u1", email: "ada@example.com", full_name: "Ada Lovelace", created_at: "2026-01-01T00:00:00Z", workspace: null, role: null } as AdminUser
const SESSIONS = {
  sessions: [
    { id: "s1", created_at: "2026-10-01T10:00:00Z", last_used_at: "2026-10-16T10:00:00Z", expires_at: "2026-11-01T10:00:00Z", user_agent: "Mozilla/5.0 (Macintosh; Mac OS X) Chrome/130", ip: null },
    { id: "s2", created_at: "2026-10-02T10:00:00Z", last_used_at: "2026-10-16T11:00:00Z", expires_at: "2026-11-02T10:00:00Z", user_agent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1", ip: null },
  ],
  cli_tokens: [],
}
const revokeAll = () => h.apiFetch.mock.calls.filter(([u]) => String(u).includes("sessions/revoke-all"))

let refuse = false
beforeEach(() => {
  refuse = false
  Object.values(h.toast).forEach((f) => f.mockReset())
  h.apiFetch.mockReset()
  h.apiFetch.mockImplementation(async (u: string) => {
    if (String(u).includes("revoke-all")) {
      return refuse
        ? new Response(JSON.stringify({ error: "session store is down" }), { status: 503 })
        : new Response("{}", { status: 200 })
    }
    return new Response(JSON.stringify(SESSIONS), { status: 200 })
  })
})
afterEach(cleanup)

describe("Sign out everywhere", () => {
  it("sign out everywhere uses ConfirmDialog", async () => {
    const confirmSpy = vi.fn(() => true)
    vi.stubGlobal("confirm", confirmSpy)
    render(<UserSessions user={user} workspaceId="ws-1" isMe={false} onChanged={vi.fn()} />)
    fireEvent.click(await screen.findByRole("button", { name: "Sign out everywhere" }))
    const dialog = await screen.findByRole("alertdialog", { name: "Sign Ada Lovelace out of every device?" })
    expect(confirmSpy).not.toHaveBeenCalled()
    expect(dialog).toHaveTextContent("Their password stays the same")
    expect(revokeAll()).toHaveLength(0)
    fireEvent.click(within(dialog).getByRole("button", { name: "Sign out everywhere" }))
    await waitFor(() => expect(revokeAll()).toHaveLength(1))
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull())
    vi.unstubAllGlobals()
  })

  it("stays open when the server refuses", async () => {
    refuse = true
    render(<UserSessions user={user} workspaceId="ws-1" isMe={false} onChanged={vi.fn()} />)
    fireEvent.click(await screen.findByRole("button", { name: "Sign out everywhere" }))
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(within(dialog).getByRole("button", { name: "Sign out everywhere" }))
    await waitFor(() => expect(h.toast.error).toHaveBeenCalled())
    expect(screen.getByRole("alertdialog")).toBeInTheDocument()
  })
})

describe("Admin confirmations", () => {
  it("no admin page uses the browser's confirm()", () => {
    const root = path.resolve(__dirname, "../../../../..")
    const hits: string[] = []
    const walk = (dir: string) => {
      for (const name of readdirSync(dir)) {
        const p = path.join(dir, name)
        if (name === "__tests__" || name === "node_modules") continue
        if (statSync(p).isDirectory()) walk(p)
        else if (/\.tsx?$/.test(name) && /window\.confirm\(/.test(readFileSync(p, "utf8"))) hits.push(path.relative(root, p))
      }
    }
    walk(path.join(root, "components/features/admin"))
    walk(path.join(root, "app/(dashboard)/admin"))
    expect(hits).toEqual([])
  })
})
