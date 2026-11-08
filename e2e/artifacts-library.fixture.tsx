// Browser component fixture: the real Artifacts library (Harbor 2 panel with spaces, search
// operators and facets; Home; one detail per kind; the project viewer with comments, history and
// restore; the New artifact dialog) over the real Pages and folder hooks. Pages, folders,
// preferences, document availability, the artifact projects and the Craft lease are answered by
// in-memory mocks. No live backend, model or credentials. Driven by e2e/artifacts-library.browser.mjs.
import React, { useState } from "react"
import { useIsMobile } from "../hooks/use-mobile"
import { useAppStore } from "../lib/store"
import { createRoot } from "react-dom/client"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ArtifactsLibrary } from "../components/features/artifacts-library/artifacts-library"
import { createArtifactMockServer, type ArtifactMockServer } from "../components/features/chat/artifact-context/projects/mock-server"
import { documentSchema } from "../lib/documents/schema"
import type { ArtifactFile } from "../components/features/chat/artifact-context/projects/types"
import slidesFixture from "../testdata/slides-schema-v1.json"

const WORKSPACE = "ws-fixture"
const html = (path: string, title: string, body: string): ArtifactFile => ({ path, media_type: "text/html",
  content: `<!doctype html><html><head><meta charset="utf-8"><title>${title}</title><style>body{font-family:system-ui,sans-serif;margin:0;padding:24px;background:#f8fafc;color:#1f2933}h1{margin:0 0 8px}</style></head><body>${body}</body></html>` })
const site = (n: number) => [
  html("plans.html", "Plans", `<h1 id="headline">Simple pricing${n > 2 ? " for every team" : ""}</h1><p id="lead">Version ${n} of the pricing site.</p>`),
  html("faq.html", "FAQ", `<h1 id="faq-title">FAQ</h1>`),
]

const project = createArtifactMockServer({ slug: "pricing-site", name: "Pricing site", entrypoint: "plans.html", files: site(1), message: "Plans and FAQ" })
project.agentPublish(site(2), "Team plan highlighted")
project.agentPublish(site(3), "Headline names every team")
project.agentPublish(site(4), "FAQ answers seat counting")
project.state.sharedVersion = 2
const brief = createArtifactMockServer({
  slug: "@page-brief", name: "Partner brief", entrypoint: "brief.html", files: [html("brief.html", "Brief", "<p>Brief</p>")],
  craft: { enabled: true, lease: { holder: { actor_id: "user:jana", display_name: "Jana Nováková", kind: "human" }, mine: false, generation: "7", can_write: false, requests: [] } },
})
const mocks: Record<string, ArtifactMockServer> = { "pricing-site": project, "@page-brief": brief }

const RESEARCH = { slug: "research", name: "Research" }
const FOLDERS = [{ id: "folder-research", slug: "research", name: "Research", owner: "crew/research", owner_crew_name: "Research", page_count: 2, can_accept_pages: true }]

