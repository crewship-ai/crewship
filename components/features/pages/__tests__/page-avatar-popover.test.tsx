/**
 * The page's avatar tile as a control (#2563) — the routine header's
 * pattern: click, pick, saved on the spot, one PATCH per pick carrying only
 * that field, and the list invalidated so the rail redraws.
 */
import { describe, it, expect, vi, beforeEach } from "vitest"
import React from "react"
import { render, screen, fireEvent, cleanup, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), message: vi.fn() } }))
import { toast } from "sonner"

import { PageAvatarPopover } from "@/components/features/pages/page-avatar-popover"
import { pagesKeys } from "@/hooks/use-pages"

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
}

function mount(over: Partial<React.ComponentProps<typeof PageAvatarPopover>> = {}, patch?: Response) {
  const calls: Array<{ method: string; url: string; body: unknown }> = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const method = (init?.method ?? "GET").toUpperCase()
      calls.push({ method, url: String(input), body: init?.body ? JSON.parse(String(init.body)) : null })
      if (method === "PATCH") return patch ?? jsonResponse(200, { slug: "fleet" })
      return jsonResponse(404, {})
    }),
  )
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  const invalidate = vi.spyOn(qc, "invalidateQueries")
  render(
    <QueryClientProvider client={qc}>
      <PageAvatarPopover workspaceId="ws-1" slug="fleet" icon={null} color={null} mayEdit {...over} />
    </QueryClientProvider>,
  )
  return { calls, invalidate }
}

const tile = () => document.querySelector("[data-slot='page-avatar-default'], [data-slot='page-avatar-popover'] > button > div")

describe("PageAvatarPopover", () => {
  beforeEach(() => {
    cleanup()
    vi.mocked(toast.error).mockClear()
  })

  it("is a plain tile for a viewer who may not edit — no button at all", () => {
    mount({ mayEdit: false, icon: "rocket", color: "amber" })
    expect(screen.queryByRole("button", { name: "Change page icon" })).toBeNull()
  })

  it("keeps the neutral tile as the trigger for a page without an avatar", () => {
    mount()
    const button = screen.getByRole("button", { name: "Change page icon" })
    expect(button.querySelector("[data-slot='page-avatar-default']")).toBeTruthy()
  })

  it("saves an icon pick on the spot with one PATCH carrying only the icon, and invalidates the list", async () => {
    const { calls, invalidate } = mount()
    fireEvent.click(screen.getByRole("button", { name: "Change page icon" }))
    fireEvent.click(await screen.findByTitle("Rocket"))

    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1))
    const patch = calls.find((c) => c.method === "PATCH")!
    expect(patch.url).toContain("/api/v1/pages/fleet")
    expect(patch.body).toEqual({ icon: "rocket" })
    // The tile shows the pick, and no longer as the default.
    await waitFor(() => expect(tile()?.getAttribute("data-slot")).not.toBe("page-avatar-default"))
    await waitFor(() =>
      expect(invalidate).toHaveBeenCalledWith(expect.objectContaining({ queryKey: pagesKeys.list("ws-1") })),
    )
  })

  it("saves a colour pick with one PATCH carrying only the colour", async () => {
    const { calls } = mount({ icon: "rocket" })
    fireEvent.click(screen.getByRole("button", { name: "Change page icon" }))
    fireEvent.click(await screen.findByRole("button", { name: "Choose amber color" }))
    await waitFor(() => expect(calls.filter((c) => c.method === "PATCH")).toHaveLength(1))
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ color: "amber" })
  })

  it("puts the previous avatar back and says the server's words when refused", async () => {
    mount({ icon: "rocket", color: "amber" }, jsonResponse(400, { error: "icon unicorn is not a crew icon" }))
    fireEvent.click(screen.getByRole("button", { name: "Change page icon" }))
    fireEvent.click(await screen.findByTitle("Shield"))
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(expect.stringMatching(/not a crew icon/)))
    // Back to the rocket: the trigger's glyph is the rocket again.
    const button = screen.getByRole("button", { name: "Change page icon" })
    await waitFor(() => expect(button.querySelector("svg.lucide-rocket, svg.lucide-shield")?.classList.contains("lucide-rocket")).toBe(true))
  })
})
