import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { toPageView, type PageView, type WirePage } from "@/hooks/use-pages"
import type { PageFolderView } from "@/hooks/use-page-folders"
import type { ArtifactCapabilities } from "@/components/features/chat/artifact-context/new-artifact/artifact-kinds"

const m = vi.hoisted(() => ({
  fetch: vi.fn(), push: vi.fn(), addPage: vi.fn(), wordCreate: vi.fn(), createWorkbook: vi.fn(),
  caps: {} as ArtifactCapabilities, pages: [] as PageView[], folders: [] as PageFolderView[],
}))
vi.mock("@/lib/api-fetch", () => ({ apiFetch: m.fetch, broadcastSessionExpired: vi.fn() }))
vi.mock("next/navigation", () => ({ useRouter: () => ({ push: m.push }) }))
vi.mock("@/components/features/chat/artifact-context/new-artifact/use-artifact-capabilities", () => ({ useArtifactCapabilities: () => m.caps }))
vi.mock("@/hooks/use-pages", async importOriginal => ({ ...(await importOriginal<typeof import("@/hooks/use-pages")>()), usePages: () => ({ pages: m.pages, loading: false, error: null, refresh: vi.fn() }) }))
vi.mock("@/hooks/use-page-folders", async importOriginal => ({
  ...(await importOriginal<typeof import("@/hooks/use-page-folders")>()),
  usePageFolders: () => ({ folders: m.folders, loading: false, error: null, supported: true, refresh: vi.fn(), reread: vi.fn() }),
  usePageFolderMutations: () => ({ addPage: { mutateAsync: m.addPage } }),
}))
vi.mock("@/components/features/pages/use-word-document-creation", () => ({
  useWordDocumentCreation: () => ({ allowed: true, formatAvailable: true, frozen: false, retrying: false, create: { mutateAsync: m.wordCreate, isPending: false } }),
}))
vi.mock("@/lib/workbooks/create", () => ({ createWorkbook: m.createWorkbook }))

import { NewArtifactDialog } from "../new-artifact-dialog"

const ALL: ArtifactCapabilities = { projects: true, dashboards: true, slides: true, documents: true, sheets: true, pages: true }
const folder = (slug: string, name: string, over: Partial<PageFolderView> = {}): PageFolderView => ({
  id: slug, slug, name, icon: null, color: null, ownerRef: "crew/sales", ownerLabel: "Sales", pageCount: 0, aclVersion: 3, shared: "crews", createdAt: null, updatedAt: null, ...over,
})
const page = (over: Partial<WirePage>) => toPageView({ id: over.slug, owner: "user/u1", reach: ["owner"], ...over } as WirePage)
const response = (value: unknown, status = 200) => Promise.resolve({ ok: status < 400, status, json: async () => value })

beforeEach(() => {
  m.caps = { ...ALL }
  m.pages = []
  m.folders = [folder("sales", "Sales"), folder("board", "Board", { canAcceptPages: false })]
  for (const f of [m.fetch, m.push, m.addPage, m.wordCreate, m.createWorkbook]) f.mockReset()
})
afterEach(cleanup)

function mount(props: Partial<React.ComponentProps<typeof NewArtifactDialog>> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  const onCreated = vi.fn(), onOpenChange = vi.fn()
  render(<QueryClientProvider client={client}><NewArtifactDialog workspaceId="ws1" open onOpenChange={onOpenChange} onCreated={onCreated} {...props} /></QueryClientProvider>)
  return { onCreated, onOpenChange }
}
const kinds = () => within(screen.getByRole("group", { name: "Kind" })).getAllByRole("button")
const name = (value: string) => fireEvent.change(screen.getByLabelText(/^Name/), { target: { value } })
const create = () => screen.getByRole("button", { name: /^Create/ })

it("shows a thumbnail for every creatable kind, a Coming line for the rest, and no search under nine kinds", () => {
  mount()
  expect(kinds().map(b => b.getAttribute("data-kind"))).toEqual(["project", "dashboard", "page", "word", "sheet", "slides"])
  expect(screen.queryByText(/Coming:/)).toBeNull()
  expect(screen.queryByRole("searchbox")).toBeNull()
  expect(kinds()[0]).toHaveAttribute("aria-pressed", "true")
})

