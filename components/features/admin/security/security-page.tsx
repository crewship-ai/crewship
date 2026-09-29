"use client"

import * as React from "react"
import { Bell, BookOpen, Brain, Building2, Clock, Eye, Gavel, Home, KeyRound, ListChecks, RefreshCw, Shield, Sparkles, Timer, type LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SettingsSummary, SummaryItem } from "@/components/features/settings/shared"
import { KeeperJudgeCard } from "@/components/features/admin/keeper-judge-card"
import { KeeperProfileCard } from "@/components/features/admin/keeper-profile-card"
import { KeeperGovernancePanel } from "@/components/features/admin/keeper-governance-panel"
import { JudgeModelsCard } from "@/components/features/admin/judge-models-card"
import { SecurityOverview } from "./security-overview"
import { SecurityActivity } from "./security-activity"
import { useSecurity } from "./use-security"
import { SCOPE_HINT, SCOPE_LABEL, SETTINGS, STREAMS, isSection, streamCounts, type Section, type SettingsSection, type Stream } from "./security-model"

const STREAM_ICON: Record<Stream, LucideIcon> = {
  requests: KeyRound, skill_review: Sparkles, behavior: Eye, memory_health: Brain, negative_learning: BookOpen,
}
const SETTINGS_ICON: Record<SettingsSection, LucideIcon> = {
  judge: Gavel, rules: ListChecks, "workspace-judge": Building2, background: Clock, watchdog: Shield, alerts: Bell, leases: Timer,
}

interface UrlState { section: Section; stream: Stream | "all" }

/** ?section= and ?stream= keep the page shareable and reload-safe. */
function readUrl(): UrlState {
  if (typeof window === "undefined") return { section: "overview", stream: "all" }
  const p = new URLSearchParams(window.location.search)
  const s = p.get("section")
  const st = p.get("stream")
  return {
    section: isSection(s) ? s : "overview",
    stream: STREAMS.some((x) => x.key === st) ? (st as Stream) : "all",
  }
}
function writeUrl(s: UrlState) {
  const url = new URL(window.location.href)
  if (s.section === "overview") url.searchParams.delete("section"); else url.searchParams.set("section", s.section)
  if (s.section === "activity" && s.stream !== "all") url.searchParams.set("stream", s.stream); else url.searchParams.delete("stream")
  window.history.replaceState(window.history.state, "", url.toString())
}

/**
 * Admin › Security. Posture, Keeper and Keeper reviews were three tabs, and
 * Keeper alone was a 3,300px wall of seven configuration cards under a status
 * strip and two logs. One nested page now, built like People & workspaces:
 * "← Admin", its own panel (Overview, the activity streams, the settings), and
 * one thing at a time in the middle. Every settings section says whether it
 * reaches the whole instance or this workspace only.
 *
 * The settings cards are the existing ones, unchanged: each keeps its own
 * load, save and error path. This page decides only where they appear.
 */
