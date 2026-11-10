import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { CREATE_SCHEMA } from "@/lib/documents/contracts"

const state = vi.hoisted(() => ({ available: true, advertised: true, fetch: vi.fn() }))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: state.fetch, broadcastSessionExpired: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock("@/components/features/chat/artifact-context/new-artifact/use-artifact-capabilities", () => ({
  useArtifactCapabilities: () => ({ projects: false, dashboards: false, slides: false, documents: state.advertised, docs: state.advertised, words: false, sheets: false, pages: true }),
}))
vi.mock("@/hooks/use-documents-availability", () => ({
  useDocumentsAvailability: () => ({ data: state.available, wordcraftNative: false, gridcraftNative: false, wordcraftCommands: false }),
}))
vi.mock("@/hooks/use-page-creation-access", () => ({ usePageCreationAccess: () => true }))
vi.mock("@/hooks/use-pages", async original => ({
  ...await original<typeof import("@/hooks/use-pages")>(),
  usePages: () => ({ pages: [], loading: false, error: null, refresh: vi.fn() }),
}))
vi.mock("@/hooks/use-page-folders", () => ({
  usePageFolders: () => ({ folders: [], loading: false, error: null }),
  usePageFolderMutations: () => ({ addPage: { mutateAsync: vi.fn() } }),
}))

import { NewArtifactDialog } from "../new-artifact-dialog"

afterEach(() => { cleanup(); state.fetch.mockReset(); state.available = true; state.advertised = true })

it.each(["engine", "catalog"] as const)("keeps a frozen Docs intent through %s withdrawal and retries the original initialization", async loss => {
  let creates = 0
  const initializations: Record<string, unknown>[] = []
  state.fetch.mockImplementation(async (url: string, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body))
    if (url.includes("/initialize")) {
      initializations.push(body)
      return initializations.length === 1
        ? new Response(JSON.stringify({ error: "initialization acknowledgement lost" }), { status: 503 })
        : new Response(JSON.stringify({ id: body.id, rev: 0, epoch: 1, actor: "human", kind: "init", found: true, duplicate: true }))
    }
    expect(url).toBe("/api/v1/pages?workspace_id=ws1")
    creates++
    return new Response(JSON.stringify({ id: creates === 1 ? "original-page" : "duplicate-page", slug: body.slug, pages_version: 0 }), { status: 201 })
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const onCreated = vi.fn()
  const form = () => <QueryClientProvider client={client}>
    <NewArtifactDialog workspaceId="ws1" open defaultKind="docs" onOpenChange={vi.fn()} onCreated={onCreated} />
  </QueryClientProvider>
  const view = render(form())
  expect(screen.getByText("You, and workspace owners and admins. Others only after you share it.")).toBeInTheDocument()
  expect(screen.queryByText("Only you until you share it")).toBeNull()
  fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Team notes" } })
  fireEvent.click(screen.getByRole("button", { name: "Create and edit" }))
  await screen.findByRole("alert")
  expect(creates).toBe(1)
  expect(initializations).toHaveLength(1)
  expect(onCreated).not.toHaveBeenCalled()

  // A different kind remains available. It must never replace a frozen Doc.
  state.advertised = false
  if (loss === "engine") state.available = false
  view.rerender(form())
  expect(screen.getByText("Docs is unavailable. Your creation is kept. Retry when it is available again.")).toBeInTheDocument()
  const blocked = screen.getByRole("button", { name: /^(Retry initialization|Create page)$/ })
  await act(async () => { fireEvent.click(blocked) })
  expect(creates).toBe(1)
  expect(initializations).toHaveLength(1)
  expect(onCreated).not.toHaveBeenCalled()
  expect(blocked).toBeDisabled()

  state.available = true
  state.advertised = true
  view.rerender(form())
  fireEvent.click(screen.getByRole("button", { name: "Retry initialization" }))
  await waitFor(() => expect(onCreated).toHaveBeenCalledWith("team-notes", "original-page"))
  expect(creates).toBe(1)
  expect(initializations).toHaveLength(2)
  expect(initializations[1]).toEqual(initializations[0])
  expect(initializations[0]).toMatchObject({ schema: CREATE_SCHEMA, page_id: "original-page" })
  client.clear()
})

it("allows a new kind after the server clearly refused the initial Page creation", async () => {
  state.fetch.mockResolvedValueOnce(new Response(JSON.stringify({ error: "Page creation refused" }), { status: 403 }))
    .mockResolvedValueOnce(new Response(JSON.stringify({ id: "new-page", slug: "team-notes", pages_version: 0 }), { status: 201 }))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const onCreated = vi.fn()
  const form = () => <QueryClientProvider client={client}>
    <NewArtifactDialog workspaceId="ws1" open defaultKind="docs" onOpenChange={vi.fn()} onCreated={onCreated} />
  </QueryClientProvider>
  const view = render(form())
  fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Team notes" } })
  fireEvent.click(screen.getByRole("button", { name: "Create and edit" }))
  await screen.findByRole("alert")
  state.advertised = false
  state.available = false
  view.rerender(form())
  const createPage = screen.getByRole("button", { name: "Create page" })
  expect(createPage).toBeEnabled()
  fireEvent.click(createPage)
  await waitFor(() => expect(onCreated).toHaveBeenCalledWith("team-notes", "new-page"))
  expect(state.fetch).toHaveBeenCalledTimes(2)
  expect(state.fetch.mock.calls.every(([url]) => url === "/api/v1/pages?workspace_id=ws1")).toBe(true)
  client.clear()
})
