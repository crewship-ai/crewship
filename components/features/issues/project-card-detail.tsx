"use client"

// The project detail, as one scrolling surface of cards — the same shape as
// the issue card next to it and the routine card before that.
//
// What went, and why:
//
//   The 360px    the old panel was a right rail rendered at full width. Its
//   rail         property rows put a 72px label on the left and pushed the
//                value to `justify-end`, so on a wide screen "Status" sat at
//                one edge of the monitor and "In Progress" at the other,
//                with a metre of nothing between them.
//   Three        a breadcrumb saying the project's name, above a header
//   headers      saying "Project", above a title saying the name again.
//                One identity card says it once.
//   No issues    a project page that never showed the project's issues. The
//                one question you open a project to ask.
//
// Ordered by what an operator asks: what is it → how much is done → what is
// in it → who is on it → what shape is it in.

import * as React from "react"
import Link from "next/link"
import {
  Activity,
  CalendarClock,
  CircleDot,
  Flag,
  FolderKanban,
  Tag,
  UserCircle2,
  Users,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { formatDate, formatShortDate, relTime } from "@/lib/time"
import { Appear, DetailCard, EntityChip, Pill, StatStrip } from "@/components/ui/detail"
import { TintedCard, type TintTone } from "./tinted-card"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusIcon, statusLabel } from "@/components/features/issues/status-icon"
import { PriorityIcon, priorityLabel } from "@/components/features/issues/priority-icon"
import { ProjectStatusIcon } from "@/components/features/issues/project-status-icon"
import {
  ProjectDatesPicker,
  ProjectHealthPicker,
  ProjectIconEditor,
  ProjectLeadPicker,
  ProjectNameEditor,
  ProjectPriorityPicker,
  ProjectStatusPicker,
  type ProjectCardEdit,
} from "@/components/features/issues/project-card-editors"
import { getCrewIconDef, iconColorProps } from "@/lib/entities"
import { ISSUE_STATUS_COLORS, CREW_COLOR_DEFAULT } from "@/lib/colors"
import {
  issuePriorityTone,
  projectFacts,
  projectHealthLabel,
  projectHealthTone,
  projectStatusLabel,
} from "@/lib/issue-facts"
import type { Mission, Project, ProjectStats } from "@/lib/types/mission"

interface Props {
  project: Project
  stats: ProjectStats | null
  /** The project's issues, already filtered by the caller. */
  issues: Mission[]
  actions?: React.ReactNode
  /** Makes the card writable. Absent, every property renders as text. */
  edit?: ProjectCardEdit
}

type BreakdownTab = "assignees" | "labels"

/**
 * Health, as the one verdict on this page. `DetailCard`'s tone scale has no
 * destructive border, so off-track routes to the tinted card instead — which
 * is the louder treatment anyway, and off-track is the loud case.
 */
const HEALTH_TINT: Record<Project["health"], TintTone> = {
  on_track: "success",
  at_risk: "warn",
  off_track: "destructive",
}

function clamp(n: number): number {
  return Math.max(0, Math.min(100, Math.round(n)))
}