export function SecurityPage() {
  const { workspaceId } = useWorkspace()
  const data = useSecurity(workspaceId)
  const [state, setState] = React.useState(readUrl)
  const { section, stream } = state
  const update = (next: Partial<UrlState>) => setState((prev) => {
    const merged = { ...prev, ...next }
    writeUrl(merged)
    return merged
  })
  const counts = streamCounts(data.entries)
  const settings = SETTINGS.find((s) => s.key === section)

  const nav = (
    <>
      <DrillNavSection label="Status" collapsible={false}>
        <DrillNavItem selected={section === "overview"} onSelect={() => update({ section: "overview" })}
          icon={<Home className="h-3.5 w-3.5" />} label="Overview" />
      </DrillNavSection>
      <DrillNavSection label="Activity" count={data.entries.length}>
        {STREAMS.map((s, i) => {
          const Icon = STREAM_ICON[s.key]
          const on = section === "activity" && stream === s.key
          return (
            <DrillNavItem key={s.key} index={i} selected={on} muted={!counts[s.key]}
              onSelect={() => update({ section: "activity", stream: s.key })}
              icon={<Icon className="h-3.5 w-3.5" />} label={s.label} meta={counts[s.key]} />
          )
        })}
      </DrillNavSection>
      <DrillNavSection label="Settings" count={SETTINGS.length}>
        {SETTINGS.map((s, i) => {
          const Icon = SETTINGS_ICON[s.key]
          return (
            <DrillNavItem key={s.key} index={i} selected={section === s.key} onSelect={() => update({ section: s.key })}
              icon={<Icon className="h-3.5 w-3.5" />} label={s.label} title={SCOPE_HINT[s.scope]}
              meta={<span className={cn("rounded px-1 text-[9.5px] uppercase", s.scope === "instance" ? "bg-primary/10 text-primary-hover" : "bg-muted")}>{s.scope === "instance" ? "inst" : "ws"}</span>} />
          )
        })}
      </DrillNavSection>
    </>
  )

  const mobileNav = (
    <select aria-label="Section" value={section === "activity" ? `activity:${stream}` : section}
      onChange={(e) => {
        const v = e.target.value
        if (v.startsWith("activity:")) update({ section: "activity", stream: v.slice(9) as Stream | "all" })
        else update({ section: v as Section })
      }}
      className="h-9 w-full rounded-md border border-control-border bg-surface-subtle px-3 text-control">
      <option value="overview">Overview</option>
      <option value="activity:all">Activity · all</option>
      {STREAMS.map((s) => <option key={s.key} value={`activity:${s.key}`}>Activity · {s.label}</option>)}
      {SETTINGS.map((s) => <option key={s.key} value={s.key}>{s.label}</option>)}
    </select>
  )

  let body: React.ReactNode
  if (!workspaceId || (data.loading && !data.status && data.entries.length === 0)) {
    body = <div className="space-y-3"><Skeleton className="h-8 rounded-lg" /><Skeleton className="h-[280px] rounded-card" /></div>
  } else if (section === "overview") {
    body = <SecurityOverview status={data.status} posture={data.posture} postureError={data.postureError} entries={data.entries}
      workspaceId={workspaceId} onOpenActivity={() => update({ section: "activity", stream: "all" })} />
  } else if (section === "activity") {
    body = <SecurityActivity entries={data.entries} stream={stream} onStream={(s) => update({ stream: s })} live={data.live} error={data.activityError} />
  } else if (settings) {
    body = (
      <>
        <SettingsSummary slot="security-scope">
          <SummaryItem>
            <span className={cn("mr-2 rounded-full px-2 font-mono text-[10.5px]", settings.scope === "instance" ? "bg-primary/10 text-primary-hover" : "bg-muted text-muted-foreground")}>
              {SCOPE_LABEL[settings.scope]}
            </span>
            {SCOPE_HINT[settings.scope]}
          </SummaryItem>
          <SummaryItem>{settings.about}</SummaryItem>
        </SettingsSummary>
        {section === "judge" && <KeeperJudgeCard workspaceId={workspaceId} />}
        {section === "rules" && <KeeperProfileCard workspaceId={workspaceId} />}
        {section === "workspace-judge" && <KeeperGovernancePanel workspaceId={workspaceId} serverEnabled={data.status?.enabled ?? false} section="judge" />}
        {section === "background" && <JudgeModelsCard workspaceId={workspaceId} />}
        {section === "watchdog" && <KeeperGovernancePanel workspaceId={workspaceId} serverEnabled={data.status?.enabled ?? false} section="watchdog" />}
        {section === "alerts" && <KeeperGovernancePanel workspaceId={workspaceId} serverEnabled={data.status?.enabled ?? false} section="alerts" />}
        {section === "leases" && <KeeperGovernancePanel workspaceId={workspaceId} serverEnabled={data.status?.enabled ?? false} section="leases" />}
      </>
    )
  }

  return (
    <DrillPage
      parent={{ href: "/admin", label: "Admin", icon: Shield }}
      title="Security"
      icon={Shield}
      description="Who may read a secret, and how this server is set up"
      nav={nav}
      mobileNav={mobileNav}
      actions={
        <Button size="sm" variant="outline" className="h-7 text-xs" onClick={() => void data.reload()} disabled={data.loading}>
          <RefreshCw className={cn(data.loading && "animate-spin")} />Refresh
        </Button>
      }
    >
      <div className={cn("mx-auto space-y-4 p-4 md:p-6", section === "activity" ? "max-w-5xl" : "max-w-3xl")}>
        {body}
      </div>
    </DrillPage>
  )
}

