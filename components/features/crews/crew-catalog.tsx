"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { LayoutGrid, List, Search, Users } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { usePagedList } from "@/hooks/use-paged-list"
import { cn } from "@/lib/utils"
import { WorkspaceEmpty } from "./workspace-visuals"

export interface CatalogCrew { id: string; slug: string; name: string; description?: string | null; icon?: string | null; color?: string | null; avatar_style?: string | null; _count?: { agents: number } }
export interface CatalogAgent { id: string; name: string; slug: string; crew_id: string | null; status: string; avatar_seed?: string | null; avatar_style?: string | null; avatar_url?: string | null }
export function CrewCatalog({ workspaceId, agents, onAgentSelect, onCrewSelect }: { workspaceId: string; agents: CatalogAgent[]; onAgentSelect: (slug: string) => void; onCrewSelect: (slug: string) => void }) {
  const [search, setSearch] = useState("")
  const [query, setQuery] = useState("")
  const [order, setOrder] = useState("recent")
  const [compact, setCompact] = useState(false)
  useEffect(() => { const timer = setTimeout(() => setQuery(search.trim()), 200); return () => clearTimeout(timer) }, [search])
  const crews = usePagedList<CatalogCrew>({ url: `/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}&q=${encodeURIComponent(query)}&order=${order}`, limit: 24 })
  return <div className="px-4 md:px-8 lg:px-12 py-8 space-y-6"><div><h1 className="text-2xl font-semibold">Crews &amp; agents</h1><p className="text-sm text-muted-foreground mt-2">Find your team. Give every crew a purpose and a face.</p></div>
    <div className="flex flex-wrap items-center gap-3"><div className="relative flex-1 min-w-48 max-w-md"><Search className="absolute left-3 top-3 h-4 w-4 text-muted-foreground" /><Input value={search} onChange={e => setSearch(e.target.value)} placeholder="Find a crew by name or purpose…" aria-label="Search crews" className="pl-9" /></div><select aria-label="Sort crews" value={order} onChange={e => setOrder(e.target.value)} className="rounded-lg border border-border bg-card px-3 py-2 text-sm"><option value="recent">Newest first</option><option value="name">Name A–Z</option></select><Button variant="outline" size="icon" aria-label={compact ? "Show crew cards" : "Show compact crew list"} aria-pressed={compact} onClick={() => setCompact(v => !v)}>{compact ? <LayoutGrid /> : <List />}</Button><span role="status" className="text-xs text-muted-foreground">{crews.loading ? "Loading crews…" : crews.total !== null ? `${crews.total} crews` : "Crews"}</span></div>
    {crews.error ? <div role="alert" className="text-sm text-muted-foreground">Crews could not be loaded. <Button variant="ghost" onClick={() => void crews.refresh()}>Retry</Button></div> : crews.loading ? <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3" aria-hidden="true">{[1,2,3].map(n => <div key={n} className="h-48 animate-pulse rounded-xl bg-muted/30" />)}</div> : !crews.items.length ? <WorkspaceEmpty icon={Users} title={query ? "No matching crews" : "Your first team starts here"} description={query ? "Try another name or purpose." : "Use + Crew to create a team for your work."} /> : <div className={cn("grid gap-4", !compact && "sm:grid-cols-2 2xl:grid-cols-3")}>
      {crews.items.map(crew => {
        const members = agents.filter(agent => agent.crew_id === crew.id)
        const running = members.filter(agent => agent.status === "RUNNING").length
        const complete = crew._count?.agents === members.length
        return <section key={crew.id} className={cn("group rounded-xl border border-border/60 bg-card p-5 transition-colors hover:border-primary/30", compact && "sm:flex sm:items-center sm:gap-5")}><Link className="flex items-center gap-3 min-w-0 sm:min-w-48" href={`/crews?crew=${encodeURIComponent(crew.slug)}`} onClick={event => { if (!event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && event.button === 0) { event.preventDefault(); onCrewSelect(crew.slug) } }}><CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="lg" /><h2 className="text-lg font-medium truncate group-hover:text-primary transition-colors">{crew.name}</h2></Link><p className={cn("text-sm text-muted-foreground line-clamp-2", compact ? "flex-1 mt-3 sm:mt-0" : "mt-4 min-h-10")}>{crew.description || "A team ready for its next project."}</p><div className={cn("flex items-center justify-between gap-4", compact ? "mt-3 sm:mt-0" : "mt-5")}><div className="flex -space-x-2">{members.slice(0, 4).map(agent => <button key={agent.id} title={agent.name} aria-label={`Open ${agent.name}`} onClick={() => onAgentSelect(agent.slug)} className="rounded-full ring-2 ring-card hover:z-10 focus-visible:z-10"><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style || crew.avatar_style} avatarUrl={agent.avatar_url} className="h-9 w-9" /></button>)}{!members.length && <Users className="h-5 w-5 text-muted-foreground" />}</div><span className={cn("text-xs whitespace-nowrap", running ? "text-primary" : "text-muted-foreground")}>{running ? `● ${running}${complete ? "" : "+"} running` : crew._count ? `${crew._count.agents} agents` : "Open team"}</span></div>{!compact && <Link className="text-xs text-primary inline-block mt-4" href={`/crews?crew=${encodeURIComponent(crew.slug)}`} onClick={event => { if (!event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && event.button === 0) { event.preventDefault(); onCrewSelect(crew.slug) } }}>Open crew →</Link>}</section>
      })}
    </div>}
    {crews.hasMore && !crews.loading && <Button variant="outline" disabled={crews.loadingMore} onClick={() => void crews.loadMore()}>{crews.loadingMore ? "Loading…" : "Load more crews"}</Button>}
    {agents.some(a => !a.crew_id) && <section><h2 className="text-sm font-medium mb-3">Workspace agents</h2><div className="flex flex-wrap gap-2">{agents.filter(a => !a.crew_id).map(agent => <Button key={agent.id} variant="outline" onClick={() => onAgentSelect(agent.slug)}><AgentAvatar seed={agent.avatar_seed || agent.slug} style={agent.avatar_style} avatarUrl={agent.avatar_url} className="h-6 w-6" />{agent.name}</Button>)}</div></section>}
  </div>
}