it("leaves out kinds the workspace cannot create", () => {
  m.caps = { ...ALL, projects: false, sheets: false, slides: false }
  mount()
  expect(kinds().map(b => b.getAttribute("data-kind"))).toEqual(["page", "word"])
  expect(screen.getByText(/Coming:/)).toHaveTextContent("Coming: Project, Dashboard, Sheet, Slides")
})

it("defaults to the requested kind and folder, and says who can open it", () => {
  mount({ defaultKind: "word", defaultFolderSlug: "sales" })
  expect(screen.getByRole("button", { name: /Word document/ })).toHaveAttribute("aria-pressed", "true")
  expect(screen.getByLabelText("Folder")).toHaveValue("sales")
  expect(screen.getByText(/Who can open/)).toHaveTextContent("People with access to Sales")
  fireEvent.change(screen.getByLabelText("Folder"), { target: { value: "" } })
  expect(screen.getByText(/Who can open/)).toHaveTextContent("Only you until you share it")
})

it("disables folders the caller cannot file into", () => {
  mount({ defaultKind: "page", defaultFolderSlug: "board" })
  expect(screen.getByRole("option", { name: /Board · view only/ })).toBeDisabled()
  expect(screen.getByLabelText("Folder")).toHaveValue("")
})

it("creates a blank Word document through the shared document creation, then files it", async () => {
  m.wordCreate.mockResolvedValue({ slug: "partner-brief", pagesVersion: 0 })
  m.addPage.mockResolvedValue(null)
  const { onCreated } = mount({ defaultKind: "word", defaultFolderSlug: "sales" })
  expect(screen.getByRole("radio", { name: "Blank" })).toHaveAttribute("aria-checked", "true")
  expect(create()).toBeDisabled()
  name("Partner brief")
  fireEvent.click(create())
  await waitFor(() => expect(onCreated).toHaveBeenCalledWith("partner-brief"))
  expect(m.wordCreate).toHaveBeenCalledWith({ name: "Partner brief", slug: "partner-brief" })
  expect(m.addPage).toHaveBeenCalledWith({ folder: "sales", page: "partner-brief", pagesVersion: 0, aclVersion: 3 })
  expect(m.fetch).not.toHaveBeenCalled()
})

it("creates an empty Page with allow_empty and puts it in the chosen folder", async () => {
  m.fetch.mockImplementation(() => response({ id: "p1", slug: "overdue-invoices", pages_version: 0 }, 201))
  m.addPage.mockResolvedValue(null)
  const { onCreated } = mount({ defaultKind: "page" })
  fireEvent.change(screen.getByLabelText("Folder"), { target: { value: "sales" } })
  name("Overdue invoices")
  fireEvent.click(create())
  await waitFor(() => expect(onCreated).toHaveBeenCalledWith("overdue-invoices"))
  const [url, init] = m.fetch.mock.calls[0]
  expect(url).toBe("/api/v1/pages?workspace_id=ws1")
  expect(JSON.parse(init.body)).toEqual({ name: "Overdue invoices", slug: "overdue-invoices", panels: [], allow_empty: true })
  expect(m.addPage).toHaveBeenCalledWith({ folder: "sales", page: "overdue-invoices", pagesVersion: 0, aclVersion: 3 })
})

it("does not file an unfiled Page and shows the server's refusal inline", async () => {
  m.fetch.mockImplementation(() => response({ error: "slug already taken" }, 409))
  const { onCreated } = mount({ defaultKind: "page" })
  name("Overdue invoices")
  fireEvent.click(create())
  expect(await screen.findByRole("alert")).toHaveTextContent("slug already taken")
  expect(onCreated).not.toHaveBeenCalled()
  expect(m.addPage).not.toHaveBeenCalled()
})

it("keeps a created Page when filing fails and offers to open it", async () => {
  m.fetch.mockImplementation(() => response({ id: "p1", slug: "monitor", pages_version: 0 }, 201))
  m.addPage.mockRejectedValue(new Error("folder changed"))
  const { onCreated } = mount({ defaultKind: "page", defaultFolderSlug: "sales" })
  name("Monitor")
  fireEvent.click(create())
  expect(await screen.findByRole("alert")).toHaveTextContent("Monitor was created but not put in Sales: folder changed")
  expect(onCreated).not.toHaveBeenCalled()
  fireEvent.click(screen.getByRole("button", { name: "Open it" }))
  expect(onCreated).toHaveBeenCalledWith("monitor")
  expect(m.fetch).toHaveBeenCalledTimes(1)
})

