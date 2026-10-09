"use client"

import { useMemo, useState } from "react"
import { motion, useReducedMotion } from "motion/react"
import Link from "next/link"
import { toast } from "sonner"
import { Streamdown } from "streamdown"
import {
  AlertTriangle,
  BookOpen,
  Check,
  Copy,
  CopyPlus,
  Download,
  ExternalLink,
  FileText,
  Inbox,
  KeyRound,
  ListOrdered,
  MoreHorizontal,
  Pencil,
  ShieldAlert,
  Target,
  Trash2,
  Users,
} from "lucide-react"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { Button } from "@/components/ui/button"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { InlineEmpty } from "@/components/ui/inline-empty"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusPill } from "@/components/ui/status-pill"
import { Switch } from "@/components/ui/switch"
import { useJournalList } from "@/hooks/use-journal-list"
import { useDeleteSkill, useSetSkillAssignment, useSkillDetail } from "@/hooks/use-skills"
import { entityHref } from "@/lib/entity-links"
import { formatRelativeShort } from "@/lib/time"
import { cn } from "@/lib/utils"
import { SkillTile } from "./skill-card"
import { hasStructure, readSkillDoc, skillMarkdown, splitSections, usageByDay, type SkillMeta } from "./skill-doc"
import type { SkillEditorTarget } from "./skill-editor"
import {
  MATURITY_LABEL,
  domainMeta,
  skillName,
  skillTags,
  skillTrust,
  sourceLabel,
  type SkillDetail,
  type SkillRow,
  type SkillsAgent,
  type SkillsCrew,
} from "./skills-model"

// One skill, opened in the content pane beside the explorer (#3033) — the
// Routines detail grammar: identity card with the actions, pill tabs, then
// cards. The Overview reads the skill itself (its SKILL.md sections) in the
// main column and keeps the facts in a rail beside it.

export type SkillTab = "overview" | "agents" | "content" | "activity"
export const SKILL_TABS: { id: SkillTab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "content", label: "SKILL.md" },
  { id: "agents", label: "Agents" },
  { id: "activity", label: "Activity" },
]
export function isSkillTab(v: string | null | undefined): v is SkillTab {
  return v === "overview" || v === "agents" || v === "content" || v === "activity"
}

