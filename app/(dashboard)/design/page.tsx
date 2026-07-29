"use client"

import { useState } from "react"
import {
  Activity, Bell, Bot, Brain, ChevronDown, CircleDot, Clock, FolderTree, KeyRound,
  MessageSquare, MoreHorizontal, Play, Sparkles, Workflow, Wrench,
} from "lucide-react"

import { DetailCard, EntityChip, Pill } from "@/components/ui/detail"
import { cn } from "@/lib/utils"

// =============================================================================
// /design — a live wireframe bench, not a product screen.
//
// It exists because layout arguments cannot be settled in prose. Every variant
// below renders with the real kit (components/ui/detail) and the real type
// roles, so whatever is picked here is already implementable — no translation
// step between the mock and the screen.
//
// Deliberately unlinked from the sidebar: reachable by typing /design, invisible
// to anyone who is not looking for it.
// =============================================================================

const AGENT = {
  name: "Casey",
  slug: "casey",
  role: "Test & Review Engineer",
  crew: "Quality",
  model: "claude-haiku-4-5",
  status: "idle",
}

const REACH = [
  { id: "skills", icon: Sparkles, label: "Skills", value: "3 / 3", group: "Can do" },
  { id: "tools", icon: Wrench, label: "Tools", value: "0", group: "Can do" },
  { id: "memory", icon: Brain, label: "Memory", value: "on", group: "Can do" },
  { id: "channels", icon: Bell, label: "Channels", value: "0", group: "Reports to" },
  { id: "sessions", icon: MessageSquare, label: "Sessions", value: "79", group: "History" },
  { id: "workspace", icon: FolderTree, label: "Workspace", value: "files", group: "History" },
  { id: "activity", icon: Activity, label: "Activity", value: "all", group: "History" },
]

const MAIN_TABS = ["Overview", "Configuration"]

/* ── header variants ─────────────────────────────────────────────────────── */

function Avatar({ size = "h-11 w-11" }: { size?: string }) {
  return (
    <span className={cn("grid shrink-0 place-items-center rounded-[10px] bg-destructive/80 text-white", size)}>
      <Bot className="h-5 w-5" />
    </span>
  )
}

function IdentityLine() {
  return (
    <div className="type-meta flex flex-wrap items-center gap-x-2 gap-y-1 font-mono text-muted-foreground">
      <span>{AGENT.slug}</span>
      <span className="opacity-40">·</span>
      <span>{AGENT.role}</span>
      <span className="opacity-40">·</span>
      <span className="text-primary">{AGENT.crew}</span>
      <span className="opacity-40">·</span>
      <span>{AGENT.model}</span>
    </div>
  )
}

function Actions({ compact = false }: { compact?: boolean }) {
  const btn = "type-row inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 font-medium transition-colors"
  return (
    <>
      <button className={cn(btn, "bg-primary text-primary-foreground hover:bg-primary-hover")}>
        <MessageSquare className="h-3.5 w-3.5" /> Chat
      </button>
      <button className={cn(btn, "border border-border bg-surface-raised hover:bg-white/[.09]")}>
        <FolderTree className="h-3.5 w-3.5" /> Files
      </button>
      {!compact && (
        <button className={cn(btn, "border border-border bg-surface-raised hover:bg-white/[.09]")}>
          <Play className="h-3.5 w-3.5" /> Run
        </button>
      )}
      <button className="rounded-lg border border-border p-2 text-muted-foreground hover:bg-white/[.05]">
        <MoreHorizontal className="h-3.5 w-3.5" />
      </button>
    </>
  )
}

function HeaderA() {
  return (
    <header className="border-b border-border pb-4">
      <div className="flex items-start gap-3.5">
        <Avatar />
        <div className="min-w-0 flex-1">
          <div className="mb-1.5 flex flex-wrap items-center gap-2">
            <Pill><span className="h-1.5 w-1.5 rounded-full bg-current" /> {AGENT.status}</Pill>
            <Pill tone="purple">Lead</Pill>
          </div>
          <h1 className="type-title">{AGENT.name}</h1>
          <div className="mt-1"><IdentityLine /></div>
        </div>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        <Actions />
      </div>
    </header>
  )
}

function HeaderB() {
  return (
    <header className="flex flex-wrap items-center gap-3 border-b border-border pb-3">
      <Avatar size="h-9 w-9" />
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="type-title leading-none">{AGENT.name}</h1>
          <Pill><span className="h-1.5 w-1.5 rounded-full bg-current" /> {AGENT.status}</Pill>
          <Pill tone="purple">Lead</Pill>
        </div>
        <div className="mt-1"><IdentityLine /></div>
      </div>
      <div className="ml-auto flex items-center gap-2"><Actions compact /></div>
    </header>
  )
}