export function ProjectCardDetail({ project, stats, issues, actions, edit }: Props) {
  const [breakdown, setBreakdown] = React.useState<BreakdownTab>("assignees")

  const facts = React.useMemo(() => projectFacts(project, stats), [project, stats])
  const scope = stats?.total_issues ?? project.issue_count
  const done = stats?.completed_issues ?? project.done_count
  const Icon = getCrewIconDef(project.icon || "folder").icon
  const segments = React.useMemo(() => donutSegments(stats), [stats])

  return (
    <div className="flex flex-col gap-4 p-4">
      <Appear order={0}>
        <DetailCard>
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="flex min-w-0 items-start gap-3">
                {edit ? (
                  // The icon+colour swatch, straight from the rail's header —
                  // the one thing up there that was worth keeping.
                  <div className="shrink-0">
                    <ProjectIconEditor project={project} edit={edit} />
                  </div>
                ) : (
                  <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border border-border/60 bg-surface-raised">
                    <Icon className={cn("h-5 w-5", iconColorProps(project.color).className)} style={iconColorProps(project.color).style} />
                  </div>
                )}
                <div className="min-w-0">
                  {/* The name, in full, once. */}
                  {edit ? (
                    <ProjectNameEditor
                      name={project.name}
                      onSave={(next) => void edit.patch({ name: next })}
                    />
                  ) : (
                    <h1 className="truncate text-lg font-semibold tracking-tight">{project.name}</h1>
                  )}
                  <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                    <span className="font-mono">{project.slug}</span>
                    {project.lead_name && (
                      <>
                        <span aria-hidden>·</span>
                        <span className="inline-flex items-center gap-1.5 rounded-full border border-border/60 py-0.5 pl-0.5 pr-2">
                          <AgentAvatar
                            seed={project.lead_id ?? project.lead_name}
                            className="h-4 w-4"
                            alt=""
                          />
                          <span className="font-medium">{project.lead_name}</span>
                        </span>
                      </>
                    )}
                  </div>
                </div>
              </div>
              {actions && <div className="flex shrink-0 items-center gap-1.5">{actions}</div>}
            </div>

            {project.description && (
              <p className="max-w-[80ch] text-[13px] leading-relaxed text-foreground/85">
                {project.description}
              </p>
            )}

            <div className="flex flex-wrap items-center gap-1.5">
              <Pill tone="default">
                <ProjectStatusIcon status={project.status} className="h-3 w-3" />
                {projectStatusLabel(project.status)}
              </Pill>
              <Pill tone={projectHealthTone(project.health)}>
                {projectHealthLabel(project.health)}
              </Pill>
              {project.priority && project.priority !== "none" && (
                <Pill tone={issuePriorityTone(project.priority)}>
                  <PriorityIcon priority={project.priority} className="h-3 w-3" />
                  {priorityLabel[project.priority]}
                </Pill>
              )}
              {/* Same source as the Scope figure below. Reading the row's
                  counter here while the band reads /stats puts two different
                  answers to "how big is this" on one card. */}
              <Pill tone="default">
                {scope} {scope === 1 ? "issue" : "issues"}
              </Pill>
            </div>
          </div>
        </DetailCard>
      </Appear>

      <Appear order={1}>
        <StatStrip items={facts} />
      </Appear>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3 2xl:grid-cols-4">
        <div className="flex flex-col gap-4 xl:col-span-2 2xl:col-span-3">
          {/* The question you opened the project to ask. */}
          <Appear order={2}>
            <DetailCard
              title="Issues"
              icon={CircleDot}
              subtitle={String(issues.length)}
              bare
              footer={
                <Link
                  href={`/issues?project=${encodeURIComponent(project.id)}`}
                  className="text-[11px] text-primary hover:underline"
                >
                  Open these on the board
                </Link>
              }
            >
              {issues.length === 0 ? (
                <p className="px-4 py-4 text-[12px] text-muted-foreground">
                  Nothing is filed under this project yet.
                </p>
              ) : (
                <ul className="divide-y divide-hairline">
                  {issues.map((i) => (
                    <li key={i.id}>
                      <Link
                        href={`/issues/${encodeURIComponent(i.identifier ?? i.id)}`}
                        className="flex items-center gap-2.5 px-4 py-2 text-[12px] transition-colors hover:bg-white/[0.03]"
                      >
                        <StatusIcon status={i.status} className="h-3.5 w-3.5 shrink-0" />
                        <span className="w-[54px] shrink-0 truncate font-mono text-[10px] text-muted-foreground">
                          {i.identifier ?? i.id.slice(0, 6)}
                        </span>
                        <span className="min-w-0 flex-1 truncate text-foreground/85">{i.title}</span>
                        <span className="shrink-0 text-[10px] text-muted-foreground-soft">
                          {statusLabel[i.status] ?? i.status}
                        </span>
                        {i.assignee_id && (
                          <AgentAvatar
                            seed={i.assignee_id}
                            className="h-4 w-4 shrink-0 rounded-full"
                            alt={i.assignee_name ?? ""}
                          />
                        )}
                        <PriorityIcon
                          priority={i.priority ?? "none"}
                          className="h-3 w-3 shrink-0"
                        />
                      </Link>
                    </li>
                  ))}
                </ul>
              )}
            </DetailCard>
          </Appear>

          <Appear order={3}>
            <DetailCard
              title="Breakdown"
              subtitle={breakdown === "assignees" ? "who is carrying it" : "what kind of work"}
              action={
                <div className="flex items-center gap-0.5 rounded-md border border-border/60 p-0.5">
                  {(["assignees", "labels"] as const).map((t) => (
                    <button
                      key={t}
                      type="button"
                      onClick={() => setBreakdown(t)}
                      aria-pressed={breakdown === t}
                      className={cn(
                        "rounded px-1.5 py-0.5 text-[10px] font-medium capitalize transition-colors",
                        breakdown === t
                          ? "bg-primary/15 text-primary"
                          : "text-muted-foreground hover:text-foreground",
                      )}
                    >
                      {t}
                    </button>
                  ))}
                </div>
              }
            >
              {breakdown === "assignees" ? (
                (stats?.by_assignee?.length ?? 0) === 0 ? (
                  <p className="text-[12px] text-muted-foreground">Nothing is assigned yet.</p>
                ) : (
                  <ul className="space-y-2">
                    {stats!.by_assignee.map((a) => (
                      <li key={a.agent_id || a.agent_name} className="flex items-center gap-2.5">
                        <AgentAvatar
                          seed={a.agent_id || a.agent_name}
                          className="h-5 w-5 rounded-full"
                          alt=""
                        />
                        <span className="min-w-0 flex-1 truncate text-[12px] text-foreground/85">
                          {a.agent_name}
                        </span>
                        <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
                          {a.completed} of {a.total}
                        </span>
                        <div className="h-1.5 w-16 shrink-0 overflow-hidden rounded-full bg-white/[0.06]">
                          <div
                            className="h-full rounded-full bg-primary/70"
                            style={{
                              width: `${a.total > 0 ? (a.completed / a.total) * 100 : 0}%`,
                            }}
                          />
                        </div>
                      </li>
                    ))}
                  </ul>
                )
              ) : (stats?.by_label?.length ?? 0) === 0 ? (
                <p className="text-[12px] text-muted-foreground">No labels in use here.</p>
              ) : (
                <ul className="space-y-2">
                  {stats!.by_label.map((l) => (
                    <li key={l.label_name} className="flex items-center gap-2.5">
                      <span
                        className="h-2.5 w-2.5 shrink-0 rounded-full"
                        style={{ backgroundColor: l.color }}
                      />
                      <span className="min-w-0 flex-1 truncate text-[12px] text-foreground/85">
                        {l.label_name}
                      </span>
                      <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
                        {l.count}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </DetailCard>
          </Appear>
        </div>

        <div className="flex flex-col gap-4">
          <Appear order={4}>
            <TintedCard
              tone={HEALTH_TINT[project.health]}
              icon={Activity}
              title={`${projectHealthLabel(project.health)} · ${clamp(project.progress)}% done`}
              subtitle={`${done} of ${scope} closed`}
            >
              {segments.length === 0 ? (
                <p className="text-[12px] text-muted-foreground">
                  No issues to measure yet.
                </p>
              ) : (
                <div className="flex items-center gap-4">
                  <svg viewBox="0 0 40 40" className="h-14 w-14 shrink-0" aria-hidden>
                    {segments.map((s) => (
                      <circle
                        key={s.status}
                        cx="20"
                        cy="20"
                        r="16"
                        fill="none"
                        stroke={s.color}
                        strokeWidth="5"
                        strokeDasharray={s.dasharray}
                        strokeDashoffset={s.dashoffset}
                        transform="rotate(-90 20 20)"
                      />
                    ))}
                  </svg>
                  <ul className="min-w-0 flex-1 space-y-0.5">
                    {segments.map((s) => (
                      <li key={s.status} className="flex items-center gap-1.5 text-[11px]">
                        <span
                          className="h-2 w-2 shrink-0 rounded-sm"
                          style={{ backgroundColor: s.color }}
                        />
                        <span className="min-w-0 flex-1 truncate capitalize text-muted-foreground">
                          {s.status.toLowerCase().replace(/_/g, " ")}
                        </span>
                        <span className="tabular-nums text-muted-foreground-soft">{s.value}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </TintedCard>
          </Appear>

          <Appear order={5}>
            <DetailCard title="Properties">
              <dl className="space-y-0.5">
                <Row icon={FolderKanban} label="Status">
                  {edit ? (
                    <ProjectStatusPicker project={project} edit={edit} />
                  ) : (
                    <span className="inline-flex items-center gap-1.5">
                      <ProjectStatusIcon status={project.status} className="h-3.5 w-3.5" />
                      {projectStatusLabel(project.status)}
                    </span>
                  )}
                </Row>
                {/* Priority had a row in the rail and only a pill here — a
                    pill you cannot click is not where you change a priority. */}
                {edit && (
                  <Row icon={Flag} label="Priority">
                    <ProjectPriorityPicker project={project} edit={edit} />
                  </Row>
                )}
                <Row icon={CircleDot} label="Health">
                  {edit ? (
                    <ProjectHealthPicker project={project} edit={edit} />
                  ) : (
                    projectHealthLabel(project.health)
                  )}
                </Row>
                <Row icon={UserCircle2} label="Lead">
                  {edit ? (
                    <ProjectLeadPicker project={project} edit={edit} />
                  ) : project.lead_name ? (
                    <span className="inline-flex items-center gap-1.5">
                      <AgentAvatar
                        seed={project.lead_id ?? project.lead_name}
                        className="h-4 w-4"
                        alt=""
                      />
                      {project.lead_name}
                    </span>
                  ) : (
                    <span className="text-muted-foreground-soft">No lead</span>
                  )}
                </Row>
                <Row icon={CalendarClock} label="Dates">
                  {edit ? (
                    <ProjectDatesPicker project={project} edit={edit}>
                      <span className="truncate text-[12px]">{dateRange(project)}</span>
                    </ProjectDatesPicker>
                  ) : (
                    dateRange(project)
                  )}
                </Row>
              </dl>
            </DetailCard>
          </Appear>

        </div>
      </div>

      {/* Short, glanceable, and none of it worth a rail slot. Spanning the
          full width is also what keeps the page from ending in a column of
          dead space beside a rail that outgrew the main column. */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <Appear order={6}>
          <DetailCard title="Teams" icon={Users}>
            {(stats?.crews?.length ?? 0) === 0 ? (
              <p className="text-[12px] text-muted-foreground">
                No crew has picked up work here yet.
              </p>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {stats!.crews.map((c) => (
                  <EntityChip key={c} icon={Users} label={c} />
                ))}
              </div>
            )}
          </DetailCard>
        </Appear>

        <Appear order={7}>
          <DetailCard title="Labels" icon={Tag}>
            {(stats?.by_label?.length ?? 0) === 0 ? (
              <p className="text-[12px] text-muted-foreground">No labels.</p>
            ) : (
              <div className="flex flex-wrap gap-1.5">
                {stats!.by_label.map((l) => (
                  <span
                    key={l.label_name}
                    className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px]"
                    style={{ backgroundColor: `${l.color}1f`, color: l.color }}
                  >
                    <span
                      className="h-1.5 w-1.5 rounded-full"
                      style={{ backgroundColor: l.color }}
                    />
                    {l.label_name}
                  </span>
                ))}
              </div>
            )}
          </DetailCard>
        </Appear>

        <Appear order={8}>
          <DetailCard title="Metadata">
            <dl className="grid grid-cols-2 gap-x-3 gap-y-2 text-[11px]">
              <Fact label="created" value={formatDate(project.created_at)} />
              <Fact label="updated" value={relTime(project.updated_at)} />
              <Fact label="slug" value={project.slug} mono />
              <Fact label="id" value={project.id} mono />
            </dl>
          </DetailCard>
        </Appear>
      </div>
    </div>
  )
}

/* ------------------------------------------------------------------ *
 *  Pieces                                                             *
 * ------------------------------------------------------------------ */

function Row({
  icon: Icon,
  label,
  children,
}: {
  icon: React.ComponentType<{ className?: string }>
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex items-center gap-2 py-1 text-[12px]">
      <Icon className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
      <dt className="w-[60px] shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 truncate text-foreground/85">{children}</dd>
    </div>
  )
}

/** "1 Sep → 1 Oct", or a muted note when neither end is set. */
function dateRange(project: Project): React.ReactNode {
  if (!project.start_date && !project.target_date) {
    return <span className="text-muted-foreground-soft">Not scheduled</span>
  }
  const from = project.start_date ? formatShortDate(project.start_date) : "?"
  const to = project.target_date ? formatShortDate(project.target_date) : "?"
  return `${from} → ${to}`
}

function Fact({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">{label}</dt>
      <dd className={cn("truncate text-foreground/85", mono && "font-mono text-[10px]")}>{value}</dd>
    </div>
  )
}

interface Segment {
  status: string
  value: number
  color: string
  dasharray: string
  dashoffset: number
}

/** Status ring, in the shared issue-status palette so it matches the board. */
function donutSegments(stats: ProjectStats | null): Segment[] {
  const entries = Object.entries(stats?.by_status ?? {}).filter(([, v]) => v > 0)
  const total = entries.reduce((sum, [, v]) => sum + v, 0)
  if (total === 0) return []

  const radius = 16
  const circumference = 2 * Math.PI * radius
  let offset = 0
  return entries.map(([status, value]) => {
    const len = (value / total) * circumference
    const seg: Segment = {
      status,
      value,
      color: ISSUE_STATUS_COLORS[status] || CREW_COLOR_DEFAULT,
      dasharray: `${len} ${circumference - len}`,
      dashoffset: -offset,
    }
    offset += len
    return seg
  })
}
