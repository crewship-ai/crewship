import React from "react"
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

import { AccessNotice } from "../access-notice"
import { RestrictedRoutines } from "@/components/features/routines/restricted-routines"

// The restricted member's "no access" screen: it words the server's answer
// and lists only the screens the server allows; it never asks for more.
const api = vi.hoisted(() => vi.fn())
vi.mock("@/components/features/routines/restricted-routine-definitions", () => ({ RestrictedRoutineDefinitions: () => null }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: api }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { email: "viewer@example.com" } }, status: "authenticated" }) }))
afterEach(() => { cleanup(); api.mockReset() })

function json(body: unknown, status: number) { return new Response(JSON.stringify(body), { status }) }
const context = { workspaceName: "Acme", role: "VIEWER", surfaces: ["chat", "routines", "account_security"] as const }

describe("AccessNotice", () => {
  it("lists exactly the server's surfaces and marks the current one", () => {
    render(<AccessNotice kind="no-access" title="No routines shared with you yet" description="None." context={{ ...context, surfaces: [...context.surfaces] }} current="routines" />)
    expect(screen.getByRole("heading", { name: "No routines shared with you yet" })).toBeTruthy()
    expect(screen.getByText("Guest · Viewer · Acme")).toBeTruthy()
    expect(screen.getByRole("link", { name: /Routines/, current: "page" })).toBeTruthy()
    expect(screen.getByRole("link", { name: "Go to Chat" }).getAttribute("href")).toBe("/chat")
    expect(screen.queryByText("Pages")).toBeNull()
    expect(screen.getByText("viewer@example.com")).toBeTruthy()
  })

  it("offers a retry and no way out for a failed load", () => {
    const retry = vi.fn()
    render(<AccessNotice kind="unavailable" title="Routine catalog unavailable." description="Could not load." onRetry={retry} />)
    expect(screen.getByRole("alert").textContent).toBe("Routine catalog unavailable.")
    fireEvent.click(screen.getByRole("button", { name: "Try again" }))
    expect(retry).toHaveBeenCalledOnce()
  })
})

describe("restricted catalog retry", () => {
  it("re-reads the catalog on Check again and shows what was granted since", async () => {
    api.mockResolvedValueOnce(json([], 200)).mockResolvedValueOnce(json([{ slug: "allowed", name: "Allowed routine", definition_hash: "h", execution_hash: "e", inputs: [] }], 200))
    render(<RestrictedRoutines workspaceId="workspace" />)
    await screen.findByText("No routines can currently be run with your access. Shared definitions appear above.")
    fireEvent.click(screen.getByRole("button", { name: "Check again" }))
    await screen.findByRole("option", { name: "Allowed routine" })
    await waitFor(() => expect(api).toHaveBeenCalledTimes(2))
  })
})
