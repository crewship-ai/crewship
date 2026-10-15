import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { afterEach, expect, it, vi } from "vitest"
import { NewArtifactDialog } from "../new-artifact-dialog"
const request = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: request }))
vi.mock("@/hooks/use-pages", () => ({ pagesKeys: { all: (workspace: string) => ["pages", workspace] } }))
afterEach(() => { cleanup(); request.mockReset() })
it("creates a human-owned empty page before initializing the durable document", async () => {
  request.mockResolvedValue({ ok: true, json: async () => ({ enabled: true }) })
  const created = vi.fn()
  render(<QueryClientProvider client={new QueryClient()}><NewArtifactDialog workspaceId="w" onClose={vi.fn()} onCreated={created} /></QueryClientProvider>)
  await waitFor(() => expect(screen.getByRole("option", { name: "Doc" })).toBeEnabled())
  fireEvent.change(screen.getByLabelText("Content"), { target: { value: "document" } })
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Team notes" } })
  fireEvent.click(screen.getByRole("button", { name: "Create Doc page" }))
  await waitFor(() => expect(created).toHaveBeenCalledWith("team-notes"))
  expect(request.mock.calls.map(([url]) => url)).toEqual(["/api/v1/documents/status?workspace_id=w", "/api/v1/pages?workspace_id=w", "/api/v1/pages/team-notes/document/initialize?workspace_id=w"])
  expect(JSON.parse(request.mock.calls[1][1].body)).not.toHaveProperty("owner")
  expect(JSON.parse(request.mock.calls[2][1].body)).toMatchObject({ schema: 2, body: { type: "doc", content: [{ type: "paragraph", attrs: { id: expect.any(String) } }] } })
})
