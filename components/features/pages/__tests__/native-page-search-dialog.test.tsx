import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { beforeEach, expect, it, vi } from "vitest"
import { NativePageSearchDialog } from "@/components/features/pages/native-page-search-dialog"
import { apiFetch } from "@/lib/api-fetch"
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
const fetch = vi.mocked(apiFetch)
beforeEach(() => fetch.mockReset())

it("labels the supported scope and opens immutable Page ID, rendering snippets as plain text", async () => {
  fetch.mockResolvedValueOnce(new Response(JSON.stringify({ scope: "native-docs-slides", matches: [{ page_id: "original-page", slug: "reused-address", name: "Original report", content_type: "document", schema_version: 2, source_revision: 0, source_generation: 0, snippet: "<img src=x> needle" }], has_more: false }), { status: 200 }))
  const open = vi.fn()
  render(<NativePageSearchDialog workspaceId="workspace" viewerId="viewer" onClose={vi.fn()} onOpenPage={open} />)
  expect(screen.getByRole("heading", { name: "Search Docs and Slides text" })).toBeTruthy()
  fireEvent.change(screen.getByRole("textbox", { name: "Words to find" }), { target: { value: "needle" } })
  fireEvent.click(screen.getByRole("button", { name: "Search", exact: true }))
  await screen.findByText("<img src=x> needle")
  expect(document.querySelector("img")).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: /Original report/ }))
  expect(open).toHaveBeenCalledExactlyOnceWith("original-page")
})

it("unavailable coverage displays retry failure rather than no matches and editing clears it", async () => {
  fetch.mockResolvedValueOnce(new Response("{}", { status: 503 }))
  render(<NativePageSearchDialog workspaceId="workspace" viewerId="viewer" onClose={vi.fn()} onOpenPage={vi.fn()} />)
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "needle" } })
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Search", exact: true })) })
  expect(screen.getByRole("alert").textContent).toContain("not ready")
  expect(screen.queryByText("No matching supported native Pages.")).toBeNull()
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "other" } })
  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull())
})
