"use client"

import { useEffect, useMemo, useState } from "react"
import { Library, Plus, Terminal } from "lucide-react"

import { SecondaryPanel, useCloseSecondaryPanelSheet } from "@/components/layout/secondary-panel"
import { SubBar, SubBarIconButton, SubBarPrimary } from "@/components/layout/sub-bar"
import { PageEmpty, PageError, PageLoading, PageNoResults } from "@/components/layout/page-state"
import { useIsMobile } from "@/hooks/use-mobile"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { useProposedSkills, useReviewProposedSkill, useSkillsAgents, useSkillsCrews, useSkillsList } from "@/hooks/use-skills"
import { useWorkspace } from "@/hooks/use-workspace"
import { AddSkillDialog } from "./add-skill-dialog"
import { GiveToPicker } from "./give-to-picker"
import { SkillEditor, type SkillEditorTarget } from "./skill-editor"
import { SkillPage } from "./skill-page"
import { SkillsCliDialog } from "./skills-cli-dialog"
import { SkillsExplorer, visibleViews } from "./skills-explorer"
import { SkillsLibrary } from "./skills-library"
import { ArchivedBanner, AttentionView, ProposedView, reviewToast } from "./skills-views"
import {
  STATUS_LABEL,
  TRUST_LABEL,
  VIEW_LABEL,
  activeFilterCount,
  canGiveSkills,
  canManageSkills,
  domainMeta,
  isLegacyTab,
  isSkillGroup,
  isSkillSort,
  legacyView,
  skillSource,
  sourceCount,
  tabFromParam,
  viewFromParam,
  visibleSkills,
  type ProposedSkill,
  type SkillFilters,
  type SkillGroup,
  type SkillRow,
  type SkillSort,
  type SkillStatus,
  type SkillTab,
  type TrustLevel,
} from "./skills-model"

// /skills (Harbor 2 #3396) on the shared skeleton: the SubBar's identity and
// actions in the top bar, the SecondaryPanel, and the page. The URL holds what
// a reload must keep — the open skill and its tab, the view, every facet, the
// agent chip, sort and group; only the search text is the panel's own.

function fmt(n: number) {
  return n.toLocaleString("en-US").replace(/,/g, " ")
}

const TRUST_PARAM = new Set<TrustLevel>(["verified", "unreviewed", "flagged"])
const STATUS_PARAM = new Set<SkillStatus>(["stale", "made"])

