import { cleanup, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { apiFetch } from "@/lib/api-fetch"
import type { CraftLeaseStatus } from "../artifact-context/projects/types"
import { SavedDocumentPanel, type SavedDocumentPanelProps } from "./saved-document-panel"

// The native Craft lease on a saved Word document opened from chat: who edits, and Request edit for everyone else.
const lease = vi.hoisted(() => ({ status: undefined as CraftLeaseStatus | undefined, heldByMe: false, slugs: [] as (string | null)[], request: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: () => {} }))
vi.mock("@/components/features/pages/page-document", () => ({ PageDocument: () => <div data-testid="page-document" /> }))
vi.mock("../artifact-context/craft/use-craft-lease", () => ({
  useCraftLeaseState: (_ws: string, slug: string | null) => {
    lease.slugs.push(slug)
    return { status: lease.status, heldByMe: lease.heldByMe, requestedByMe: false, requestedGeneration: null,
      requestHandoff: { mutate: lease.request, isPending: false, isError: false } }
  },
}))
const response = (value: unknown) => new Response(JSON.stringify(value), { status: 200 })
const props = (): SavedDocumentPanelProps => ({ workspaceId: "ws1", chatId: "chat1", currentUserId: "human1", activationKey: "open1", enabled: true,
  target: { workspaceId: "ws1", pageId: "page1", type: "document", mode: "latest" }, layout: "side", expanded: false, onClose: vi.fn(), onExpandedChange: vi.fn() })
const base: CraftLeaseStatus = { holder: null, mine: false, generation: "3", can_write: true, requests: [] }
const pavel = { actor_id: "user:pavel", display_name: "Pavel", kind: "human" as const }
beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_CRAFT_LEASE", "true")
  lease.status = undefined; lease.heldByMe = false; lease.slugs = []; lease.request.mockReset()
  vi.mocked(apiFetch).mockReset().mockImplementation(async url => response(String(url).includes("/document?")
    ? { page_id: "page1", schema: 3, rev: 4, content: { type: "doc", content: [{ type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text: "Saved body" }] }] } }
    : { id: "page1", name: "Launch plan", has_document: true }))
})
afterEach(() => { cleanup(); vi.unstubAllEnvs() })

it("with the flag off the panel asks nothing about Craft and keeps its own Edit here", async () => {
  vi.stubEnv("NEXT_PUBLIC_CHAT_CRAFT_LEASE", "false")
  lease.status = base
  render(<SavedDocumentPanel {...props()} />)
  expect(await screen.findByRole("button", { name: "Edit here" })).toBeInTheDocument()
  expect(screen.queryByTestId("craft-lock-bar")).toBeNull()
  expect(lease.slugs).toEqual([])
})

it("a Page without native Craft keeps Edit here", async () => {
  render(<SavedDocumentPanel {...props()} />)
  expect(await screen.findByRole("button", { name: "Edit here" })).toBeInTheDocument()
  expect(screen.queryByTestId("craft-lock-bar")).toBeNull()
})

it("asks for the lease of the opened Page by its stable reference, with one admission for the whole panel", async () => {
  lease.status = base
  render(<SavedDocumentPanel {...props()} />)
  await screen.findByText("Saved body")
  expect(new Set(lease.slugs)).toEqual(new Set(["@page1"]))
  expect(screen.getAllByTestId("craft-lock-bar")).toHaveLength(1)
})

it("shows who edits and offers only Request edit while someone else holds the document", async () => {
  lease.status = { ...base, holder: pavel }
  render(<SavedDocumentPanel {...props()} />)
  expect(await screen.findByText("Editing: Pavel")).toBeInTheDocument()
  expect(screen.queryByRole("button", { name: "Edit here" })).toBeNull()
  expect(screen.queryByRole("button", { name: "Edit" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Request edit" }))
  expect(lease.request).toHaveBeenCalledOnce()
  expect(screen.getByText("Saved body")).toBeInTheDocument()
})

it("on a free document Edit opens the in-place editor, which takes the lease itself", async () => {
  lease.status = base
  render(<SavedDocumentPanel {...props()} />)
  await screen.findByText("Free")
  expect(screen.queryByRole("button", { name: "Edit here" })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Edit" }))
  expect(screen.getByTestId("page-document")).toBeInTheDocument()
})

it("my own hold elsewhere links to the full editor by Page reference", async () => {
  lease.status = { ...base, holder: { actor_id: "user:human1", display_name: "Me", kind: "human" } }
  lease.heldByMe = true
  render(<SavedDocumentPanel {...props()} />)
  expect(await screen.findByText("Editing: you")).toBeInTheDocument()
  expect(screen.getByRole("link", { name: "Open in new tab" }).getAttribute("href")).toBe("/pages?ref=page1&workspace_id=ws1")
})