const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()
const row = (over: Record<string, unknown>) => ({ owner: "user/fixture-human", reach: ["owner"], panel_count: 0, panel_states: { fresh: 0, stale: 0, failed: 0, never_produced: 0 }, created_at: ago(9000), ...over })
const LIST = [
  row({ id: "page-site", slug: "pricing-site", name: "Pricing site", artifact_kind: "project", artifact_shared_version: 2, folder: RESEARCH, updated_at: ago(12) }),
  row({ id: "page-brief", slug: "partner-brief", name: "Partner brief", artifact_kind: "word", has_document: true, owner: "crew/research", owner_crew_slug: "research", reach: ["crew:research"], updated_at: ago(95) }),
  row({ id: "page-model", slug: "price-model", name: "Price model", artifact_kind: "sheet", has_workbook: true, artifact_shared_version: 3, updated_at: ago(60 * 26) }),
  row({ id: "page-pipeline", slug: "sales-pipeline", name: "Sales pipeline", artifact_kind: "dashboard", artifact_shared_version: 1, folder: RESEARCH, updated_at: ago(60 * 30) }),
  // A legacy Page whose body is panels: kind Page, never Dashboard.
  row({ id: "page-weekly", slug: "weekly-competitor-report", name: "Weekly competitor report", panel_count: 4, panel_states: { fresh: 3, stale: 1, failed: 0, never_produced: 0 }, updated_at: ago(60 * 50) }),
  row({ id: "page-deck", slug: "q3-board-deck", name: "Q3 board deck", has_slides: true, slides_schema: 1, updated_at: ago(60 * 80) }),
  row({ id: "page-support", slug: "q3-support-summary", name: "Q3 support summary", artifact_kind: "project", private_audience: true, updated_at: ago(60 * 100) }),
  row({ id: "page-calc", slug: "pricing-calculator", name: "Pricing calculator", has_application: true, updated_at: ago(30) }),
  // A Docs page: a document without a version head (has_document, no typed kind).
  row({ id: "page-launch", slug: "launch-notes", name: "Launch notes", has_document: true, updated_at: ago(60 * 110) }),
  // An interactive App: a project that publishes an application (interactive.json).
  row({ id: "page-survey", slug: "field-survey", name: "Field survey", artifact_kind: "project", has_application: true, artifact_shared_version: 1, updated_at: ago(60 * 120) }),
  // A Design canvas; its live document is the use-design stand-in (artifacts-library.design-fixture.ts).
  row({ id: "page-design", slug: "launch-landing", name: "Launch landing", artifact_kind: "design", updated_at: ago(60 * 130) }),
]
// The private project's mock: a single unshared version.
mocks["q3-support-summary"] = createArtifactMockServer({ slug: "q3-support-summary", name: "Q3 support summary", entrypoint: "plans.html", files: site(1), message: "First draft" })

// The interactive App: the server advertises its runtime, but this installation has no separate
// application domain (runtime_url ""), so the frame must say it cannot run and load nothing.
mocks["field-survey"] = createArtifactMockServer({ slug: "field-survey", name: "Field survey", entrypoint: "index.html",
  files: [html("index.html", "Survey", `<h1>Field survey</h1><form><label>Store <input name="store"></label></form>`), { path: "interactive.json", media_type: "application/json", content: JSON.stringify({ collections: ["responses"] }) }], message: "Survey form" })
mocks["field-survey"].state.sharedVersion = 1
const INTERACTIVE: Record<string, unknown> = { "field-survey": { runtime_url: "", development_same_origin: false, collections: ["responses"] } }

// The typed Dashboard: its project, read in the generic viewer while its own Dashboard view is switched off.
mocks["sales-pipeline"] = createArtifactMockServer({ slug: "sales-pipeline", name: "Sales pipeline", entrypoint: "index.html",
  files: [html("index.html", "Pipeline", `<h1 id="pipeline">Sales pipeline</h1><p>12 open deals</p>`), { path: "dashboard.json", media_type: "application/json", content: JSON.stringify({ schema_version: 1, collections: ["deals"] }) }], message: "Pipeline" })
mocks["sales-pipeline"].state.sharedVersion = 1
// The legacy deck: its saved slides (schema 1), read through the Slides read route.
// The schema fixture's words are replaced with the deck's own English ones; its shapes stay.
const english = (value: unknown): unknown => Array.isArray(value) ? value.map(english)
  : value && typeof value === "object" ? Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, key === "value" && typeof entry === "string" ? "Q3 board update" : key === "notes" ? "" : english(entry)]))
    : value
const CELLS = [["Item", "EUR"], ["Research", "12345"]]
const DECK = { ...(english(slidesFixture.deck) as typeof slidesFixture.deck), deck_id: "page-deck" }
for (const slide of DECK.slides) for (const element of slide.elements as { table?: { cells: string[][] } }[]) if (element.table) element.table.cells = element.table.cells.map((row, r) => row.map((_cell, c) => CELLS[r]?.[c] ?? ""))

