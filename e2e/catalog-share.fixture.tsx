// Browser component fixture: the real My space catalog and share dialog with
// synthetic identity and an in-page fake of the folder and move routes.
import React from "react"
import { createRoot } from "react-dom/client"
import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { PersonalDocumentSpace } from "../components/features/pages/personal-document-space"

const w = window as unknown as { __moves: unknown[] }
w.__moves = []
const row = (id: string, fresh: number) => ({ id, slug: `s-${id}`, name: `Doc ${id}`, owner: "user/fixture-human", has_document: true, private_audience: true, can_move: true, pages_version: 1, panel_count: 1, fresh_panels: fresh, stale_panels: 1 - fresh, panels: undefined })
const rows = [row("one", 1), row("two", 0), row("three", 1)]
const folder = { slug: "ops", name: "Ops", owner: "crew/ops", shared: "workspace", can_accept_pages: true, acl_version: 3, page_count: 0 }
;(window as unknown as { __fetch: unknown }).__fetch = async (url: string, init?: RequestInit) => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })
  if (init?.method === "POST") {
    const id = decodeURIComponent(url.split("/pages/")[1].split("/move")[0])
    w.__moves.push({ id, body: JSON.parse(String(init.body)) })
    return id === "two" ? json({ error: "Changed meanwhile." }, 409) : json({ page: { id } })
  }
  if (url.includes("/acl")) return json({ error: "managers only" }, 403)
  if (url.startsWith("/api/v1/page-folders")) return json([folder])
  return json(rows)
}
createRoot(document.getElementById("root")!).render(
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    <PersonalDocumentSpace workspaceId="fixture-ws" onSelect={() => {}} />
  </QueryClientProvider>)
