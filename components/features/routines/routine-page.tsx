"use client"

// The routine page (Harbor 2 RT-PAGE): header, the draft bar, tabs Overview ·
// Steps · Triggers · Settings (+ Versions in Full detail), one save bar for
// what Steps and Settings change, and the publish review over the page.
//
// The page actions (Test, Run, ⋯ — or Approve for a proposal, Publish for a
// draft) are the panel's: it owns the run guards and governance handlers and
// puts them in the top bar. The tab lives in `?tab=`; older `?view=` links are
// mapped onto it once, and `?view=publish` keeps opening the publish review.

import * as React from "react"
import { SubBarActions } from "@/components/layout/sub-bar"
import { useRouter } from "next/navigation"
import { toast } from "@/lib/toast"
import { apiFetch } from "@/lib/api-fetch"
import { useUrlSelection } from "@/hooks/use-issue-detail"
import { useUserPreference } from "@/hooks/use-user-preference"
import { PageSaveBar, PageSaveProvider, usePageSave } from "@/components/ui/page-save-bar"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import { isActiveRunStatus } from "@/hooks/use-pipeline-run-records"
import type { Pipeline } from "@/hooks/use-pipelines"
import { discardRoutineDraft, loadRoutineDraft } from "@/lib/routine-drafts"
import { parseValidationErrors } from "@/lib/routine-wizard"
import { routineFilesFromDefinition } from "@/lib/routine-files"
import { isRecord } from "@/lib/routine-step-describe"
import {
  DETAIL_DEPTH_PREF,
  legacyViewTarget,
  parseTab,
  routineActivityHref,
  type DetailDepth,
  type RoutineTab,
} from "@/lib/routine-page"
import { RoutineDraftBar, RoutinePageHeader, RoutineTabRow } from "./routine-page-header"
import { RoutineOverviewTab } from "./routine-overview-tab"
import { RoutineStepsTab } from "./routine-steps-tab"
import { RoutineTriggersTab, useRoutineTriggers } from "./routine-triggers-tab"
import { RoutineSettingsTab } from "./routine-settings-tab"
import { RoutineVersionsTab } from "./routine-versions-tab"
import { RoutineComparison } from "./routine-comparison"
import { RoutinePublishDialog } from "./routine-publish-dialog"
import { useRoutineEditor, type RoutineEditor } from "./use-routine-editor"
import type { RoutineDetail } from "./routines-detail-panel"

/** What the page hands the top-bar actions: the draft's own verbs. */
export interface RoutinePageActionContext {
  openPublish: () => void
  testDraft: () => void
  testingDraft: boolean
}

export interface RoutinePageProps {
  /** The page actions (Test, Run, ⋯ …), rendered into the top bar. */
  actions?: (ctx: RoutinePageActionContext) => React.ReactNode
  /** Put the actions in the page's SubBar (SubBarActions); inline without one. */
  actionsInBar?: boolean
  routine: RoutineDetail
  workspaceId: string
  crewName?: string
  routines?: Pipeline[]
  records: PipelineRunRecord[]
  lifecycle: "active" | "proposed" | "disabled"
  /** Manager and above: edit steps and settings, publish, test. */
  canEdit: boolean
  /** Owner and Admin: turn off, delete. */
  canManage: boolean
  canRun: boolean
  /** Lifecycle and last-run pills for the header. */
  pills?: React.ReactNode
  /** Banners above the Overview: a proposal to approve, live runs. */
  top?: React.ReactNode
  onChanged: () => void
  onRun: () => void
  onTest?: () => void
  onOpenRun: (runId: string) => void
  onGovernance: (action: "disable" | "enable") => void
  onDelete: () => void
}

export function RoutinePage(props: RoutinePageProps) {
  return (
    <PageSaveProvider>
      <div className="relative flex h-full flex-col">
        <div className="flex-1 overflow-auto pb-24">
          <RoutinePageBody {...props} />
        </div>
        <PageSaveBar saveLabel="Save as draft" />
      </div>
    </PageSaveProvider>
  )
}

/** Registers the page editor with the page's one save bar. */
export function useEditorSaveBar(editor: RoutineEditor) {
  usePageSave({
    label: "Routine",
    count: editor.changeCount,
    saving: editor.busy,
    canSave: !editor.loading && !editor.unavailable,
    save: async () => {
      if (!(await editor.save())) throw new Error("The changes were not saved. The reason is shown on the page.")
    },
    discard: editor.reset,
  })
}