function HeaderC() {
  return (
    <header className="border-b border-border pb-3">
      <div className="flex items-center gap-3">
        <Avatar size="h-8 w-8" />
        <h1 className="type-title leading-none">{AGENT.name}</h1>
        <span className="type-meta font-mono text-muted-foreground">{AGENT.slug}</span>
        <div className="ml-auto flex items-center gap-2"><Actions compact /></div>
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Pill><span className="h-1.5 w-1.5 rounded-full bg-current" /> {AGENT.status}</Pill>
        <Pill tone="purple">Lead</Pill>
        <span className="type-meta font-mono text-muted-foreground">
          {AGENT.role} · <span className="text-primary">{AGENT.crew}</span> · {AGENT.model}
        </span>
      </div>
    </header>
  )
}

function HeaderD() {
  return (
    <header className="flex flex-wrap items-center gap-2.5 border-b border-border pb-2.5">
      <Avatar size="h-7 w-7" />
      <span className="type-row font-semibold">{AGENT.name}</span>
      <span className="type-meta font-mono text-muted-foreground">
        {AGENT.slug} · {AGENT.role} · <span className="text-primary">{AGENT.crew}</span>
      </span>
      <Pill><span className="h-1.5 w-1.5 rounded-full bg-current" /> {AGENT.status}</Pill>
      <div className="ml-auto flex items-center gap-2"><Actions compact /></div>
    </header>
  )
}

const HEADERS = [
  { id: "A", label: "A · stacked", note: "pills, name, identity, actions on their own row (today)", render: HeaderA },
  { id: "B", label: "B · one row", note: "name and pills inline, actions right — no action row", render: HeaderB },
  { id: "C", label: "C · title bar", note: "name + actions on top, state and identity below", render: HeaderC },
  { id: "D", label: "D · strip", note: "everything on one 28px line, densest", render: HeaderD },
]

/* ── reach variants ──────────────────────────────────────────────────────── */

function TabBar({ children }: { children?: React.ReactNode }) {
  return (
    <div className="flex items-center gap-4 border-b border-border">
      {MAIN_TABS.map((t, i) => (
        <button
          key={t}
          className={cn(
            "type-row border-b-2 px-1 pb-2 transition-colors",
            i === 0 ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
          )}
        >
          {t}
        </button>
      ))}
      {children}
    </div>
  )
}

function ReachSecondRow() {
  return (
    <>
      <TabBar />
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 py-2">
        {REACH.map((r) => (
          <button key={r.id} className="type-row inline-flex items-center gap-1.5 text-muted-foreground transition-colors hover:text-foreground">
            <r.icon className="h-3.5 w-3.5" />
            {r.label}
            <span className="type-meta font-mono opacity-60">{r.value}</span>
          </button>
        ))}
      </div>
    </>
  )
}

function ReachInMainMenu() {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-border">
      {MAIN_TABS.map((t, i) => (
        <button
          key={t}
          className={cn(
            "type-row border-b-2 px-1 pb-2",
            i === 0 ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
          )}
        >
          {t}
        </button>
      ))}
      <span className="mx-1 h-4 w-px self-center bg-border" />
      {REACH.map((r) => (
        <button key={r.id} className="type-row inline-flex items-center gap-1.5 border-b-2 border-transparent px-1 pb-2 text-muted-foreground hover:text-foreground">
          {r.label}
          <span className="type-meta font-mono opacity-60">{r.value}</span>
        </button>
      ))}
    </div>
  )
}

function ReachDropdown() {
  const [open, setOpen] = useState(false)
  return (
    <TabBar>
      <div className="relative ml-auto pb-1.5">
        <button
          onClick={() => setOpen((o) => !o)}
          className="type-row inline-flex items-center gap-1.5 rounded-lg border border-border bg-surface-raised px-2.5 py-1 text-muted-foreground hover:text-foreground"
        >
          Reach
          <ChevronDown className={cn("h-3 w-3 transition-transform", open && "rotate-180")} />
        </button>
        {open && (
          <div className="absolute right-0 z-20 mt-1 w-56 rounded-lg border border-border bg-surface-raised p-1 shadow-xl">
            {REACH.map((r) => (
              <button key={r.id} className="type-row flex w-full items-center gap-2 rounded-md px-2 py-1.5 hover:bg-white/[.06]">
                <r.icon className="h-3.5 w-3.5 text-muted-foreground-soft" />
                {r.label}
                <span className="type-meta ml-auto font-mono text-muted-foreground-soft">{r.value}</span>
              </button>
            ))}
          </div>
        )}
      </div>
    </TabBar>
  )
}

function ReachCard() {
  const groups = REACH.reduce<Record<string, typeof REACH>>((acc, r) => {
    ;(acc[r.group] ??= []).push(r)
    return acc
  }, {})
  return (
    <>
      <TabBar />
      <DetailCard title="What it touches" subtitle="blast radius" bare className="mt-3">
        {Object.entries(groups).map(([g, items]) => (
          <div key={g} className="flex flex-wrap items-center gap-2 border-b border-hairline px-4 py-2.5 last:border-b-0">
            <span className="type-meta w-24 shrink-0 uppercase tracking-wide text-muted-foreground-soft">{g}</span>
            {items.map((r) => (
              <EntityChip key={r.id} icon={r.icon} label={r.label} note={r.value} />
            ))}
          </div>
        ))}
      </DetailCard>
    </>
  )
}

