"use client"

import * as React from "react"
import { Plus } from "lucide-react"

import type { BackupsSection } from "@/app/(dashboard)/admin/navigation"
import { SmallButton } from "./backups-kit"
import { BackupsScopeBar } from "./backups-scope-bar"
import { INSTANCE_ONLY, SECTION_TITLE } from "./backups-model"
import { useBackupsScope, type SectionCtx } from "./backups-scope"
import { BackupsOverview } from "./backups-overview"
import { BackupsHistory } from "./backups-history"
import { BackupsSchedules } from "./backups-schedules"
import { BackupsStorage } from "./backups-storage"
import { BackupsRecovery } from "./backups-recovery"
import { BackupsKeys } from "./backups-keys"
import { DataRetention } from "./data-retention"

/**
 * Admin › Backups (six pages) and Admin › Data retention: the scope strip, the
 * page heading with its actions, and the page. The Admin sidebar picks the
 * page; the scope lives in the URL (?scope=instance|workspaces&ws=slug,…) so a
 * link shows what its sender saw.
 */
export function BackupsConsole({ page, onNavigate }: {
  page: BackupsSection | "retention"
  onNavigate: (section: BackupsSection) => void
}) {
  const { workspaceId, demo, workspaces, urlState, selected, scope, commit, backUpNow } = useBackupsScope(page === "retention")
  // ?run= opens that run in Backup history: the link a backup incident's
  // inbox card carries (View failure).
  const [focus, setFocus] = React.useState<{ run: string | null; path: string | null }>(() => ({
    run: typeof window === "undefined" ? null : new URLSearchParams(window.location.search).get("run"),
    path: null,
  }))
  const [newPlanSignal, setNewPlanSignal] = React.useState(0)

  const toggle = (id: string) => {
    const next = new Set(selected)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    commit(urlState.scope, next)
  }

  const ctx: SectionCtx = {
    scope, selected, workspaces, currentWorkspaceId: workspaceId, demo,
    focusRun: focus.run, focusPath: focus.path, backUpNow, newPlanSignal,
    go: (section, opts) => {
      setFocus({ run: opts?.run ?? null, path: opts?.path ?? null })
      onNavigate(section)
    },
  }

  const head = SECTION_TITLE[page]
  const actions =
    page === "overview" || page === "history" ? (
      <>
        <SmallButton onClick={() => ctx.go("schedules")}>Set up automatic backups</SmallButton>
        <SmallButton primary onClick={() => backUpNow()}>Back up now</SmallButton>
      </>
    ) : page === "schedules" ? (
      <SmallButton primary onClick={() => setNewPlanSignal((x) => x + 1)}><Plus className="h-3.5 w-3.5" />New plan</SmallButton>
    ) : null

  const mode = page === "retention" ? "workspaces-only" : INSTANCE_ONLY.has(page) ? "instance-only" : "scoped"
  const n = workspaces.length

  return (
    <div data-slot="backups-console" data-page={page} className="text-control">
      <BackupsScopeBar mode={mode} scope={scope} onScope={(s) => commit(s, selected)} workspaces={workspaces} selected={selected} onToggle={toggle}
        instanceSummary={`${n} workspace${n === 1 ? "" : "s"}, users, instance settings, container environments`} />
      <div className="mx-auto max-w-5xl space-y-3 p-4 md:p-6">
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-2">
          <h2 className="text-sm font-semibold">{head.title}</h2>
          <span className="text-control text-muted-foreground">{head.sub}</span>
          {actions && <div className="flex flex-wrap items-center gap-1.5 sm:ml-auto">{actions}</div>}
        </div>
        {demo && <p className=" text-micro text-muted-foreground-soft">Demo data · ?demo=1 · nothing is sent</p>}
        {page === "overview" && <BackupsOverview ctx={ctx} />}
        {page === "history" && <BackupsHistory ctx={ctx} />}
        {page === "schedules" && <BackupsSchedules ctx={ctx} />}
        {page === "storage" && <BackupsStorage ctx={ctx} />}
        {page === "recovery" && <BackupsRecovery ctx={ctx} />}
        {page === "keys" && <BackupsKeys ctx={ctx} />}
        {page === "retention" && <DataRetention ctx={ctx} />}
      </div>
    </div>
  )
}