/** Test a draft without side effects: POST /pipelines/test_run with its definition. */
async function testDraft(workspaceId: string, routine: RoutineDetail): Promise<void> {
  const draft = await loadRoutineDraft(workspaceId, routine.slug)
  if (!draft.id || !isRecord(draft.document.definition)) throw new Error("There is no saved draft to test.")
  const crew = (draft.document.author_crew_id as string | undefined) ?? routine.author_crew_id
  const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/test_run`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ definition: draft.document.definition, sample_inputs: {}, ...(crew ? { author_crew_id: crew } : {}) }),
  })
  const data = (await res.json().catch(() => ({}))) as { status?: string; error?: string }
  const passed = res.ok && (data.status === "DRY_RUN_OK" || data.status === "COMPLETED")
  if (passed) {
    toast.success(`Draft r${draft.revision} passed the test`, { description: "Nothing was sent or written. Publish it to make it live." })
    return
  }
  const problems = parseValidationErrors(data.error || `The test did not pass (${res.status}).`)
  toast.error(`Draft r${draft.revision} needs attention`, {
    description: problems.map((p) => (p.stepIndex != null ? `Step ${p.stepIndex + 1}: ${p.message || p.path}` : p.message || p.path)).join(" · "),
  })
}

function RoutinePageBody({
  routine,
  workspaceId,
  crewName,
  routines = [],
  records,
  lifecycle,
  canEdit,
  canManage,
  canRun,
  pills,
  top,
  onChanged,
  onRun,
  onTest,
  onOpenRun,
  onGovernance,
  onDelete,
  actions,
  actionsInBar,
}: RoutinePageProps) {
  const router = useRouter()
  const [tabParam, setTabParam] = useUrlSelection("tab")
  const [viewParam, setViewParam] = useUrlSelection("view")
  const [storedDepth, setStoredDepth] = useUserPreference<DetailDepth>(DETAIL_DEPTH_PREF, "simple")
  const [visitFull, setVisitFull] = React.useState(false)
  const depth: DetailDepth = visitFull ? "full" : storedDepth === "full" ? "full" : "simple"
  const tab = parseTab(tabParam)

  // An older link: ?view=definition|plan|edit|settings|versions|history.
  React.useEffect(() => {
    const target = legacyViewTarget(viewParam)
    if (!target || target.publish) return
    if (target.activity) {
      router.replace(routineActivityHref(routine.slug))
      return
    }
    setViewParam(null, { replace: true })
    if (target.full) setVisitFull(true)
    setTabParam(target.tab === "overview" ? null : target.tab, { replace: true })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewParam, routine.slug])

  const setTab = (next: RoutineTab) => setTabParam(next === "overview" ? null : next)
  const setDepth = (next: DetailDepth) => {
    setVisitFull(false)
    setStoredDepth(next)
  }

  const editor = useRoutineEditor(workspaceId, routine, onChanged, canEdit)
  useEditorSaveBar(editor)
  const triggers = useRoutineTriggers(workspaceId, routine, lifecycle)
  const files = React.useMemo(() => routine.files ?? routineFilesFromDefinition(routine.definition), [routine.files, routine.definition])
  const publishing = viewParam === "publish" && canEdit && !!routine.draft
  const openPublish = () => setViewParam("publish")
  const closePublish = () => setViewParam(null, { replace: true })
  const [discarding, setDiscarding] = React.useState(false)
  const [testing, setTesting] = React.useState(false)
  const activeRuns = records.filter((r) => isActiveRunStatus(r.status)).length
  const draftOnly = !!routine.draft_only || !((routine.head_version ?? 0) > 0)

  const runTestDraft = async () => {
    if (testing) return
    setTesting(true)
    try {
      await testDraft(workspaceId, routine)
    } catch (e) {
      toast.error("The draft could not be tested", { description: e instanceof Error ? e.message : String(e) })
    } finally {
      setTesting(false)
    }
  }

  const actionNode = actions?.({ openPublish, testDraft: () => void runTestDraft(), testingDraft: testing })

  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 sm:p-6">
      {actionNode != null &&
        (actionsInBar ? (
          <SubBarActions>{actionNode}</SubBarActions>
        ) : (
          <div className="flex flex-wrap items-center justify-end gap-1.5" data-testid="routine-page-actions">
            {actionNode}
          </div>
        ))}
      <RoutinePageHeader
        routine={routine}
        workspaceId={workspaceId}
        crewName={crewName}
        depth={depth}
        appearance={editor.appearance}
        canEditAppearance={canEdit}
        onAppearance={(next) => void editor.saveAppearance(next)}
        onVersions={() => {
          if (depth !== "full") setVisitFull(true)
          setTab("versions")
        }}
        pills={pills}
      />
      <RoutineDraftBar
        routine={routine}
        canEdit={canEdit}
        testing={testing}
        onDiscard={() => setDiscarding(true)}
        onTest={() => void runTestDraft()}
        onPublish={openPublish}
      />
      <RoutineTabRow slug={routine.slug} tab={tab} depth={depth} triggers={draftOnly ? undefined : triggers.length} onTab={setTab} onDepth={setDepth} />

      <div role="tabpanel" aria-label={tab} className="min-w-0">
        {tab === "overview" && (
          <RoutineOverviewTab
            routine={routine}
            workspaceId={workspaceId}
            depth={depth}
            records={records}
            triggers={triggers}
            files={files}
            canRun={canRun && !draftOnly && lifecycle === "active"}
            onRun={onRun}
            onOpenRun={onOpenRun}
            onTab={setTab}
            top={top}
          />
        )}
        {tab === "steps" && (
          <RoutineStepsTab routine={routine} workspaceId={workspaceId} editor={editor} canEdit={canEdit} depth={depth} files={files} routines={routines} onTest={canEdit ? (draftOnly ? () => void runTestDraft() : onTest) : undefined} />
        )}
        {tab === "triggers" && (
          <RoutineTriggersTab workspaceId={workspaceId} routine={routine} lifecycle={lifecycle} depth={depth} rows={triggers} onOpenRun={onOpenRun} onSettings={() => setTab("settings")} />
        )}
        {tab === "settings" && (
          <RoutineSettingsTab
            routine={routine}
            workspaceId={workspaceId}
            editor={editor}
            canEdit={canEdit}
            canManage={canManage}
            depth={depth}
            lifecycle={lifecycle}
            onGovernance={onGovernance}
            onDelete={onDelete}
          />
        )}
        {tab === "versions" && (
          <div className="flex flex-col gap-4">
            <RoutineVersionsTab
              workspaceId={workspaceId}
              slug={routine.slug}
              draft={routine.draft}
              routine={routine}
              onPublish={canEdit && routine.draft ? openPublish : undefined}
              onChanged={onChanged}
            />
            {!draftOnly && (
              <details className="rounded-card border border-hairline bg-card px-4 py-3 text-xs">
                <summary className="cursor-pointer text-muted-foreground">Compare two versions on the same data · live runs, real costs</summary>
                <div className="mt-3">
                  <RoutineComparison key={`${workspaceId}:${routine.slug}`} workspaceId={workspaceId} slug={routine.slug} />
                </div>
              </details>
            )}
          </div>
        )}
      </div>

      {canEdit && routine.draft && (
        <RoutinePublishDialog
          open={publishing}
          onOpenChange={(open) => {
            if (!open) closePublish()
          }}
          workspaceId={workspaceId}
          routine={routine}
          activeRuns={activeRuns}
          onPublished={onChanged}
          onDiscarded={onChanged}
        />
      )}
      {routine.draft && (
        <ConfirmDialog
          open={discarding}
          onOpenChange={setDiscarding}
          title={`Discard draft r${routine.draft.revision}?`}
          description={draftOnly ? "The routine has never been published, so discarding the draft removes it." : "The live version stays exactly as it is."}
          consequences={[
            { tone: "lost", text: "Every change saved in this draft." },
            ...(draftOnly ? [] : [{ tone: "kept" as const, text: `Live v${routine.head_version}, its schedules and its runs.` }]),
          ]}
          confirmLabel="Discard draft"
          destructive
          onConfirm={async () => {
            try {
              await discardRoutineDraft(workspaceId, { slug: routine.slug, id: routine.draft!.id, revision: routine.draft!.revision })
              toast.success("Draft discarded")
              onChanged()
            } catch (e) {
              toast.error("Could not discard the draft", { description: e instanceof Error ? e.message : String(e) })
              throw e
            }
          }}
        />
      )}
    </div>
  )
}
