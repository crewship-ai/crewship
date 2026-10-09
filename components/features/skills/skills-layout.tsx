"use client"

import { useEffect, useMemo, useState } from "react"
import { AnimatePresence, motion } from "motion/react"
import { toast } from "sonner"
import { ChevronLeft, ChevronRight, Library, PanelLeftOpen, Sparkles, Upload } from "lucide-react"

import { ImportSkillDialog } from "@/components/skills/import-dialog"
import { SidebarCollapseButton } from "@/components/layout/sidebar-kit"
import { SubBar, SubBarPrimary } from "@/components/layout/sub-bar"
import { Button } from "@/components/ui/button"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { useIsMobile } from "@/hooks/use-mobile"
import {
  useProposedSkills,
  useReviewProposedSkill,
  useSkillsAgents,
  useSkillsCrews,
  useSkillsList,
} from "@/hooks/use-skills"
import { useUserPreference } from "@/hooks/use-user-preference"
import { useWorkspace } from "@/hooks/use-workspace"
import { cn } from "@/lib/utils"
import { SkillPage, isSkillTab, type SkillTab } from "./skill-page"
import { AssignDialog, type AssignTarget } from "./skills-dialogs"
import { SkillEditor, type SkillEditorTarget } from "./skill-editor"
import { SkillsExplorer } from "./skills-explorer"
import { SkillsOverview } from "./skills-overview"
import {
  EMPTY_SKILL_FILTERS,
  isSkillSort,
  isSkillsView,
  skillName,
  type ProposedSkill,
  type SkillFilters,
  type SkillSort,
} from "./skills-model"

// /skills (#3033) on the app's one skeleton — SubBar, the 280px explorer,
// the content pane — exactly as Routines and Issues. The URL holds what a
// reload must keep: the open skill and its tab, the View, the agent or crew
// and the domain. Popover facets and search are the explorer's own state.

const MANAGER_ROLES = new Set(["OWNER", "ADMIN", "MANAGER"])