function ReachBubbles() {
  return (
    <>
      <TabBar />
      <div className="flex flex-wrap items-center gap-1.5 py-2">
        {REACH.map((r) => (
          <EntityChip key={r.id} icon={r.icon} label={r.label} note={r.value} />
        ))}
      </div>
    </>
  )
}

const REACHES = [
  { id: "1", label: "1 · second row", note: "plain links under the tabs, counts inline", render: ReachSecondRow },
  { id: "1b", label: "1b · bubbles", note: "same row, chips — same shape as the cells below (shipped)", render: ReachBubbles },
  { id: "2", label: "2 · one menu", note: "reach joins the tab bar after a divider", render: ReachInMainMenu },
  { id: "3", label: "3 · dropdown", note: "one Reach button, list on demand", render: ReachDropdown },
  { id: "4", label: "4 · card", note: "blast-radius card (today)", render: ReachCard },
]

/* ── bench ───────────────────────────────────────────────────────────────── */

function Switcher<T extends { id: string; label: string; note: string }>({
  title, options, value, onChange,
}: { title: string; options: T[]; value: string; onChange: (id: string) => void }) {
  const active = options.find((o) => o.id === value)
  return (
    <div className="mb-3">
      <div className="type-section mb-1.5 text-muted-foreground-soft">{title}</div>
      <div className="flex flex-wrap gap-1.5">
        {options.map((o) => (
          <button
            key={o.id}
            onClick={() => onChange(o.id)}
            aria-pressed={o.id === value}
            className={cn(
              "type-row rounded-lg border px-2.5 py-1 transition-colors",
              o.id === value
                ? "border-transparent bg-primary font-medium text-primary-foreground"
                : "border-border bg-surface-raised text-muted-foreground hover:text-foreground",
            )}
          >
            {o.label}
          </button>
        ))}
      </div>
      {active && <p className="type-meta mt-1.5 text-muted-foreground-soft">{active.note}</p>}
    </div>
  )
}

function Body() {
  return (
    <div className="mt-4 grid gap-3.5 md:grid-cols-2 xl:grid-cols-4">
      {[
        { t: "Issues", n: 4, icon: CircleDot },
        { t: "Routines", n: 0, icon: Workflow },
        { t: "Triggers", n: 1, icon: Play },
        { t: "Credentials", n: 3, icon: KeyRound },
      ].map((c) => (
        <DetailCard key={c.t} title={c.t} subtitle={String(c.n)} bare>
          <div className="divide-y divide-border/40">
            {c.n === 0 ? (
              <p className="type-row px-4 py-6 text-center text-muted-foreground-soft">Nothing matches this filter.</p>
            ) : (
              Array.from({ length: Math.min(c.n, 3) }).map((_, i) => (
                <div key={i} className="flex items-start gap-2.5 px-4 py-2">
                  <span className="mt-px grid h-5 w-5 place-items-center rounded-md bg-surface-raised text-muted-foreground">
                    <c.icon className="h-3 w-3" />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="type-row block truncate">{c.t} row {i + 1}</span>
                    <span className="type-meta block font-mono text-muted-foreground">backlog · medium</span>
                  </span>
                </div>
              ))
            )}
          </div>
        </DetailCard>
      ))}
    </div>
  )
}

export default function DesignBenchPage() {
  const [header, setHeader] = useState("A")
  const [reach, setReach] = useState("4")

  const Header = HEADERS.find((h) => h.id === header)?.render ?? HeaderA
  const Reach = REACHES.find((r) => r.id === reach)?.render ?? ReachCard

  return (
    <div className="mx-auto w-full max-w-[1180px] px-6 py-6 md:px-8 lg:px-12">
      <div className="mb-5 rounded-xl border border-warn/30 bg-warn/[.06] px-4 py-2.5">
        <div className="type-section text-warn">Design bench</div>
        <p className="type-meta mt-0.5 text-muted-foreground">
          Not a product screen. Variants render with the real kit and the real type roles, so whatever wins here is
          already implementable. Pick one of each and say the number.
        </p>
      </div>

      <Switcher title="Header" options={HEADERS} value={header} onChange={setHeader} />
      <Switcher title="Reach" options={REACHES} value={reach} onChange={setReach} />

      <div className="mt-6 rounded-xl border border-border bg-background p-5">
        <Header />
        <div className="mt-4"><Reach /></div>
        <Body />
      </div>

      <div className="mt-5 flex items-center gap-2">
        <Clock className="h-3.5 w-3.5 text-muted-foreground-soft" />
        <span className="type-meta text-muted-foreground-soft">
          Live on dev3 while the branch is pinned. Delete this route before the branch merges.
        </span>
      </div>
    </div>
  )
}
