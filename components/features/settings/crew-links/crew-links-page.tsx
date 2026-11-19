"use client"

import * as React from "react"
import { motion, useReducedMotion } from "motion/react"
import { Circle, Link2, Plus, Search, Terminal, Upload, Users, X } from "lucide-react"

import { toast } from "@/lib/toast"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { useAbilities } from "@/hooks/use-abilities"
import { isAdminTier } from "@/lib/permissions/tiers"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { DrillNavItem, DrillNavSection } from "@/components/layout/drill-page"
import { SidebarFilterButton, SidebarSearch } from "@/components/layout/sidebar-kit"
import { SidebarFacetBlock, SidebarInlineFacet, useSidebarFacets } from "@/components/layout/sidebar-facets"
import { PageEmpty, PageError } from "@/components/layout/page-state"
import { SettingsDrillPage } from "@/components/features/settings/settings-drill-page"

import {
  DEFAULT_CREW_LINK_FILTERS,
  agentCount,
  byMostLinked,
  byName,
  canDeliver,
  connectionBetween,
  crewMatches,
  duplicateNames,
  isOpenNetwork,
  linkRows,
  mostLinked,
  parseView,
  partnerCounts,
  rowAtLevel,
  type Crew,
  type CrewLinkFilters,
  type CrewLinksView,
} from "./crew-links-model"
import { CrewPage, LevelsCard } from "./crew-page"
import { CrewTile } from "./crew-profile"
import { CrewsTable, LinksList } from "./link-lists"
import { LinkDialog, type LinkDialogInit } from "./link-dialog"
import { useCrewLinks } from "./use-crew-links"
import { useLinkChanges } from "./use-link-changes"

interface UrlState { view: CrewLinksView; crew: string; filters: CrewLinkFilters }

/** ?view=, ?crew= (a slug), ?level= ?agents= ?net= ?q= keep the page shareable and
 *  reload-safe; defaults stay out of the URL. The old ?links=linked|none reads as
 *  All crews / Working alone. */
function readUrl(): UrlState {
  if (typeof window === "undefined") return { view: "crew", crew: "", filters: DEFAULT_CREW_LINK_FILTERS }
  const p = new URLSearchParams(window.location.search)
  const agents = p.get("agents")
  const net = p.get("net")
  const level = p.get("level")
  return {
    view: parseView(p.get("view"), p.get("links")),
    crew: p.get("crew") ?? "",
    filters: {
      agents: agents === "with" || agents === "none" ? agents : "",
      net: net === "restricted" || net === "open" ? net : "",
      q: p.get("q") ?? "",
      level: level === "deliver" || level === "view" || level === "work" ? level : "",
    },
  }
}
function writeUrl({ view, crew, filters }: UrlState) {
  const url = new URL(window.location.href)
  const set = (k: string, v: string, keep: boolean) => (keep ? url.searchParams.set(k, v) : url.searchParams.delete(k))
  set("view", view, view !== "crew")
  set("crew", crew, Boolean(crew) && view === "crew")
  url.searchParams.delete("links")
  set("level", filters.level, Boolean(filters.level) && (view === "all" || view === "matrix"))
  set("agents", filters.agents, Boolean(filters.agents))
  set("net", filters.net, Boolean(filters.net))
  set("q", filters.q, Boolean(filters.q))
  window.history.replaceState(window.history.state, "", url.toString())
}

const NAMED = 8