export function SkillsLayout() {
  const { workspaceId, role, loading: wsLoading } = useWorkspace()
  const isMobile = useIsMobile()
  const [collapsed, setCollapsed] = useState(false)
  useEffect(() => {
    if (isMobile) setCollapsed(true)
  }, [isMobile])

  const list = useSkillsList(workspaceId)
  const agentsQ = useSkillsAgents(workspaceId)
  const crewsQ = useSkillsCrews(workspaceId)
  const skills = useMemo(() => list.data ?? [], [list.data])
  const agents = useMemo(() => agentsQ.data ?? [], [agentsQ.data])
  const crews = useMemo(() => crewsQ.data ?? [], [crewsQ.data])
  const canReview = MANAGER_ROLES.has((role ?? "").toUpperCase())
  const crewIds = useMemo(() => (canReview ? crews.map((c) => c.id) : []), [crews, canReview])
  const { proposed } = useProposedSkills(workspaceId, crewIds)
  const review = useReviewProposedSkill(workspaceId)

  const [skillId, setSkillId] = useUrlSelection("skill")
  const [tabParam, setTabParam] = useUrlSelection("tab")
  const [viewParam, setViewParam] = useUrlSelection("view")
  const [agentParam, setAgentParam] = useUrlSelection("agent")
  const [crewParam, setCrewParam] = useUrlSelection("crew")
  const [domainParam, setDomainParam] = useUrlSelection("domain")
  const [local, setLocal] = useState<Pick<SkillFilters, "query" | "sources" | "trust" | "maturities" | "needsCredential">>({
    query: EMPTY_SKILL_FILTERS.query,
    sources: [],
    trust: [],
    maturities: [],
    needsCredential: false,
  })
  const filters: SkillFilters = {
    ...local,
    view: isSkillsView(viewParam) ? viewParam : "all",
    agentId: agentParam,
    crewId: crewParam,
    domain: domainParam,
  }
  const tab: SkillTab = isSkillTab(tabParam) ? tabParam : "overview"

  const [sortPref, setSortPref] = useUserPreference<string>("skills.sort", "used")
  const sort: SkillSort = isSkillSort(sortPref) ? sortPref : "used"
  const [layoutPref, setLayoutPref] = useUserPreference<string>("skills.layout", "grid")
  const layout = layoutPref === "list" ? "list" : "grid"

  const [assignTarget, setAssignTarget] = useState<AssignTarget | null>(null)
  const [editor, setEditor] = useState<SkillEditorTarget | null>(null)
  const takenSlugs = useMemo(() => new Set(skills.map((x) => x.slug)), [skills])

  const closeSkill = () => {
    setTabParam(null, { replace: true })
    setSkillId(null)
  }
  const openSkill = (id: string, nextTab?: string) => {
    setTabParam(nextTab && nextTab !== "overview" ? nextTab : null, { replace: true })
    setSkillId(id)
  }

  const onChange = (patch: Partial<SkillFilters>) => {
    const { view, agentId, crewId, domain, ...rest } = patch
    if (view !== undefined) setViewParam(view === "all" ? null : view, { replace: true })
    if (agentId !== undefined) setAgentParam(agentId, { replace: true })
    if (crewId !== undefined) setCrewParam(crewId, { replace: true })
    if (domain !== undefined) setDomainParam(domain, { replace: true })
    if (Object.keys(rest).length) setLocal((l) => ({ ...l, ...rest }))
    // Changing what the list shows means "show me the list".
    if (skillId && (view !== undefined || agentId !== undefined || crewId !== undefined || domain !== undefined || rest.query !== undefined)) closeSkill()
  }
  const clearAll = () => {
    setLocal({ query: "", sources: [], trust: [], maturities: [], needsCredential: false })
    setAgentParam(null, { replace: true })
    setCrewParam(null, { replace: true })
    setDomainParam(null, { replace: true })
    setViewParam(null, { replace: true })
  }

  // `/` focuses the explorer's search, like Routines and Issues.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return
      if (e.key === "/") {
        const el = document.querySelector<HTMLInputElement>("[data-skills-search] input")
        if (el) {
          e.preventDefault()
          if (collapsed) setCollapsed(false)
          el.focus()
        }
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [collapsed])

  const onReview = async (p: ProposedSkill, approve: boolean) => {
    try {
      await review.mutateAsync({ proposal: p, approve })
      toast.success(approve ? `${p.name} added to the catalog. Give it to an agent to use it.` : `${p.name} rejected`)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not review the proposal")
    }
  }

  const selectedRow = skillId ? skills.find((s) => s.id === skillId) : undefined
  const held = skills.filter((s) => (s.installed_on?.length ?? 0) > 0).length

  return (
    <div className="flex h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] flex-col bg-background">
      <SubBar
        icon={Library}
        title="Skills"
        description={
          list.isLoading || wsLoading ? (
            "Loading…"
          ) : (
            <>
              {skills.length} {skills.length === 1 ? "skill" : "skills"} · {held} on agents
              {proposed.length > 0 && <> · {proposed.length} proposed</>}
            </>
          )
        }
        ariaLabel="Skills"
        actions={
          workspaceId ? (
            <>
              <ImportSkillDialog
                workspaceId={workspaceId}
                onImported={() => void list.refetch()}
                triggerVariant="ghost"
                triggerSize="sm"
                triggerClassName="h-7 gap-1.5 text-xs"
                triggerLabel={
                  <span className="inline-flex items-center gap-1.5 text-xs font-medium">
                    <Upload className="h-3 w-3" />
                    <span className="hidden sm:inline">Import</span>
                  </span>
                }
              />
              <SubBarPrimary icon={Sparkles} onClick={() => setEditor({ kind: "new" })} title="Describe a skill for Claude to write, or write one yourself">
                New skill
              </SubBarPrimary>
            </>
          ) : undefined
        }
      />

      <div className="relative flex flex-1 overflow-hidden">
        {isMobile && !collapsed && (
          <button
            type="button"
            aria-label="Close skills explorer"
            onClick={() => setCollapsed(true)}
            className="fixed inset-0 z-40 touch-none overscroll-contain bg-black/50"
          />
        )}
        <aside
          aria-label="Skills explorer"
          className={cn(
            "shrink-0 overflow-hidden border-r border-foreground/[0.06] bg-card transition-all",
            collapsed ? (isMobile ? "w-0 border-r-0" : "w-9") : "w-[280px]",
            isMobile && !collapsed && "fixed inset-y-0 left-0 z-50 pb-[env(safe-area-inset-bottom)] pt-[env(safe-area-inset-top)] shadow-2xl",
          )}
        >
          {collapsed ? (
            !isMobile && (
              <div className="flex h-full flex-col items-center pt-1.5">
                <SidebarCollapseButton collapsed onToggle={() => setCollapsed(false)} />
              </div>
            )
          ) : (
            <SkillsExplorer
              skills={skills}
              agents={agents}
              crews={crews}
              proposedCount={proposed.length}
              filters={filters}
              onChange={onChange}
              onPicked={() => isMobile && setCollapsed(true)}
              onToggleCollapse={() => setCollapsed(true)}
            />
          )}
        </aside>

        <div className="relative min-w-0 flex-1 overflow-hidden">
          {/* On a phone the explorer is a drawer; this opens it. */}
          {isMobile && collapsed && !skillId && (
            <div className="absolute left-3 top-3 z-10">
              <Button variant="outline" size="sm" onClick={() => setCollapsed(false)} className="h-11 gap-1.5 bg-card shadow-sm">
                <PanelLeftOpen className="h-4 w-4" />
                Filters
              </Button>
            </div>
          )}
          <AnimatePresence mode="wait">
            {skillId ? (
              <motion.div
                key={`skill-${skillId}`}
                initial={{ opacity: 0, x: 12 }}
                animate={{ opacity: 1, x: 0 }}
                exit={{ opacity: 0, x: -12 }}
                transition={{ duration: 0.18, ease: "easeOut" }}
                className="absolute inset-0 flex flex-col overflow-hidden"
              >
                <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2">
                  <button
                    type="button"
                    onClick={closeSkill}
                    className="inline-flex min-h-8 items-center gap-1.5 rounded-md px-2 py-1 text-label font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                  >
                    <ChevronLeft className="h-3.5 w-3.5" />
                    Back to skills
                  </button>
                  <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
                  <span className="truncate text-label font-medium text-foreground/85">{selectedRow ? skillName(selectedRow) : "Skill"}</span>
                  {selectedRow && <span className="ml-1 hidden truncate font-mono text-micro text-muted-foreground sm:inline">{selectedRow.slug}</span>}
                </div>
                <div className="flex-1 overflow-y-auto">
                  {workspaceId && (
                    <SkillPage
                      workspaceId={workspaceId}
                      skillId={skillId}
                      row={selectedRow}
                      agents={agents}
                      crews={crews}
                      tab={tab}
                      onTab={(t) => setTabParam(t === "overview" ? null : t, { replace: true })}
                      onAssign={() => selectedRow && setAssignTarget({ mode: "skill", skill: selectedRow })}
                      onDeleted={closeSkill}
                      onEditor={setEditor}
                    />
                  )}
                </div>
              </motion.div>
            ) : (
              <motion.div
                key="overview"
                initial={{ opacity: 0 }}
                animate={{ opacity: 1 }}
                exit={{ opacity: 0 }}
                transition={{ duration: 0.15 }}
                className={cn("absolute inset-0 overflow-y-auto", isMobile && collapsed && "pt-14")}
              >
                <SkillsOverview
                  skills={skills}
                  loading={list.isLoading || wsLoading}
                  error={list.isError}
                  onRetry={() => void list.refetch()}
                  agents={agents}
                  crews={crews}
                  proposed={proposed}
                  canReview={canReview}
                  reviewBusy={review.isPending}
                  onReview={onReview}
                  filters={filters}
                  onChange={onChange}
                  onClearAll={clearAll}
                  sort={sort}
                  onSort={setSortPref}
                  layout={layout}
                  onLayout={setLayoutPref}
                  onOpen={openSkill}
                  onAddSkillsToAgent={(id) => {
                    const a = agents.find((x) => x.id === id)
                    if (a) setAssignTarget({ mode: "agent", agent: a })
                  }}
                />
              </motion.div>
            )}
          </AnimatePresence>
        </div>
      </div>

      {workspaceId && (
        <>
          <AssignDialog
            target={assignTarget}
            workspaceId={workspaceId}
            skills={skills}
            agents={agents}
            crews={crews}
            onClose={() => setAssignTarget(null)}
          />
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