function Section({
  icon: Icon,
  title,
  meta,
  children,
  className,
}: {
  icon: typeof Target
  title: string
  meta?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  return (
    <section className={cn("overflow-hidden rounded-card border border-border bg-card", className)}>
      <div className="flex flex-wrap items-center gap-2 px-4 py-3">
        <Icon className="h-3.5 w-3.5 text-primary-hover" aria-hidden />
        <h3 className="eyebrow">{title}</h3>
        {meta && <div className="ml-auto text-label text-muted-foreground">{meta}</div>}
      </div>
      {children}
    </section>
  )
}

/** SKILL.md as the CLI's `skill export` writes it: frontmatter + body. */
function exportMeta(s: SkillDetail): SkillMeta {
  return {
    name: s.slug,
    display_name: s.display_name,
    description: s.description,
    version: s.version,
    author: s.author,
    license: s.spdx_license || s.license,
    category: s.category,
    icon: s.icon,
    credential_requirements: s.needs_credentials ?? [],
    tags: skillTags(s),
  }
}

function downloadSkill(s: SkillDetail) {
  const blob = new Blob([skillMarkdown(exportMeta(s), s.content ?? "")], { type: "text/markdown" })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = `${s.slug}.SKILL.md`
  a.click()
  URL.revokeObjectURL(url)
}

/** Inline `code` in a SKILL.md list item, without a markdown renderer per line. */
function Inline({ text }: { text: string }) {
  const parts = text.replace(/\*\*(.+?)\*\*/g, "$1").split(/(`[^`]+`)/g)
  return (
    <>
      {parts.map((p, i) =>
        p.startsWith("`") && p.endsWith("`") && p.length > 2 ? (
          <code key={i} className="rounded bg-foreground/[0.06] px-1 py-px font-mono text-label [overflow-wrap:anywhere]">
            {p.slice(1, -1)}
          </code>
        ) : (
          <span key={i}>{p}</span>
        ),
      )}
    </>
  )
}

function Bullets({ items, icon: Icon, tone }: { items: string[]; icon: typeof Check; tone: string }) {
  return (
    <ul className="flex flex-col gap-2 px-4 pb-4">
      {items.map((it, i) => (
        <li key={i} className="flex min-w-0 items-start gap-2 text-control leading-relaxed">
          <Icon className={cn("mt-[3px] h-3.5 w-3.5 shrink-0", tone)} aria-hidden />
          <span className="min-w-0">
            <Inline text={it} />
          </span>
        </li>
      ))}
    </ul>
  )
}

function RailBlock({ title, action, children }: { title: string; action?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="border-t border-border px-4 py-3 first:border-t-0">
      <div className="mb-2 flex items-center">
        <h4 className="eyebrow text-muted-foreground">{title}</h4>
        {action && <div className="ml-auto text-label">{action}</div>}
      </div>
      {children}
    </div>
  )
}

const ACTIVITY_TYPES = "skill.invoked,skill.assigned,skill.unassigned,skill.imported"

function useSkillJournal(skill: SkillDetail, workspaceId: string) {
  const { entries, loading, error } = useJournalList({ workspaceId, params: { entry_type: ACTIVITY_TYPES }, limit: 200 })
  const mine = useMemo(
    () =>
      entries.filter((e) => {
        const p = (e.payload ?? {}) as Record<string, unknown>
        return p.skill_id === skill.id || p.skill_slug === skill.slug || (e.entry_type === "skill.imported" && e.summary.endsWith(`: ${skill.slug}`))
      }),
    [entries, skill.id, skill.slug],
  )
  return { entries: mine, loading, error }
}

function UsageRail({ skill, workspaceId, onTab }: { skill: SkillDetail; workspaceId: string; onTab: (t: SkillTab) => void }) {
  const { entries } = useSkillJournal(skill, workspaceId)
  const days = useMemo(() => usageByDay(entries), [entries])
  const peak = Math.max(1, ...days.map((d) => d.uses))
  const usage = skill.usage
  return (
    <RailBlock
      title="Usage · 7 days"
      action={
        <button type="button" className="text-primary-hover hover:underline" onClick={() => onTab("activity")}>
          Activity
        </button>
      }
    >
      <div className="flex h-11 items-end gap-1" role="img" aria-label={`Uses per day: ${days.map((d) => `${d.label} ${d.uses}`).join(", ")}`}>
        {days.map((d) => (
          <span
            key={d.date}
            title={`${d.date}: ${d.uses} ${d.uses === 1 ? "use" : "uses"}${d.errors ? `, ${d.errors} failed` : ""}`}
            className={cn(
              "flex-1 rounded-t-[3px] transition-[height] duration-500",
              d.uses === 0 ? "bg-foreground/[0.08]" : d.errors === d.uses ? "bg-destructive/70" : "bg-primary/55",
            )}
            style={{ height: d.uses === 0 ? 2 : `${Math.max(12, (d.uses / peak) * 100)}%` }}
          />
        ))}
      </div>
      <div className="mt-1 flex gap-1 font-mono text-[10px] text-muted-foreground-soft">
        {days.map((d) => (
          <span key={d.date} className="flex-1 text-center">
            {d.label}
          </span>
        ))}
      </div>
      <div className="mt-2 flex flex-wrap gap-x-3 text-label text-muted-foreground tabular-nums">
        <span>
          <b className="font-semibold text-foreground">{usage?.uses_7d ?? 0}</b> uses
        </span>
        <span className={usage?.errors_7d ? "text-destructive" : undefined}>
          <b className={cn("font-semibold", usage?.errors_7d ? "text-destructive" : "text-foreground")}>{usage?.errors_7d ?? 0}</b> errors
        </span>
        <span>{usage?.last_used_at ? `last ${formatRelativeShort(usage.last_used_at)}` : "never used"}</span>
      </div>
    </RailBlock>
  )
}

function OverviewTab({ skill, workspaceId, onTab }: { skill: SkillDetail; workspaceId: string; onTab: (t: SkillTab) => void }) {
  const doc = useMemo(() => readSkillDoc(skill.content), [skill.content])
  const structured = hasStructure(doc)
  const trust = skillTrust(skill)
  const holders = skill.installed_on ?? []
  const lacking = holders.filter((a) => (a.missing_credentials?.length ?? 0) > 0)
  const needs = skill.needs_credentials ?? []
  const headings = doc.sections.filter((s) => s.level > 1)

  return (
    <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_300px]">
      <div className="flex min-w-0 flex-col gap-4">
        <Section icon={Target} title="When agents use it">
          <div className="flex flex-col gap-3 px-4 pb-4">
            <p className="max-w-[68ch] text-body leading-relaxed text-pretty">{skill.description || "This skill has no description, so agents cannot tell when to load it."}</p>
            {doc.triggers.length > 0 && (
              <div className="flex flex-wrap gap-1.5" aria-label="Trigger words">
                {doc.triggers.map((t) => (
                  <span key={t} className="rounded-md border border-primary/25 bg-primary/[0.08] px-2 py-0.5 font-mono text-label text-primary-hover">
                    {t}
                  </span>
                ))}
              </div>
            )}
          </div>
          {doc.when.length > 0 && <Bullets items={doc.when} icon={Check} tone="text-success" />}
        </Section>

        {doc.steps.length > 0 && (
          <Section icon={ListOrdered} title="How it works" meta={`${doc.steps.length} ${doc.steps.length === 1 ? "step" : "steps"}`}>
            <ol className="px-4 pb-2">
              {doc.steps.map((step, i) => (
                <motion.li
                  key={i}
                  initial={{ opacity: 0, y: 4 }}
                  animate={{ opacity: 1, y: 0 }}
                  transition={{ delay: Math.min(i, 12) * 0.03, duration: 0.2 }}
                  className="grid grid-cols-[22px_minmax(0,1fr)] gap-2.5 border-t border-border/60 py-2.5 text-control leading-relaxed first:border-t-0"
                >
                  <span className="grid h-[22px] w-[22px] place-items-center rounded-md bg-primary/10 font-mono text-micro font-semibold text-primary-hover">{i + 1}</span>
                  <span className="min-w-0">
                    <Inline text={step} />
                  </span>
                </motion.li>
              ))}
            </ol>
          </Section>
        )}

        {(doc.output.length > 0 || doc.guardrails.length > 0) && (
          <div className="grid gap-4 md:grid-cols-2">
            {doc.output.length > 0 && (
              <Section icon={Inbox} title="What it hands back">
                <Bullets items={doc.output} icon={Check} tone="text-muted-foreground" />
              </Section>
            )}
            {doc.guardrails.length > 0 && (
              <Section icon={ShieldAlert} title="Guardrails">
                <Bullets items={doc.guardrails} icon={AlertTriangle} tone="text-warn" />
              </Section>
            )}
          </div>
        )}

        {!structured && headings.length > 0 && (
          <Section icon={BookOpen} title="What's inside">
            <ul className="flex flex-col px-2 pb-2">
              {headings.map((h) => (
                <li key={h.id}>
                  <button
                    type="button"
                    onClick={() => onTab("content")}
                    className="row-hover flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-control"
                    style={{ paddingLeft: 8 + (h.level - 2) * 14 }}
                  >
                    <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" />
                    {h.heading}
                  </button>
                </li>
              ))}
            </ul>
          </Section>
        )}

        <p className="flex flex-wrap items-center gap-1.5 px-1 text-micro text-muted-foreground-soft">
          <FileText className="h-3 w-3" />
          {structured
            ? `Read from its SKILL.md${doc.other.length ? `; ${doc.other.length} more ${doc.other.length === 1 ? "section is" : "sections are"} in the full file` : ""}.`
            : "This SKILL.md has no When to Activate, Instructions, Output Format or Guardrails sections to lay out."}
          <button type="button" className="text-primary-hover hover:underline" onClick={() => onTab("content")}>
            Open SKILL.md →
          </button>
        </p>
      </div>

      <aside className="min-w-0 overflow-hidden rounded-card border border-border bg-card" aria-label="About this skill">
        <RailBlock
          title={`Agents · ${holders.length}`}
          action={
            <button type="button" className="text-primary-hover hover:underline" onClick={() => onTab("agents")}>
              Manage
            </button>
          }
        >
          {holders.length === 0 ? (
            <p className="text-label text-muted-foreground">No agent has it yet.</p>
          ) : (
            <ul className="flex flex-col gap-1.5">
              {holders.slice(0, 6).map((a) => (
                <li key={a.agent_id} className="flex min-w-0 items-center gap-2 text-control">
                  <AgentAvatar
                    seed={a.avatar_seed ?? a.agent_slug}
                    style={a.avatar_style ?? a.crew_avatar_style}
                    agentId={a.agent_id}
                    avatarUrl={a.avatar_url}
                    alt=""
                    width={20}
                    height={20}
                    className="h-5 w-5 shrink-0 rounded-full bg-foreground/[0.04]"
                  />
                  <Link href={entityHref({ kind: "agent", slug: a.agent_slug })} className="truncate hover:underline">
                    {a.agent_name}
                  </Link>
                  {(a.missing_credentials?.length ?? 0) > 0 && (
                    <span title={`Missing ${a.missing_credentials!.join(", ")}`}>
                      <KeyRound className="h-3 w-3 shrink-0 text-warn" aria-label="Missing a credential" />
                    </span>
                  )}
                  <span className="ml-auto truncate text-micro text-muted-foreground-soft">{a.crew_name}</span>
                </li>
              ))}
              {holders.length > 6 && <li className="text-label text-muted-foreground">and {holders.length - 6} more</li>}
            </ul>
          )}
        </RailBlock>

        <UsageRail skill={skill} workspaceId={workspaceId} onTab={onTab} />

        <RailBlock title="Needs">
          {needs.length === 0 ? (
            <p className="text-label text-muted-foreground">Nothing. It runs as instructions only.</p>
          ) : (
            <div className="flex flex-col gap-1.5">
              <div className="flex flex-wrap gap-1">
                {needs.map((n) => (
                  <code key={n} className="rounded bg-foreground/[0.06] px-1.5 py-0.5 font-mono text-micro">
                    {n}
                  </code>
                ))}
              </div>
              <p className={cn("text-label", lacking.length ? "text-warn" : "text-muted-foreground")}>
                {lacking.length > 0
                  ? `${lacking.length} ${lacking.length === 1 ? "agent is" : "agents are"} missing it.`
                  : holders.length > 0
                    ? "Every agent with the skill has it."
                    : "Give agents the credential with the skill."}
                {lacking.length > 0 && (
                  <>
                    {" "}
                    <Link href={entityHref({ kind: "credentials" })} className="text-primary-hover hover:underline">
                      Credentials
                    </Link>
                  </>
                )}
              </p>
            </div>
          )}
        </RailBlock>

        <RailBlock title="About">
          <dl className="grid grid-cols-[76px_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-label">
            <dt className="text-muted-foreground-soft">Trust</dt>
            <dd className={cn("[overflow-wrap:anywhere]", trust.level === "flagged" && "text-destructive", trust.level === "unscanned" && "text-warn")}>
              {trust.level === "flagged"
                ? `Flagged: ${skill.description_quality || "suspicious instructions"}`
                : trust.level === "unscanned"
                  ? "Never scanned"
                  : trust.level === "verified"
                    ? "Scan clean, curator verified"
                    : "Scan clean, not verified"}
            </dd>
            <dt className="text-muted-foreground-soft">Source</dt>
            <dd className="[overflow-wrap:anywhere]">
              {sourceLabel(skill.source)}
              {skill.vendor ? ` from ${skill.vendor}` : ""}
              {skill.homepage && (
                <>
                  {" · "}
                  <a href={skill.homepage} target="_blank" rel="noreferrer" className="text-primary-hover hover:underline">
                    link <ExternalLink className="inline h-3 w-3" />
                  </a>
                </>
              )}
            </dd>
            <dt className="text-muted-foreground-soft">Domain</dt>
            <dd>{domainMeta(skill.category).label}</dd>
            {skill.version && (
              <>
                <dt className="text-muted-foreground-soft">Version</dt>
                <dd className="font-mono">{skill.version}</dd>
              </>
            )}
            <dt className="text-muted-foreground-soft">License</dt>
            <dd>{skill.spdx_license || skill.license || "Not stated"}</dd>
            {skill.updated_at && (
              <>
                <dt className="text-muted-foreground-soft">Updated</dt>
                <dd>{formatRelativeShort(skill.updated_at)}</dd>
              </>
            )}
          </dl>
        </RailBlock>
      </aside>
    </div>
  )
}

function ContentTab({ skill, canEdit, onEdit }: { skill: SkillDetail; canEdit: boolean; onEdit: () => void }) {
  const [source, setSource] = useState(false)
  const [copied, setCopied] = useState(false)
  const body = skill.content ?? ""
  const full = skillMarkdown(exportMeta(skill), body)
  const headings = useMemo(() => splitSections(body).filter((s) => s.level > 1), [body])
  const meta = exportMeta(skill)
  const props: [string, React.ReactNode][] = [
    ["name", meta.name],
    ["description", meta.description || "—"],
    ["category", meta.category],
    ["icon", meta.icon ? <span className="inline-flex items-center gap-1.5"><SkillTile category={skill.category} icon={skill.icon} size="sm" className="h-5 w-5 rounded [&>svg]:h-3 [&>svg]:w-3" />{meta.icon}</span> : "— (domain icon)"],
    ["version", meta.version || "—"],
    ...(meta.credential_requirements?.length ? [["credentials", meta.credential_requirements.join(", ")] as [string, React.ReactNode]] : []),
    ...(meta.license ? [["license", meta.license] as [string, React.ReactNode]] : []),
  ]
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(full)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      toast.error("The browser blocked the clipboard. Download the file instead.")
    }
  }
  const jump = (id: string) => document.getElementById(`skill-md-${id}`)?.scrollIntoView({ behavior: "smooth", block: "start" })

  return (
    <div className={cn("grid items-start gap-4", headings.length > 1 && "lg:grid-cols-[200px_minmax(0,1fr)]")}>
      {headings.length > 1 && (
        <nav aria-label="Sections" className="flex gap-1 overflow-x-auto rounded-card border border-border bg-card p-1.5 lg:sticky lg:top-4 lg:flex-col">
          {headings.map((h) => (
            <button
              key={h.id}
              type="button"
              onClick={() => {
                setSource(false)
                setTimeout(() => jump(h.id), 0)
              }}
              className="row-hover shrink-0 truncate rounded-md px-2 py-1.5 text-left text-label text-muted-foreground hover:text-foreground"
              style={{ paddingLeft: 8 + Math.max(0, h.level - 2) * 12 }}
            >
              {h.heading}
            </button>
          ))}
        </nav>
      )}
      <section className="min-w-0 overflow-hidden rounded-card border border-border bg-card">
        <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
          <div role="group" aria-label="Show as" className="inline-flex rounded-md bg-foreground/[0.05] p-0.5">
            {(["Rendered", "Source"] as const).map((l) => {
              const on = (l === "Source") === source
              return (
                <button
                  key={l}
                  type="button"
                  aria-pressed={on}
                  onClick={() => setSource(l === "Source")}
                  className={cn("rounded px-2.5 py-1 text-label transition-colors", on ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground")}
                >
                  {l}
                </button>
              )
            })}
          </div>
          <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{full.split("\n").length} lines</span>
          <span className="flex-1" />
          <Button variant="ghost" size="sm" onClick={copy}>
            {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
            {copied ? "Copied" : "Copy"}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => downloadSkill(skill)}>
            <Download className="h-3.5 w-3.5" />
            Download
          </Button>
          {canEdit && (
            <Button variant="outline" size="sm" onClick={onEdit}>
              <Pencil className="h-3.5 w-3.5" />
              Edit
            </Button>
          )}
        </div>
        {source ? (
          <pre className="overflow-x-auto whitespace-pre-wrap break-words px-4 py-3 font-mono text-label leading-relaxed text-foreground/85">{full}</pre>
        ) : (
          <>
            <dl className="mx-4 mt-3 overflow-hidden rounded-lg border border-border text-label" aria-label="Frontmatter">
              {props.map(([k, v]) => (
                <div key={k} className="grid grid-cols-[110px_minmax(0,1fr)] border-t border-border first:border-t-0">
                  <dt className="bg-foreground/[0.03] px-2.5 py-1.5 font-mono text-muted-foreground-soft">{k}</dt>
                  <dd className="px-2.5 py-1.5 [overflow-wrap:anywhere]">{v}</dd>
                </div>
              ))}
            </dl>
            <div className="max-w-[80ch] px-4 py-4 text-control leading-relaxed [&_h1]:mb-2 [&_h1]:text-heading [&_h1]:font-semibold [&_h2]:mb-2 [&_h2]:mt-5 [&_h2]:text-body [&_h2]:font-semibold [&_h3]:mb-1.5 [&_h3]:mt-4 [&_h3]:text-control [&_h3]:font-semibold [&_h4]:mt-3 [&_h4]:text-control [&_h4]:font-medium [&_pre]:overflow-x-auto">
              {body ? <SkillMarkdown body={body} /> : <p className="text-muted-foreground">This skill has no instructions.</p>}
            </div>
          </>
        )}
      </section>
    </div>
  )
}

/** The body rendered section by section, so the outline can scroll to each heading. */
function SkillMarkdown({ body }: { body: string }) {
  const sections = useMemo(() => splitSections(body), [body])
  const lead = body.split(/\n(?=#{1,4}\s)/)[0]
  const hasLead = sections.length === 0 || !/^#{1,4}\s/.test(body.trimStart())
  return (
    <>
      {hasLead && <Streamdown>{sections.length === 0 ? body : lead}</Streamdown>}
      {sections.map((s) => (
        <div key={s.id} id={`skill-md-${s.id}`} className="scroll-mt-4">
          <Streamdown>{`${"#".repeat(s.level)} ${s.heading}\n\n${s.body}`}</Streamdown>
        </div>
      ))}
    </>
  )
}

function AgentsTab({ skill, agents, crews, workspaceId }: { skill: SkillDetail; agents: SkillsAgent[]; crews: SkillsCrew[]; workspaceId: string }) {
  const assign = useSetSkillAssignment(workspaceId)
  const [pending, setPending] = useState<Record<string, boolean>>({})
  const holders = useMemo(() => new Map((skill.installed_on ?? []).map((a) => [a.agent_id, a])), [skill.installed_on])
  const needs = skill.needs_credentials ?? []
  const flagged = skillTrust(skill).level === "flagged"

  // A switch applies at once (README › Saving); it shows the asked-for state
  // while the write is in flight, and a failure flips it back with a toast.
  const setOne = async (agent: SkillsAgent, on: boolean) => {
    setPending((p) => ({ ...p, [agent.id]: on }))
    try {
      await assign.mutateAsync({ agentId: agent.id, skillId: skill.id, on })
      toast.success(on ? `${agent.name} now has ${skillName(skill)}` : `${agent.name} no longer has ${skillName(skill)}`)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Could not change the assignment")
    } finally {
      setPending((p) => {
        const next = { ...p }
        delete next[agent.id]
        return next
      })
    }
  }
  const setCrew = async (members: SkillsAgent[], on: boolean) => {
    const todo = members.filter((a) => holders.has(a.id) !== on)
    for (const a of todo) setPending((p) => ({ ...p, [a.id]: on }))
    const results = await Promise.allSettled(todo.map((a) => assign.mutateAsync({ agentId: a.id, skillId: skill.id, on })))
    setPending({})
    const failed = results.filter((r) => r.status === "rejected").length
    if (failed) toast.error(`${failed} of ${todo.length} changes failed`)
    else toast.success(on ? `${skillName(skill)} given to ${todo.length} agents` : `${skillName(skill)} removed from ${todo.length} agents`)
  }

  const groups = [
    ...crews.map((c) => ({ crew: c as SkillsCrew | null, members: agents.filter((a) => a.crew_id === c.id) })),
    { crew: null, members: agents.filter((a) => !a.crew_id || !crews.some((c) => c.id === a.crew_id)) },
  ].filter((g) => g.members.length > 0)

  return (
    <Section icon={Users} title="Agents with this skill" meta="A switch applies at once; the agent loads the skill on its next run.">
      {flagged && (
        <div className="mx-4 mb-3 flex items-start gap-2 rounded-lg border border-destructive/30 bg-chip-danger-bg px-3 py-2 text-label text-chip-danger-fg">
          <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
          The import scan flagged this skill. Read its SKILL.md before giving it to another agent.
        </div>
      )}
      {groups.length === 0 && (
        <div className="px-4 pb-4">
          <InlineEmpty icon={Users} text="This workspace has no agents yet. Create a crew to give skills to its agents." />
        </div>
      )}
      {groups.map(({ crew, members }) => {
        const on = members.filter((a) => (pending[a.id] ?? holders.has(a.id))).length
        const all = on === members.length
        return (
          <div key={crew?.id ?? "none"} className="border-t border-border">
            <div className="flex items-center gap-2 px-4 py-2.5">
              {crew ? (
                <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" className="h-[18px] w-[18px] rounded-md [&>svg]:h-3 [&>svg]:w-3" />
              ) : (
                <Users className="h-3.5 w-3.5 text-muted-foreground" />
              )}
              <span className="text-control font-semibold">{crew?.name ?? "No crew"}</span>
              <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">
                {on}/{members.length}
              </span>
              <span className="flex-1" />
              <button type="button" className="text-label text-primary-hover hover:underline" onClick={() => setCrew(members, !all)}>
                {all ? (crew ? "Remove from crew" : "Remove from all") : crew ? "Give to whole crew" : "Give to all"}
              </button>
            </div>
            {members.map((a) => {
              const held = pending[a.id] ?? holders.has(a.id)
              const missing = holders.get(a.id)?.missing_credentials ?? []
              return (
                <div key={a.id} className="row-hover flex min-h-11 items-center gap-2.5 py-1.5 pl-10 pr-4">
                  <AgentAvatar
                    seed={a.avatar_seed ?? a.slug}
                    style={a.avatar_style}
                    agentId={a.id}
                    avatarUrl={a.avatar_url}
                    alt=""
                    width={20}
                    height={20}
                    className="h-5 w-5 shrink-0 rounded-full bg-foreground/[0.04]"
                  />
                  <Link href={entityHref({ kind: "agent", slug: a.slug })} className="text-control hover:underline">
                    {a.name}
                  </Link>
                  {a.role_title && <span className="hidden truncate text-micro text-muted-foreground-soft sm:inline">{a.role_title}</span>}
                  <span className="flex-1" />
                  {held && missing.length > 0 && (
                    <Link
                      href={entityHref({ kind: "credentials" })}
                      className="inline-flex items-center gap-1 text-micro text-warn hover:underline"
                      title="Give this agent the credential on the Credentials page"
                    >
                      <KeyRound className="h-3 w-3" />
                      no {missing.join(", ")}
                    </Link>
                  )}
                  {!held && needs.length > 0 && <span className="hidden text-micro text-muted-foreground-soft md:inline">needs {needs.join(", ")}</span>}
                  <Switch
                    checked={held}
                    disabled={a.id in pending}
                    onCheckedChange={(v) => setOne(a, v)}
                    aria-label={`${a.name} has ${skillName(skill)}`}
                  />
                </div>
              )
            })}
          </div>
        )
      })}
    </Section>
  )
}

const ACTIVITY_WORDS: Record<string, string> = {
  "skill.invoked": "used it",
  "skill.assigned": "was given it",
  "skill.unassigned": "lost it",
  "skill.imported": "Imported",
}

function ActivityTab({ skill, workspaceId, agents }: { skill: SkillDetail; workspaceId: string; agents: SkillsAgent[] }) {
  const { entries: mine, loading, error } = useSkillJournal(skill, workspaceId)
  const agentName = (id?: string) => agents.find((a) => a.id === id)?.name ?? "An agent"
  return (
    <Section
      icon={BookOpen}
      title="Journal"
      meta={
        <Link href={entityHref({ kind: "journal" })} className="text-primary-hover hover:underline">
          Open Journal ↗
        </Link>
      }
    >
      {loading ? (
        <div className="flex flex-col gap-2 px-4 pb-4">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-8" />
          ))}
        </div>
      ) : error ? (
        <div className="px-4 pb-4">
          <InlineEmpty icon={AlertTriangle} text="The journal could not be read." />
        </div>
      ) : mine.length === 0 ? (
        <div className="px-4 pb-4">
          <InlineEmpty icon={BookOpen} text="Nothing recorded yet. Assigning, removing and every use by an agent appear here." />
        </div>
      ) : (
        <ul>
          {mine.map((e) => {
            const p = (e.payload ?? {}) as Record<string, unknown>
            const who = e.entry_type === "skill.imported" ? "" : `${agentName((p.agent_id as string) ?? e.agent_id)} `
            const failed = e.entry_type === "skill.invoked" && Number(p.exit_code ?? 0) !== 0
            return (
              <li key={e.id} className="grid grid-cols-[90px_1fr_auto] items-center gap-3 border-t border-border/60 px-4 py-2 text-control">
                <span className="font-mono text-micro text-muted-foreground-soft">{formatRelativeShort(e.ts)}</span>
                <span className="min-w-0 truncate">
                  {who}
                  {ACTIVITY_WORDS[e.entry_type] ?? e.entry_type}
                </span>
                <StatusPill tone={failed ? "danger" : e.entry_type === "skill.invoked" ? "blue" : "muted"} label={failed ? "error" : e.entry_type} />
              </li>
            )
          })}
        </ul>
      )}
    </Section>
  )
}

export interface SkillPageProps {
  workspaceId: string
  skillId: string
  /** The row from the list, shown while the detail loads. */
  row?: SkillRow
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  tab: SkillTab
  onTab: (t: SkillTab) => void
  onAssign: () => void
  onDeleted: () => void
  onEditor: (t: SkillEditorTarget) => void
}

export function SkillPage({ workspaceId, skillId, row, agents, crews, tab, onTab, onAssign, onDeleted, onEditor }: SkillPageProps) {
  const reduce = useReducedMotion()
  const { data, isLoading, isError, refetch } = useSkillDetail(workspaceId, skillId)
  const del = useDeleteSkill(workspaceId)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const skill = data ?? (row as SkillDetail | undefined)

  if (!skill) {
    return (
      <div className="flex flex-col gap-3 p-4 md:p-5">
        {isError ? (
          <InlineEmpty
            icon={AlertTriangle}
            text="This skill could not be loaded. It may have been deleted."
            action={
              <Button variant="outline" size="sm" onClick={() => refetch()}>
                Retry
              </Button>
            }
          />
        ) : (
          <>
            <Skeleton className="h-28 rounded-card" />
            <Skeleton className="h-8 w-80" />
            <Skeleton className="h-48 rounded-card" />
          </>
        )}
      </div>
    )
  }

  const trust = skillTrust(skill)
  const holders = skill.installed_on?.length ?? 0
  const builtIn = skill.source === "BUNDLED"
  const maturity = (skill.maturity ?? "COMMUNITY").toUpperCase()
  // Edits go through the importer, which refuses built-ins; the detail (with
  // its content) has to be loaded before there is anything to edit.
  const canEdit = !builtIn && !!data
  const DomainIcon = domainMeta(skill.category).icon
  const BUILT_IN_WHY = "Built-in skills ship with Crewship and reinstall on every start."

  return (
    <div className="flex flex-col gap-4 p-3 pb-24 sm:p-4 md:p-5">
      <motion.section
        initial={reduce ? false : { opacity: 0, y: 6 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.25, ease: [0.22, 1, 0.36, 1] }}
        className="group flex flex-wrap items-start gap-4 rounded-card border border-border/60 bg-card p-4"
      >
        {canEdit ? (
          <button
            type="button"
            aria-label="Change icon or domain"
            title="Change icon or domain"
            onClick={() => data && onEditor({ kind: "edit", skill: data, panel: "icon" })}
            className="rounded-xl"
          >
            <SkillTile category={skill.category} icon={skill.icon} size="lg" />
          </button>
        ) : (
          <SkillTile category={skill.category} icon={skill.icon} size="lg" />
        )}
        <div className="min-w-0 flex-1">
          <h2 className="text-heading font-semibold tracking-tight">{skillName(skill)}</h2>
          <p className="mt-0.5 truncate font-mono text-micro text-muted-foreground-soft">
            {skill.vendor ? `${skill.vendor}/` : ""}
            {skill.slug}
          </p>
          <div className="mt-2 flex flex-wrap items-center gap-1.5 text-micro text-muted-foreground">
            <StatusPill tone={trust.tone} label={trust.label} />
            <StatusPill tone={maturity === "OFFICIAL" ? "blue" : "muted"} label={MATURITY_LABEL[maturity] ?? maturity} />
            {skill.lifecycle_state && skill.lifecycle_state !== "active" && <StatusPill tone="warn" label={skill.lifecycle_state} />}
            <span className="inline-flex items-center gap-1 rounded-full border border-border px-2 py-px">
              <DomainIcon className="h-3 w-3" aria-hidden />
              {domainMeta(skill.category).label}
            </span>
            <span className="inline-flex items-center rounded-full border border-border px-2 py-px">{sourceLabel(skill.source)}</span>
            <span aria-hidden>·</span>
            <span>
              on <b className="tabular-nums text-foreground">{holders}</b> {holders === 1 ? "agent" : "agents"}
            </span>
            <span aria-hidden>·</span>
            <span className="tabular-nums">
              {skill.usage?.uses_7d ?? 0} {skill.usage?.uses_7d === 1 ? "use" : "uses"} this week
            </span>
          </div>
          {trust.level === "flagged" && (
            <div className="mt-3 flex items-start gap-2 rounded-lg border border-destructive/30 bg-chip-danger-bg px-3 py-2 text-label text-chip-danger-fg">
              <AlertTriangle className="mt-px h-3.5 w-3.5 shrink-0" />
              {skill.description_quality || "The import scan flagged its instructions"}. Read the SKILL.md before keeping it on agents.
            </div>
          )}
        </div>
        <div className="flex w-full flex-wrap items-center gap-1.5 sm:w-auto">
          <Button size="sm" onClick={onAssign} className="flex-1 sm:flex-none">
            <Users className="h-3.5 w-3.5" />
            Assign to agents
          </Button>
          {canEdit && (
            <Button variant="outline" size="sm" onClick={() => data && onEditor({ kind: "edit", skill: data })}>
              <Pencil className="h-3.5 w-3.5" />
              Edit
            </Button>
          )}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label="More actions"
                className="rounded-lg border border-border/60 p-1.5 text-muted-foreground transition-colors hover:text-foreground"
              >
                <MoreHorizontal className="h-3.5 w-3.5" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-60">
              <DropdownMenuItem disabled={!data} onSelect={() => data && onEditor({ kind: "duplicate", skill: data })}>
                <CopyPlus className="h-3.5 w-3.5" />
                Duplicate as new skill
              </DropdownMenuItem>
              <DropdownMenuItem disabled={!data} onSelect={() => data && downloadSkill(data)}>
                <Download className="h-3.5 w-3.5" />
                Download SKILL.md
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" disabled={builtIn} onSelect={() => setConfirmDelete(true)}>
                <Trash2 className="h-3.5 w-3.5" />
                Delete
              </DropdownMenuItem>
              {builtIn && <p className="px-2 pb-1.5 text-micro text-muted-foreground-soft">{BUILT_IN_WHY} Duplicate one to change it.</p>}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </motion.section>

      <nav role="tablist" aria-label="Skill sections" className="flex gap-2 overflow-x-auto border-b border-border pb-2">
        {SKILL_TABS.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            onClick={() => onTab(t.id)}
            className={cn(
              "shrink-0 rounded-full px-3 py-1.5 text-label transition-colors",
              tab === t.id ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:bg-muted/60",
            )}
          >
            {t.label}
            {t.id === "agents" && <span className="ml-1 tabular-nums text-muted-foreground">· {holders}</span>}
          </button>
        ))}
      </nav>

      <motion.div
        key={tab}
        initial={reduce ? false : { opacity: 0, y: 4 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.18 }}
        className="flex flex-col gap-4"
      >
        {tab === "overview" &&
          (isLoading && !data ? <Skeleton className="h-64 rounded-card" /> : <OverviewTab skill={skill} workspaceId={workspaceId} onTab={onTab} />)}
        {tab === "agents" && <AgentsTab skill={skill} agents={agents} crews={crews} workspaceId={workspaceId} />}
        {tab === "content" &&
          (isLoading && !data ? (
            <Skeleton className="h-64 rounded-card" />
          ) : (
            <ContentTab skill={skill} canEdit={canEdit} onEdit={() => data && onEditor({ kind: "edit", skill: data })} />
          ))}
        {tab === "activity" && <ActivityTab skill={skill} workspaceId={workspaceId} agents={agents} />}
      </motion.div>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {skillName(skill)}?</AlertDialogTitle>
            <AlertDialogDescription>
              It is removed from the catalog and from {holders} {holders === 1 ? "agent" : "agents"}; they stop loading it on their next run. To get it back,
              import its SKILL.md again — download it first if you have no copy.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              onClick={async () => {
                try {
                  await del.mutateAsync(skill.id)
                  toast.success(`${skillName(skill)} deleted`)
                  onDeleted()
                } catch (e) {
                  toast.error(e instanceof Error ? e.message : "Could not delete the skill")
                }
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
