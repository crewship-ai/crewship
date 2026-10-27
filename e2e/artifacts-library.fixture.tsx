// Browser component fixture: the real Artifacts library (gallery, kind filter, table, project
// detail on its version history, native Word detail with its edit lock) over the real Pages list
// hook. The Pages list, the artifact project and the Craft lease are answered by in-memory mocks.
// No live backend, model or credentials. Driven by e2e/artifacts-library.browser.mjs.
import React, { useState } from "react"
import { createRoot } from "react-dom/client"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { ArtifactsLibrary } from "../components/features/artifacts-library/artifacts-library"
import { createArtifactMockServer, type ArtifactMockServer } from "../components/features/chat/artifact-context/projects/mock-server"
import type { ArtifactFile } from "../components/features/chat/artifact-context/projects/types"

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

const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()
const row = (over: Record<string, unknown>) => ({ owner: "user/fixture-human", reach: ["owner"], panel_count: 0, panel_states: { fresh: 0, stale: 0, failed: 0, never_produced: 0 }, created_at: ago(9000), ...over })
const LIST = [
  row({ id: "page-site", slug: "pricing-site", name: "Pricing site", artifact_kind: "project", artifact_shared_version: 2, updated_at: ago(12) }),
  row({ id: "page-brief", slug: "partner-brief", name: "Partner brief", artifact_kind: "word", has_document: true, owner: "crew/research", owner_crew_slug: "research", reach: ["crew:research"], updated_at: ago(95) }),
  row({ id: "page-model", slug: "price-model", name: "Price model", artifact_kind: "sheet", has_workbook: true, artifact_shared_version: 3, updated_at: ago(60 * 26) }),
  row({ id: "page-weekly", slug: "weekly-competitor-report", name: "Weekly competitor report", panel_count: 4, panel_states: { fresh: 3, stale: 1, failed: 0, never_produced: 0 }, updated_at: ago(60 * 50) }),
  row({ id: "page-deck", slug: "q3-board-deck", name: "Q3 board deck", has_slides: true, slides_schema: 1, updated_at: ago(60 * 80) }),
  row({ id: "page-support", slug: "q3-support-summary", name: "Q3 support summary", artifact_kind: "project", private_audience: true, updated_at: ago(60 * 100) }),
  row({ id: "page-calc", slug: "pricing-calculator", name: "Pricing calculator", has_application: true, updated_at: ago(30) }),
]
// The private project's mock: a single unshared version.
mocks["q3-support-summary"] = createArtifactMockServer({ slug: "q3-support-summary", name: "Q3 support summary", entrypoint: "plans.html", files: site(1), message: "First draft" })

declare global { interface Window { __unmocked: string[] } }
window.__unmocked = []
const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } })
const realFetch = window.fetch.bind(window)
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url
  const path = new URL(url, location.href).pathname
  if (path === "/api/v1/pages") return json(LIST)
  const slug = /^\/api\/v1\/pages\/([^/]+)\/(?:artifact|craft)\b/.exec(path)?.[1]
  const mock = slug ? mocks[decodeURIComponent(slug)] : path === "/api/v1/pages/artifacts/availability" ? project : undefined
  const response = mock?.handle(url, init)
  if (response) return response
  if (path === "/bundle.js") return realFetch(input, init)
  window.__unmocked.push(`${(init?.method ?? "GET").toUpperCase()} ${url}`)
  return new Response("not mocked", { status: 404 })
}

const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })

function Fixture() {
  const [item, setItem] = useState<string | null>(null)
  return <QueryClientProvider client={queryClient}>
    <div className="min-h-dvh bg-background text-foreground"><ArtifactsLibrary workspaceId={WORKSPACE} item={item} onSelect={setItem} /></div>
  </QueryClientProvider>
}

createRoot(document.getElementById("root")!).render(<Fixture />)