it("creates a Sheet through the workbook route", async () => {
  m.createWorkbook.mockResolvedValue({ pageId: "w1", slug: "price-model", name: "Price model" })
  const { onCreated } = mount({ defaultKind: "sheet" })
  name("Price model")
  fireEvent.click(create())
  await waitFor(() => expect(onCreated).toHaveBeenCalledWith("price-model"))
  expect(m.createWorkbook).toHaveBeenCalledWith("ws1", { name: "Price model", slug: "price-model" })
})

it("warns about the same name and kind in the folder and opens the existing one", () => {
  m.pages = [
    page({ slug: "brief", name: "Partner Brief", artifact_kind: "word", has_document: true, folder: { slug: "sales", name: "Sales" } }),
    page({ slug: "brief-sheet", name: "Partner brief", has_workbook: true, folder: { slug: "sales", name: "Sales" } }),
  ]
  const { onCreated, onOpenChange } = mount({ defaultKind: "word", defaultFolderSlug: "sales" })
  name("partner brief")
  expect(screen.getByText(/already exists here/)).toHaveTextContent("Partner Brief already exists here")
  fireEvent.click(screen.getByRole("button", { name: "Open it" }))
  expect(onCreated).toHaveBeenCalledWith("brief")
  expect(onOpenChange).toHaveBeenCalledWith(false)
  cleanup()
  mount({ defaultKind: "word" })
  name("partner brief")
  expect(screen.queryByText(/already exists here/)).toBeNull()
})

it("hands agent kinds to chat as an unsent draft with Describe it", () => {
  const { onOpenChange } = mount({ defaultKind: "project" })
  expect(screen.getByRole("radiogroup", { name: "Start with" })).toBeInTheDocument()
  expect(screen.getByRole("radio", { name: "Describe it" })).toHaveAttribute("aria-checked", "true")
  expect(screen.queryByRole("radio", { name: "Blank" })).toBeNull()
  name("Pricing site")
  fireEvent.change(screen.getByLabelText(/What should it contain/), { target: { value: "plans, compare, FAQ" } })
  fireEvent.click(screen.getByRole("button", { name: "Continue in chat" }))
  expect(m.push).toHaveBeenCalledTimes(1)
  const url = new URL(m.push.mock.calls[0][0], "http://x")
  expect(url.pathname).toBe("/chat")
  expect(url.searchParams.get("draft")).toBe("1")
  expect(url.searchParams.get("prompt")).toBe('Create a project "Pricing site": plans, compare, FAQ')
  expect(onOpenChange).toHaveBeenCalledWith(false)
  expect(m.fetch).not.toHaveBeenCalled()
})

it("hands a Slides deck to the agent like a project", () => {
  mount({ defaultKind: "slides" })
  name("Q3 review")
  fireEvent.change(screen.getByLabelText(/What should it contain/), { target: { value: "results and risks" } })
  fireEvent.click(screen.getByRole("button", { name: "Continue in chat" }))
  expect(new URL(m.push.mock.calls[0][0], "http://x").searchParams.get("prompt")).toBe('Create a slide deck "Q3 review": results and risks')
  expect(m.fetch).not.toHaveBeenCalled()
})

it("seeds the chat draft from an agent template", () => {
  mount({ defaultKind: "dashboard" })
  fireEvent.click(screen.getByRole("radio", { name: "From template" }))
  fireEvent.click(screen.getByRole("button", { name: "Support SLA" }))
  fireEvent.click(screen.getByRole("button", { name: "Continue in chat" }))
  expect(new URL(m.push.mock.calls[0][0], "http://x").searchParams.get("prompt")).toBe("Create a dashboard from the Support SLA template")
})

it("marks templates and upload for Crafts as coming", () => {
  mount({ defaultKind: "word" })
  expect(screen.getByRole("radio", { name: /From template/ })).toBeDisabled()
  expect(screen.getByRole("radio", { name: /Upload/ })).toBeDisabled()
  expect(screen.getByRole("radio", { name: "Describe it" })).toBeEnabled()
})
