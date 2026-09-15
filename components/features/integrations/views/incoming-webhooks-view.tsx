"use client"

import * as React from "react"
import Link from "next/link"
import { useQuery } from "@tanstack/react-query"
import { apiFetch } from "@/lib/api-fetch"
import { usePipelines } from "@/hooks/use-pipelines"
import { usePage, usePages } from "@/hooks/use-pages"
import { usePageGrants } from "@/hooks/use-page-grants"
import { RoutineWebhooksTab } from "@/components/features/routines/routine-webhooks-tab"
import { TriggersTab } from "@/components/features/chat/right-panel-tabs/triggers-tab"
import { WebhooksCard } from "@/components/features/pages/page-settings"

// Configuration stays owned by each target's existing API. In particular,
// opening this view must never rotate an agent secret or issue a Page token.
type TargetKind = "routine" | "agent" | "page" | "issue"
const TARGETS: { kind: TargetKind; label: string; description: string }[] = [
  { kind: "routine", label: "Routines", description: "Run a routine with the incoming event as input." },
  { kind: "agent", label: "Agents / chat", description: "Send an event to an agent as a background run. This does not append a turn to an existing chat." },
  { kind: "page", label: "Pages", description: "Update one panel using its producer token." },
  { kind: "issue", label: "Issues via a routine", description: "Choose a routine that creates, updates or comments on an issue. The routine defines the action; selecting it here does not add an issue step." },
]

interface AgentTarget { id: string; name: string; slug: string }

export function IncomingWebhooksView({ workspaceId }: { workspaceId: string }) {
  const [kind, setKind] = React.useState<TargetKind>("routine")
  const [selected, setSelected] = React.useState("")
  const [search, setSearch] = React.useState("")
  const routines = usePipelines(kind === "routine" || kind === "issue" ? workspaceId : null)
  const pages = usePages(kind === "page" ? workspaceId : null)
  const agents = useQuery({
    queryKey: ["incoming-webhook-agents", workspaceId],
    enabled: kind === "agent",
    queryFn: async ({ signal }): Promise<AgentTarget[]> => {
      const response = await apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`, { signal })
      if (!response.ok) throw new Error(`Could not load agents (${response.status})`)
      const body = await response.json()
      if (!Array.isArray(body)) throw new Error("Unexpected agents response")
      return body
    },
  })
  const targets = kind === "page" ? pages.pages : kind === "agent" ? agents.data ?? [] : routines.pipelines
  const loading = kind === "page" ? pages.loading : kind === "agent" ? agents.isPending : routines.loading
  const error = kind === "page" ? pages.error : kind === "agent" ? agents.error?.message : routines.error
  const visible = targets.filter((target) => `${target.name} ${target.slug}`.toLowerCase().includes(search.toLowerCase()))
  const target = targets.find((item) => item.id === selected)
  const description = TARGETS.find((item) => item.kind === kind)!.description

  return (
    <div className="flex min-h-0 flex-1 flex-col md:flex-row">
      <aside className="shrink-0 border-b border-hairline p-3 md:w-[280px] md:overflow-y-auto md:border-r md:border-b-0">
        <nav aria-label="Incoming webhook targets" className="flex flex-wrap gap-1 md:flex-col">
          {TARGETS.map((item) => (
            <button key={item.kind} type="button" aria-current={kind === item.kind ? "page" : undefined}
              className={`rounded px-3 py-2 text-left text-xs coarse:min-h-12 ${kind === item.kind ? "bg-primary/15 text-primary" : "text-muted-foreground hover:bg-muted"}`}
              onClick={() => { setKind(item.kind); setSelected(""); setSearch("") }}>
              {item.label}
            </button>
          ))}
        </nav>
        <label className="mt-4 block text-xs text-muted-foreground">
          Search targets
          <input value={search} onChange={(event) => setSearch(event.target.value)} className="mt-2 w-full rounded border border-hairline bg-background p-2 coarse:min-h-12" />
        </label>
        {error && <p role="alert" className="mt-3 text-xs text-destructive">{error}</p>}
        {loading ? <p className="p-3 text-xs">Loading…</p> : (
          <div className="mt-2 max-h-48 overflow-y-auto md:max-h-none">
            {visible.map((item) => <button key={item.id} type="button" aria-pressed={selected === item.id}
              onClick={() => setSelected(item.id)} className={`block w-full rounded p-2 text-left text-xs coarse:min-h-12 ${selected === item.id ? "bg-primary/15" : "hover:bg-muted"}`}>
              {item.name || item.slug}
            </button>)}
            {!error && visible.length === 0 && <p className="p-2 text-xs text-muted-foreground">No matching targets available.</p>}
          </div>
        )}
      </aside>
      <main className="min-w-0 flex-1 space-y-4 overflow-y-auto p-4">
        <h2 className="text-sm font-medium">Incoming webhooks</h2>
        <p className="text-xs text-muted-foreground">{description}</p>
        {target ? <section key={`${kind}:${target.id}`} aria-label={`Webhook configuration for ${target.name}`} className="space-y-4">
          <h3 className="text-sm font-medium">{target.name}</h3>
          {(kind === "routine" || kind === "issue") && <RoutineWebhooksTab workspaceId={workspaceId} pipelineId={target.id} slug={target.slug} />}
          {kind === "agent" && <TriggersTab agentId={target.id} workspaceId={workspaceId} />}
          {kind === "page" && <PageWebhookTarget workspaceId={workspaceId} slug={target.slug} />}
          <Link className="inline-block text-xs text-primary hover:underline" href={kind === "page" ? `/pages/${encodeURIComponent(target.slug)}` : kind === "agent" ? `/activity?walk=agent:${encodeURIComponent(target.id)}` : `/activity?pipeline=${encodeURIComponent(target.slug)}`}>
            {kind === "page" ? "Open Page" : "Open activity"}
          </Link>
        </section> : <p className="rounded border border-hairline p-6 text-sm text-muted-foreground">Choose a target to create or manage its incoming webhooks. The target supplies the receiving URL; you do not need another domain.</p>}
      </main>
    </div>
  )
}

function PageWebhookTarget({ workspaceId, slug }: { workspaceId: string; slug: string }) {
  const detail = usePage(workspaceId, slug)
  const grants = usePageGrants(workspaceId, slug, true)
  if (detail.loading) return <p>Loading Page…</p>
  if (detail.error || !detail.page) return <p role="alert">{detail.error || "Page unavailable"}</p>
  const panelIDs = (detail.page.panels ?? []).filter((panel) => panel.producer?.startsWith("webhook/") || panel.producer?.startsWith("script/")).map((panel) => panel.spec.id)
  return <WebhooksCard workspaceId={workspaceId} slug={slug} panelIDs={panelIDs} canManage={grants.refusal === null} manageRefusal="Only the Page owner or an authorized administrator can issue producer tokens." />
}