export function SkillsLayout() {
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const phone = useIsMobile()
  const closePanelSheet = useCloseSecondaryPanelSheet()

  const list = useSkillsList(workspaceId)
  const agentsQ = useSkillsAgents(workspaceId)
  const crewsQ = useSkillsCrews(workspaceId)
  const skills = useMemo(() => list.data ?? [], [list.data])
  const agents = useMemo(() => agentsQ.data ?? [], [agentsQ.data])
  const crews = useMemo(() => crewsQ.data ?? [], [crewsQ.data])
  const canGive = canGiveSkills(role)
  // Approving and rejecting is ADMIN+ on the server (roleManage); the view
  // shows only to the roles whose buttons work.
  const canReview = canManageSkills(role)
  const crewIds = useMemo(() => (canReview ? crews.map((c) => c.id) : []), [crews, canReview])
  const { proposed } = useProposedSkills(workspaceId, crewIds)
  const review = useReviewProposedSkill(workspaceId)

  const [skillId, setSkillId] = useUrlSelection("skill")
  const [tabParam, setTabParam] = useUrlSelection("tab")
  const [viewParam, setViewParam] = useUrlSelection("view")
  const [agentParam, setAgentParam] = useUrlSelection("agent")
  const [crewParam, setCrewParam] = useUrlSelection("crew")
  const [domainParam, setDomainParam] = useUrlSelection("domain")
  const [sourceParam, setSourceParam] = useUrlSelection("source")
  const [trustParam, setTrustParam] = useUrlSelection("trust")
  const [statusParam, setStatusParam] = useUrlSelection("status")
  const [sortParam, setSortParam] = useUrlSelection("sort")
  const [groupParam, setGroupParam] = useUrlSelection("group")
  // `?q=` seeds the search once (links that carry a search); typing stays local.
  const [query, setQuery] = useState(() => (typeof window === "undefined" ? "" : (new URLSearchParams(window.location.search).get("q") ?? "")))

  // Old links: ?view=all|assigned|unassigned and ?tab=content|agents|activity
  // land on their new names, keeping every other parameter.
  useEffect(() => {
    const v = legacyView(viewParam)
    if (v) setViewParam(v === "library" ? null : v, { replace: true })
  }, [viewParam, setViewParam])
  useEffect(() => {
    if (isLegacyTab(tabParam)) {
      const t = tabFromParam(tabParam)
      setTabParam(t === "overview" ? null : t, { replace: true })
    }
  }, [tabParam, setTabParam])

  let view = viewFromParam(viewParam) ?? "library"
  if (view === "proposed" && !canReview && !wsLoading) view = "library"
  const filters: SkillFilters = {
    view,
    agentId: agentParam,
    crewId: crewParam,
    domain: domainParam,
    source: sourceParam,
    trust: trustParam && TRUST_PARAM.has(trustParam as TrustLevel) ? (trustParam as TrustLevel) : null,
    status: statusParam && STATUS_PARAM.has(statusParam as SkillStatus) ? (statusParam as SkillStatus) : null,
    query,
  }
  const sort: SkillSort = isSkillSort(sortParam) ? sortParam : "used"
  const group: SkillGroup = isSkillGroup(groupParam) ? groupParam : "none"
  const tab: SkillTab = tabFromParam(tabParam)

  const [giveTo, setGiveTo] = useState<SkillRow[] | null>(null)
  const [editor, setEditor] = useState<SkillEditorTarget | null>(null)
  const [adding, setAdding] = useState(false)
  const [cli, setCli] = useState(false)
  const takenSlugs = useMemo(() => new Set(skills.map((x) => x.slug)), [skills])
  const recentSources = useMemo(() => {
    const m = new Map<string, { label: string; url: string; n: number }>()
    for (const s of skills) {
      const src = skillSource(s)
      if (src.kind !== "repo" || !src.url) continue
      const cur = m.get(src.key) ?? { label: src.label, url: src.url, n: 0 }
      cur.n++
      m.set(src.key, cur)
    }
    return [...m.values()].sort((a, b) => b.n - a.n).slice(0, 4)
  }, [skills])

  const closeSkill = () => {
    setTabParam(null, { replace: true })
    setSkillId(null)
  }
  const openSkill = (id: string, nextTab?: SkillTab) => {
    setTabParam(nextTab && nextTab !== "overview" ? nextTab : null, { replace: true })
    setSkillId(id)
  }

  const onChange = (patch: Partial<SkillFilters>) => {
    const r = { replace: true }
    if (patch.view !== undefined) setViewParam(patch.view === "library" ? null : patch.view, r)
    if (patch.agentId !== undefined) setAgentParam(patch.agentId, r)
    if (patch.crewId !== undefined) setCrewParam(patch.crewId, r)
    if (patch.domain !== undefined) setDomainParam(patch.domain, r)
    if (patch.source !== undefined) setSourceParam(patch.source, r)
    if (patch.trust !== undefined) setTrustParam(patch.trust, r)
    if (patch.status !== undefined) setStatusParam(patch.status, r)
    if (patch.query !== undefined) setQuery(patch.query)
    // A facet narrows a list: Attention and Proposed are not lists of rows.
    const facet = ["crewId", "domain", "source", "trust", "status"].some((k) => patch[k as keyof SkillFilters] != null)
    if (facet && patch.view === undefined && (view === "attention" || view === "proposed")) setViewParam(null, r)
    // Changing what the list shows means "show me the list".
    if (skillId && Object.keys(patch).some((k) => k !== "query" || patch.query)) closeSkill()
  }
  const clearFilters = () => {
    onChange({ agentId: null, crewId: null, domain: null, source: null, trust: null, status: null, query: "" })
  }

  // `/` focuses the panel search; 1–6 switch views, as in Issues.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return
      if (e.metaKey || e.ctrlKey || e.altKey) return
      if (document.querySelector("[role=dialog]")) return
      if (e.key === "/") {
        const el = document.querySelector<HTMLInputElement>("[data-skills-search] input")
        if (el) {
          e.preventDefault()
          el.focus()
        }
        return
      }
      const views = visibleViews(canReview)
      const n = Number(e.key)
      if (Number.isInteger(n) && n >= 1 && n <= views.length) {
        e.preventDefault()
        onChange({ view: views[n - 1] })
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  })

  const onReview = async (p: ProposedSkill, approve: boolean) => {
    try {
      await review.mutateAsync({ proposal: p, approve })
      reviewToast(p, approve)
    } catch (e) {
      reviewToast(p, approve, e)
      throw e
    }
  }

  const loading = list.isLoading || wsLoading
  const rows = useMemo(() => visibleSkills(skills, filters), [skills, filters.view, filters.agentId, filters.crewId, filters.domain, filters.source, filters.trust, filters.status, filters.query]) // eslint-disable-line react-hooks/exhaustive-deps
  const selectedRow = skillId ? skills.find((s) => s.id === skillId) : undefined
  const libraryCount = skills.filter((s) => (s.lifecycle_state ?? "").toLowerCase() !== "archived").length

  const agent = filters.agentId ? agents.find((a) => a.id === filters.agentId) : undefined
  const pageTitle = agent
    ? `Agent: ${agent.name}`
    : filters.source
      ? (skills.map(skillSource).find((s) => s.key === filters.source)?.label ?? VIEW_LABEL[view])
      : filters.domain
        ? domainMeta(filters.domain).label
        : VIEW_LABEL[view]

  const filterWords = [
    agent && `Agent: ${agent.name}`,
    filters.source && `Source: ${pageTitle}`,
    filters.domain && `Domain: ${domainMeta(filters.domain).label}`,
    filters.trust && `Trust: ${TRUST_LABEL[filters.trust]}`,
    filters.status && `Status: ${STATUS_LABEL[filters.status]}`,
    filters.crewId && `Used by: ${crews.find((c) => c.id === filters.crewId)?.name ?? "crew"}`,
    filters.query && `“${filters.query}”`,
  ]
    .filter(Boolean)
    .join(" · ")

  const addSkill = () => setAdding(true)

  let content: React.ReactNode
  if (loading) content = <PageLoading variant="cards" onRetry={() => void list.refetch()} />
  else if (list.isError) content = <PageError title="Skills could not be loaded" description="Nothing in the library changed. Try again in a moment." onRetry={() => void list.refetch()} />
  else if (skills.length === 0)
    content = (
      <PageEmpty
        icon={Library}
        title="No skills yet"
        description="Add Agent Skills from a repository or write one here."
        primary={{ label: "Add skill", onClick: addSkill }}
        cli="crewship skill import --repo <git-url>"
      />
    )
  else if (view === "attention") content = <AttentionView skills={rows} onOpen={openSkill} />
  else if (view === "proposed") content = <ProposedView proposed={proposed} crews={crews} busy={review.isPending} onReview={onReview} />
  else
    content = (
      <div className="flex flex-col gap-3">
        {view === "archived" && <ArchivedBanner />}
        <SkillsLibrary
          rows={rows}
          sort={sort}
          group={group}
          onSort={(s) => setSortParam(s === "used" ? null : s, { replace: true })}
          onGroup={(g) => setGroupParam(g === "none" ? null : g, { replace: true })}
          onOpen={(id) => openSkill(id)}
          onShowGroup={(g, key) => {
            setGroupParam(null, { replace: true })
            onChange(g === "source" ? { source: key } : { domain: key })
          }}
          canGive={canGive}
          onGiveToCrew={setGiveTo}
          phone={phone}
          empty={
            activeFilterCount(filters) > 0 || filters.query ? (
              <PageNoResults filters={filterWords} onClear={clearFilters} />
            ) : (
              <PageEmpty
                icon={Library}
                title={view === "archived" ? "Nothing archived" : view === "used" ? "No skill is on an agent yet" : "Every skill is on an agent"}
                description={view === "archived" ? "A skill nobody used for 90 days is archived here." : "Give skills to agents from a skill's page."}
              />
            )
          }
        />
      </div>
    )

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      {!skillId && (
        <SubBar
          icon={Library}
          title="Skills"
          section={pageTitle}
          heading={false}
          ariaLabel="Skills"
          actions={
            workspaceId ? (
              <>
                <SubBarIconButton icon={Terminal} aria-label="Command line" title="Skills from the command line" onClick={() => setCli(true)} />
                {phone ? (
                  <SubBarIconButton icon={Plus} aria-label="Add skill" onClick={addSkill} />
                ) : (
                  <SubBarPrimary icon={Plus} onClick={addSkill}>
                    Add skill
                  </SubBarPrimary>
                )}
              </>
            ) : undefined
          }
        />
      )}

      <div className="relative flex flex-1 overflow-hidden">
        <SecondaryPanel id="skills" label="Skills explorer">
          <SkillsExplorer
            skills={skills}
            agents={agents}
            crews={crews}
            proposedCount={proposed.length}
            canReview={canReview}
            filters={filters}
            onChange={onChange}
            onPicked={closePanelSheet}
            skillOpen={!!skillId}
          />
        </SecondaryPanel>

        <main className="relative min-w-0 flex-1 overflow-y-auto" data-slot="skills-content">
          {skillId && workspaceId ? (
            <SkillPage
              key={skillId}
              workspaceId={workspaceId}
              skillId={skillId}
              row={selectedRow}
              agents={agents}
              crews={crews}
              tab={tab}
              onTab={(t) => setTabParam(t === "overview" ? null : t, { replace: true })}
              onBack={closeSkill}
              onGive={(s) => setGiveTo([s])}
              onDeleted={closeSkill}
              onEditor={setEditor}
              canGive={canGive}
              canManage={canManageSkills(role)}
            />
          ) : (
            <div className="mx-auto flex w-full max-w-[1240px] flex-col gap-4 p-4 pb-24 md:p-6" data-slot="skills-page">
              <div className="flex flex-col gap-1" data-slot="skills-head">
                <h1 className="text-title font-semibold tracking-tight text-balance">{pageTitle}</h1>
                <p className="text-control text-muted-foreground">
                  Instructions agents load when a task needs them · {fmt(libraryCount)} in the library from {sourceCount(skills)} {sourceCount(skills) === 1 ? "source" : "sources"}
                </p>
              </div>
              {content}
            </div>
          )}
        </main>
      </div>

      {workspaceId && (
        <>
          <GiveToPicker skills={giveTo} workspaceId={workspaceId} agents={agents} crews={crews} onClose={() => setGiveTo(null)} />
          <AddSkillDialog
            open={adding}
            onOpenChange={setAdding}
            workspaceId={workspaceId}
            crews={crews}
            agents={agents}
            takenSlugs={takenSlugs}
            recentSources={recentSources}
            onEditor={(method) => setEditor({ kind: "new", method })}
            onAdded={(ids) => {
              void list.refetch()
              if (ids.length === 1) openSkill(ids[0])
            }}
          />
          <SkillsCliDialog open={cli} onOpenChange={setCli} />
          <SkillEditor
            target={editor}
            workspaceId={workspaceId}
            takenSlugs={takenSlugs}
            onClose={() => setEditor(null)}
            onSaved={(id) => {
              const wasEdit = editor?.kind === "edit"
              setEditor(null)
              if (!wasEdit) openSkill(id)
            }}
          />
        </>
      )}
    </div>
  )
}