const LAUNCH = { type: "doc", content: [
  { type: "heading", attrs: { id: "h1", level: 2 }, content: [{ type: "text", text: "Summary" }] },
  { type: "paragraph", attrs: { id: "l1" }, content: [{ type: "text", text: "The launch moves to Tuesday. Support has the new FAQ; sales has the pricing sheet." }] },
  { type: "heading", attrs: { id: "h2", level: 2 }, content: [{ type: "text", text: "Open questions" }] },
  { type: "paragraph", attrs: { id: "l2" }, content: [{ type: "text", text: "Who announces the partner programme, and when?" }] },
] }
const LAUNCH_SNAPSHOT = documentSchema.snapshot(documentSchema.create(LAUNCH, 3))

declare global { interface Window { __unmocked: string[]; __project: ArtifactMockServer; __agentPublish: () => number } }
window.__unmocked = []
window.__project = project
// The agent answers the open comments with a new version (what a chat run would publish).
window.__agentPublish = () => project.agentPublish(site(5), "Headline answers the comment", "Reworded the headline.")
const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } })
const realFetch = window.fetch.bind(window)
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url
  const path = new URL(url, location.href).pathname
  const method = (init?.method ?? "GET").toUpperCase()
  if (path === "/api/v1/pages" && method === "GET") return json(LIST)
  if (path === "/api/v1/page-folders" && method === "GET") return json({ folders: FOLDERS })
  if (path === "/api/v1/workspaces/ws-fixture/members" && method === "GET") return json([
    { user_id: "u-jana", user: { full_name: "Jana Nováková" } }, { user_id: "u-petr", user: { full_name: "Petr Malý" } },
  ])
  if (path === "/api/v1/me/preferences" && method === "GET") return json({})
  if (path.startsWith("/api/v1/me/preferences/") && method === "PUT") return json({ ok: true })
  if (path === "/api/v1/documents/status" && method === "GET") return json({ enabled: true })
  // The legacy panel Page's own content, read by ID inside its frame (#4483): one fresh and one stale panel.
  if (path === "/api/v1/pages/%40page-weekly" && method === "GET") return json({ ...LIST[4], has_application: false, panels: [
    { id: "competitors", schema: "status.v1", title: "Competitor pricing", owner: "user/fixture-human", sla_seconds: 86400, span: 8, state: "fresh",
      data: { items: [{ name: "Acme", state: "ok", label: "No change" }, { name: "Globex", state: "warn", label: "New Team tier" }] },
      provenance: { producer: "routine/weekly-scan", run_id: "run-1", produced_at: ago(60) } },
    { id: "share", schema: "metric.v1", title: "Share of voice", sla_seconds: 3600, span: 4, state: "stale",
      data: { value: 31, unit: "%" }, provenance: { producer: "routine/weekly-scan", run_id: "run-0", produced_at: ago(60 * 30) } },
  ] })
  if (path === "/api/v1/pages/%40page-deck/slides" && method === "GET") return json({ schema_version: 1, revision: 1, order_version: 1, content_hash: "0".repeat(64), deck: DECK })
  // The Word detail's read-only preview: current metadata, then the saved document (schema 3).
  if (path === "/api/v1/pages/%40page-brief" && method === "GET") return json({ id: "page-brief", slug: "partner-brief", name: "Partner brief", has_document: true })
  if (path === "/api/v1/pages/%40page-brief/document" && method === "GET") return json({ page_id: "page-brief", schema: 3, rev: 4, content: {
    type: "doc", content: [
      // Schema 3 blocks carry a unique id.
      { type: "paragraph", attrs: { id: "p1" }, content: [{ type: "text", text: "Partner brief" }] },
      { type: "paragraph", attrs: { id: "p2" }, content: [{ type: "text", text: "Three partners, one launch date and the open questions for each." }] },
    ] } })
  // Native Word and Sheet: the server admits the editor and this person may edit (the lock is the host's).
  if (/^\/api\/v1\/pages\/%40page-(brief|model)\/craft\/availability$/.test(path) && method === "GET") return json({ enabled: true, can_edit: true })
  // The Docs page: its metadata, and the confirmed document the live editor opens (rev, epoch and a
  // Yjs snapshot of schema 3, as the documents service answers). Nothing is ever written back here.
  if (path === "/api/v1/pages/%40page-launch" && method === "GET") return json({ id: "page-launch", slug: "launch-notes", name: "Launch notes", has_document: true })
  if (path === "/api/v1/pages/%40page-launch/document" && method === "GET") return json({ page_id: "page-launch", schema: 3, rev: 2, epoch: "fixture-epoch", can_write: true, snapshot: LAUNCH_SNAPSHOT, content: LAUNCH })
  if (path === "/api/v1/pages/%40page-launch/document/comments" && method === "GET") return json({ comments: [], can_comment: false })
  if (path === "/api/v1/pages/%40page-launch/document/requests" && method === "GET") return json({ requests: [], can_dispatch: false, eligible_chats: [] })
  // The private comment draft's key service (lib/local-draft-seal): an in-memory stand-in that
  // wraps a key as its own envelope. It only lets the fixture's drafts seal; nothing is secret here.
  if (/^\/api\/v1\/pages\/[^/]+\/draft-key$/.test(path) && method === "POST") {
    const body = JSON.parse(String(init?.body ?? "{}"))
    if (body.operation === "wrap") return json({ envelope: `v1:${body.key}` })
    if (body.operation === "unwrap") return json({ key: String(body.envelope).slice(3) })
    return json({ readable: true })
  }
  const slug = /^\/api\/v1\/pages\/([^/]+)\/(?:artifact|craft)\b/.exec(path)?.[1]
  const mock = slug ? mocks[decodeURIComponent(slug)] : path === "/api/v1/pages/artifacts/availability" ? project : undefined
  const response = await mock?.handle(url, init)
  // The server answers whether this person may send to the agent, restore and share (#4628); the
  // shared mock predates those fields, so the fixture says yes where the person can write.
  if (response && method === "GET" && /\/artifact$/.test(path) && response.headers.get("content-type")?.includes("json")) {
    const body = await response.clone().json()
    const interactive = INTERACTIVE[decodeURIComponent(slug ?? "")]
    if (body && typeof body === "object" && "can_write" in body && !("can_send_to_agent" in body)) return json({ ...body, can_send_to_agent: body.can_write === true, can_restore: body.can_write === true, can_share: body.can_write === true, ...(interactive ? { interactive } : {}) })
  }
  if (response) return response
  if (path === "/bundle.js") return realFetch(input, init)
  window.__unmocked.push(`${(init?.method ?? "GET").toUpperCase()} ${url}`)
  return new Response("not mocked", { status: 404 })
}

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

/** Stand-in for AppToolbar's phone panel button: it opens the registered SecondaryPanel as a bottom sheet. */
function PhonePanelButton() {
  const isMobile = useIsMobile()
  const panel = useAppStore(s => s.secondaryPanel)
  const open = useAppStore(s => s.setSecondaryPanelOpen)
  if (!isMobile || !panel) return null
  return <div className="flex h-11 items-center border-b px-2">
    <button type="button" aria-label={panel.label} className="rounded-md border px-3 py-1.5 text-sm" onClick={() => open(true)}>Panel</button>
  </div>
}

function Fixture() {
  const [item, setItem] = useState<string | null>(null)
  return <QueryClientProvider client={queryClient}>
    <div className="min-h-dvh bg-background text-foreground"><PhonePanelButton /><ArtifactsLibrary workspaceId={WORKSPACE} item={item} onSelect={setItem} /></div>
  </QueryClientProvider>
}

createRoot(document.getElementById("root")!).render(<Fixture />)
