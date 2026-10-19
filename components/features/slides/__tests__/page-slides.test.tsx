import * as React from "react"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import fixture from "@/testdata/slides-schema-v1.json"
import { apiFetch } from "@/lib/api-fetch"
import { PageSlides } from "../page-slides"

const events = vi.hoisted(() => new Map<string, () => void>())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: vi.fn() }))
vi.mock("@/hooks/use-realtime", () => ({ useRealtimeEventSafe: (event: string, callback: () => void) => events.set(event, callback) }))
const props = { workspaceId: "workspace", pageId: "fixture_page", currentUserId: "human", activationKey: "auth-open", enabled: true }
const saved = () => ({ page_id: "fixture_page", schema_version: 1, revision: 7, order_version: 3, content_hash: fixture.hash, deck: structuredClone(fixture.deck) })
beforeEach(() => { events.clear(); vi.mocked(apiFetch).mockReset(); vi.mocked(apiFetch).mockResolvedValue(Response.json(saved())) })
afterEach(cleanup)

it("renders the confirmed revision in the real read-only editor and allows Present without writes", async () => {
  render(<PageSlides {...props} />)
  await waitFor(() => expect(screen.getByLabelText("Slides editor")).toBeTruthy())
  expect(screen.getByText(/Saved revision 7/)).toBeTruthy()
  expect((screen.getByLabelText("Speaker notes") as HTMLTextAreaElement).disabled).toBe(true)
  for (const name of ["New slide", "Duplicate slide", "Delete slide", "Add text", "Add shape", "Add table"]) expect((screen.getByRole("button", { name }) as HTMLButtonElement).disabled).toBe(true)
  expect(screen.queryByRole("button", { name: /Save|Restore|History|Initialize/ })).toBeNull()
  fireEvent.click(screen.getByRole("button", { name: "Present" }))
  expect(screen.getByRole("dialog")).toBeTruthy()
  fireEvent.keyDown(screen.getByRole("dialog"), { key: "Escape" })
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull())
  expect(apiFetch).toHaveBeenCalledTimes(1)
  expect(vi.mocked(apiFetch).mock.calls[0][1]?.method).toBeUndefined()
})

it("removes the old deck immediately on deletion and does not render rejected response content", async () => {
  render(<PageSlides {...props} />)
  await waitFor(() => expect(screen.getByLabelText("Slides editor")).toBeTruthy())
  let finish!: (value: Response) => void
  vi.mocked(apiFetch).mockReturnValue(new Promise<Response>(resolve => { finish = resolve }))
  act(() => events.get("page.deleted")?.())
  expect(screen.queryByLabelText("Slides editor")).toBeNull()
  await act(async () => finish(Response.json({ ...saved(), private_title: "Never display marker" }, { status: 403 })))
  await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy())
  expect(screen.queryByLabelText("Slides editor")).toBeNull()
  expect(screen.queryByText("Never display marker")).toBeNull()
})

it("never renders schema2 source text or a save control", async () => {
  const value = saved()
  value.schema_version = 2; value.deck.schema_version = 2
  value.deck.slides[0].notes = "Unsupported private marker"
  vi.mocked(apiFetch).mockResolvedValue(Response.json(value))
  render(<PageSlides {...props} />)
  await waitFor(() => expect(screen.getByRole("alert")).toBeTruthy())
  expect(screen.queryByText("Unsupported private marker")).toBeNull()
  expect(screen.queryByRole("button")).toBeNull()
})