export function CrewLinksPage() {
  const { workspaceId } = useWorkspace()
  const { role } = useAbilities()
  const isMobile = useIsMobile()
  const reduce = useReducedMotion()
  const links = useCrewLinks(workspaceId)
  const { crews, connections, agentsByCrew, loading, loadError, canManage } = links
  const [state, setState] = React.useState(readUrl)
  const { view, crew: crewSlug, filters } = state
  const changes = useLinkChanges(workspaceId, isAdminTier(role), connections)

  const update = (next: Partial<UrlState>) => setState((prev) => {
    const merged = { ...prev, ...next }
    writeUrl(merged)
    return merged
  })
  const setView = (v: CrewLinksView) => update({ view: v })
  const selectCrew = (slug: string) => update({ view: "crew", crew: slug })
  const setFilters = (next: Partial<CrewLinkFilters>) => update({ filters: { ...filters, ...next } })
  const [dialog, setDialog] = React.useState<LinkDialogInit | null>(null)

  const dups = React.useMemo(() => duplicateNames(crews), [crews])
  const counts = React.useMemo(() => partnerCounts(connections), [connections])
  const agentsOf = (c: Crew) => agentsByCrew?.get(c.id) ?? (agentsByCrew ? [] : undefined)
  const sorted = React.useMemo(() => [...crews].sort(byName), [crews])
  const shown = sorted.filter((c) => crewMatches(c, filters, agentsOf(c)))
  const shownIds = new Set(shown.map((c) => c.id))
  const filtered = shown.length !== crews.length
  const rows = React.useMemo(() => linkRows(crews, connections), [crews, connections])
  // A link shows when either end passes the panel's filters.
  const visibleRows = rows.filter((r) => shownIds.has(r.a.id) || shownIds.has(r.b.id))
  const deliverRows = visibleRows.filter(canDeliver)
  const levelRows = visibleRows.filter((r) => rowAtLevel(r, filters.level))
  const crewsList = [...shown].sort(byMostLinked(counts))
  const aloneList = crewsList.filter((c) => !counts.has(c.id))
  const named = mostLinked(shown, connections, NAMED)
  const selected = crews.find((c) => c.slug === crewSlug) ?? named[0] ?? shown[0] ?? sorted[0]
  const filesKnown = connections.every((c) => c.forward_file_access != null)
  const popoverCount = Number(Boolean(filters.agents)) + Number(Boolean(filters.net))
  const facets = useSidebarFacets()
  const countWhere = (f: (c: Crew) => boolean) => crews.filter(f).length
  const newLink = (init: LinkDialogInit) => setDialog(init)
  const linkPair = (a: Crew, b: Crew) => newLink({ mode: connectionBetween(connections, a.id, b.id) ? "edit" : "new", a, b })

  const toolbar = (
    <>
      <SidebarSearch value={filters.q} onValueChange={(q) => setFilters({ q })} placeholder="Search crews, agents…" />
      <SidebarFilterButton activeCount={popoverCount} {...facets.triggerProps}>Filter crews</SidebarFilterButton>
    </>
  )

  // The facets open inside the panel under the toolbar (kit COM-01), never as a floating popover.
  const facetBlock = (
    <SidebarFacetBlock {...facets.blockProps} label="Filter crews" activeCount={popoverCount} onClear={() => setFilters({ agents: "", net: "" })} className="mt-2">
      <SidebarInlineFacet
        label="Network"
        values={[
          { value: "restricted", label: "Restricted", count: countWhere((c) => c.network_mode != null && !isOpenNetwork(c)) },
          { value: "open", label: "Open", count: countWhere(isOpenNetwork) },
        ]}
        selected={filters.net || null}
        onToggle={(v) => setFilters({ net: filters.net === v ? "" : v as CrewLinkFilters["net"] })}
        onClear={() => setFilters({ net: "" })}
      />
      <SidebarInlineFacet
        label="Agents"
        values={[
          { value: "with", label: "With agents", count: countWhere((c) => (agentCount(c, agentsOf(c)) ?? 0) > 0) },
          { value: "none", label: "No agents (collectors)", count: countWhere((c) => agentCount(c, agentsOf(c)) === 0) },
        ]}
        selected={filters.agents || null}
        onToggle={(v) => setFilters({ agents: filters.agents === v ? "" : v as CrewLinkFilters["agents"] })}
        onClear={() => setFilters({ agents: "" })}
      />
    </SidebarFacetBlock>
  )

  const viewItems: { key: CrewLinksView; label: string; icon: typeof Link2; count: number; on: boolean }[] = [
    { key: "all", label: "All links", icon: Link2, count: visibleRows.length, on: view === "all" || view === "matrix" },
    { key: "deliver", label: "Can deliver files", icon: Upload, count: deliverRows.length, on: view === "deliver" },
    { key: "crews", label: "All crews", icon: Users, count: shown.length, on: view === "crews" },
    { key: "alone", label: "Working alone", icon: Circle, count: aloneList.length, on: view === "alone" },
  ]
  // Views and the few most linked crews; the rest is one row away, never listed here.
  const nav = (
    <>
      <DrillNavSection collapsible={false} label="Views">
        {viewItems.filter((v) => v.key !== "deliver" || filesKnown).map((v, i) => (
          <DrillNavItem key={v.key} index={i} tile={v.icon} selected={v.on} onSelect={() => setView(v.key)} label={v.label} meta={v.count} />
        ))}
      </DrillNavSection>
      <DrillNavSection collapsible={false} label="Most linked">
        {named.length === 0 && <p className="px-2 py-1 text-xs text-muted-foreground">No linked crew matches.</p>}
        {named.map((c, i) => (
          <DrillNavItem key={c.id} index={i + 4} selected={view === "crew" && selected?.id === c.id} onSelect={() => selectCrew(c.slug)} title={`${c.name} · ${c.slug}`}
            icon={<CrewTile crew={c} />}
            label={<>{c.name}{dups.has(c.name) && <span className="ml-1.5 font-mono text-micro text-muted-foreground">{c.slug}</span>}</>}
            meta={<span title="Crews it works with">{counts.get(c.id) ?? 0}</span>} />
        ))}
        <DrillNavItem index={13} tile={Search} onSelect={() => setView("crews")} label={<span className="text-primary-hover">All {shown.length} crews…</span>} />
      </DrillNavSection>
    </>
  )

  const mobileNav = view === "crew" && selected ? (
    <select aria-label="Crew" value={selected.slug} onChange={(e) => selectCrew(e.target.value)}
      className="h-9 w-full rounded-md border border-control-border bg-surface-subtle px-3 text-control coarse:h-[44px]">
      {sorted.map((c) => <option key={c.id} value={c.slug}>{c.name}{dups.has(c.name) ? ` (${c.slug})` : ""}{counts.has(c.id) ? "" : " · no links"}</option>)}
    </select>
  ) : undefined

  // The filters in words, each removable, above whatever view is open.
  const chips: { key: string; label: string; clear: Partial<CrewLinkFilters> }[] = []
  if (filters.q.trim()) chips.push({ key: "q", label: `“${filters.q.trim()}”`, clear: { q: "" } })
  if (filters.agents) chips.push({ key: "agents", label: filters.agents === "with" ? "With agents" : "No agents", clear: { agents: "" } })
  if (filters.net) chips.push({ key: "net", label: filters.net === "open" ? "Open network" : "Restricted network", clear: { net: "" } })

  const resetKey = `${view}|${filters.q}|${filters.agents}|${filters.net}|${filters.level}`
  const headFor: Record<Exclude<CrewLinksView, "crew">, { title: string; sentence: string }> = {
    all: { title: "All links", sentence: `${rows.length} links between ${counts.size} of ${crews.length} crews · ${crews.length - counts.size} work alone` },
    matrix: { title: "All links", sentence: `${rows.length} links between ${counts.size} of ${crews.length} crews · ${crews.length - counts.size} work alone` },
    deliver: { title: "Can deliver files", sentence: "Links where one crew may write into the other’s incoming folder. The ones to look at first." },
    crews: { title: "All crews", sentence: `${crews.length} crews · ${counts.size} linked · ${crews.length - counts.size} work alone` },
    alone: { title: "Working alone", sentence: "Crews that hand work to nobody and receive none." },
  }

  return (
    <SettingsDrillPage
      panelId="drill-crew-links"
      title="Crew links"
      icon={Link2}
      description="Who hands work to whom, and whose shared files they see."
      note="Managers and up change links"
      actions={
        <>
          <Button variant="ghost" size="icon-sm" aria-label="Command line" title="crewship crew connect <A> <B> · crewship crew connections · crewship crew file-access"
            onClick={() => toast.info("crewship crew connect <A> <B> · crewship crew connections · crewship crew file-access")}>
            <Terminal className="h-3.5 w-3.5" />
          </Button>
          {canManage && (
            <Button size="sm" onClick={() => newLink({ mode: "new" })}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />New link
            </Button>
          )}
        </>
      }
      toolbar={toolbar}
      facets={facetBlock}
      nav={nav}
      mobileNav={mobileNav}
    >
      {loading ? (
        <div className="space-y-3 p-4 md:p-6"><Skeleton className="h-10 rounded-lg" /><Skeleton className="h-[320px] rounded-card" /></div>
      ) : (
        <div className="space-y-4 p-4 md:p-6">
          {crews.length === 0 && !loadError && (
            <PageEmpty
              className="max-w-none px-0 py-0"
              icon={Users}
              title="No crews to link yet"
              description="A link lets one crew hand work to another. Create a crew first."
              primary={{ label: "Create a crew", href: "/crews" }}
            />
          )}
          {chips.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5" data-slot="crew-links-chips">
              {chips.map((c) => (
                <motion.span key={c.key} initial={reduce ? false : { scale: 0.85, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} transition={{ duration: 0.18 }}
                  className="inline-flex h-6 items-center gap-1 rounded-full bg-primary/10 pl-2.5 pr-1 text-micro font-medium text-primary-hover">
                  {c.label}
                  <button type="button" onClick={() => setFilters(c.clear)} aria-label={`Remove filter ${c.label}`} className="grid h-4 w-4 place-items-center rounded-full hover:bg-primary/20">
                    <X className="h-3 w-3" />
                  </button>
                </motion.span>
              ))}
              <button type="button" onClick={() => setFilters({ q: "", agents: "", net: "" })} className="px-1.5 text-label text-muted-foreground hover:text-foreground">Clear all</button>
            </div>
          )}
          {loadError && (
            <PageError
              className="max-w-none px-0 py-0"
              title="Current access could not be verified"
              description="What you see may be out of date."
              onRetry={() => void links.reload()}
            />
          )}
          {!filesKnown && (
            <p className="rounded-lg border border-border bg-surface-subtle px-3 py-2 text-xs text-muted-foreground">
              This server does not report shared-file access yet, so only work handoff is shown and editable.
            </p>
          )}

          {view === "crew" && selected && (
            <motion.div key={selected.id} initial={reduce ? false : { opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.22, ease: [0.2, 0.7, 0.2, 1] }}>
              <CrewPage crew={selected} crews={sorted} connections={connections} dups={dups} links={links} agentsByCrew={agentsByCrew} filesKnown={filesKnown}
                showChanges={isAdminTier(role)} changes={changes} onLinkCrew={(c) => newLink({ mode: "new", a: c })} />
            </motion.div>
          )}

          {view !== "crew" && (
            <>
              <div>
                <h2 className="text-lg font-semibold tracking-[-0.02em]">{headFor[view].title}</h2>
                <p className="text-control text-muted-foreground">{headFor[view].sentence}{filtered ? ` · ${shown.length} of ${crews.length} crews match` : ""}</p>
              </div>
              {(view === "all" || view === "matrix" || view === "deliver") && (
                <LinksList rows={view === "deliver" ? deliverRows : levelRows} view={view} level={filters.level} onLevel={(level) => setFilters({ level })}
                  onMode={(m) => setView(m)} onOpen={(c) => selectCrew(c.slug)} dups={dups} isMobile={isMobile} resetKey={resetKey}
                  connections={connections} canManage={canManage} onCell={linkPair} />
              )}
              {(view === "crews" || view === "alone") && (
                <CrewsTable crews={view === "alone" ? aloneList : crewsList} connections={connections} agentsOf={agentsOf} dups={dups} isMobile={isMobile}
                  onOpen={(c) => selectCrew(c.slug)} resetKey={resetKey} hint={view === "alone" ? "Nothing hands work to them, and they hand work to nobody" : undefined} />
              )}
              {(view === "all" || view === "matrix" || view === "deliver") && <LevelsCard />}
            </>
          )}
        </div>
      )}
      <ConfirmDialog
        open={links.pendingUnlink !== null}
        onOpenChange={(o) => { if (!o) links.cancelUnlink() }}
        title={links.pendingUnlink ? `Unlink ${links.pendingUnlink.self.name} and ${links.pendingUnlink.other.name}?` : "Unlink?"}
        description="Agents will no longer be able to hand work between them."
        consequences={[
          { tone: "lost", text: "Work handoff in both directions" },
          { tone: "lost", text: "Shared-file access between the two crews" },
          { tone: "kept", text: "Both crews, their agents and their files" },
        ]}
        confirmLabel="Unlink"
        destructive
        onConfirm={links.confirmUnlink}
      />
      <LinkDialog init={dialog} crews={sorted} dups={dups} links={links} filesKnown={filesKnown} onClose={() => setDialog(null)} />
    </SettingsDrillPage>
  )
}
