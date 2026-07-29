"use client"

import { useState } from "react"
import { ChevronRight, Clock, Terminal } from "lucide-react"

import { AgentAvatar } from "@/components/ui/agent-avatar"
import { cn } from "@/lib/utils"

// =============================================================================
// /design — a live wireframe bench, not a product screen.
// Real components, real type roles. Unlinked from the sidebar; delete before
// merge.
//
// ── the question: how small can the crews rail get ─────────────────────────
//
// Measured on the current rail: an agent row is 44px (32px portrait + 6px
// padding either side), a crew row is 52px (40px mark). Nine rows fill 420px.
//
// Two things are worth knowing before choosing:
//
// 1. The chevron is nearly redundant — clicking a crew row already selects it
//    AND expands it. But NOT collapses: the chevron is the only way back with
//    a mouse (keyboard has ArrowLeft). So "just delete it" silently removes
//    the only collapse affordance. Each variant below says what it does about
//    that rather than pretending the problem is not there.
//
// 2. The height is in the AGENT rows, not the crew rows — there are more of
//    them. Shrinking the crew mark feels tidier and saves almost nothing;
//    putting an agent on one line saves half of everything.
// =============================================================================

const CREWS = [
  {
    name: "Ops", icon: Terminal, running: 0, error: 0,
    agents: [
      { name: "Riley", role: "Platform Engineer", seed: "riley", style: "bottts-neutral" },
      { name: "Morgan", role: "SRE / Ops Lead", seed: "morgan", style: "fun-emoji" },
    ],
  },
  {
    name: "Engineering", icon: Terminal, running: 2, error: 1,
    agents: [
      { name: "Robin", role: "Frontend Engineer", seed: "robin", style: "notionists" },
      { name: "Sam", role: "Backend Engineer", seed: "sam", style: "croodles" },
      { name: "AlexXXXXXX", role: "Engineering Lead", seed: "alex", style: "open-peeps" },
    ],
  },
]

function Marks({ running, error }: { running: number; error: number }) {
  return (
    <span className="flex shrink-0 items-center gap-1">
      {error > 0 && (
        <span className="type-meta inline-flex items-center gap-1 text-destructive">
          <span className="h-1.5 w-1.5 rounded-full bg-destructive" />
          {error > 1 && error}
        </span>
      )}
      {running > 0 && (
        <span className="type-meta inline-flex items-center gap-1 text-success">
          <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-success" />
          {running > 1 && running}
        </span>
      )}
    </span>
  )
}

const Face = ({ a, px }: { a: (typeof CREWS)[0]["agents"][0]; px: string }) => (
  <AgentAvatar seed={a.seed} style={a.style} agentId={a.seed} className={cn(px, "shrink-0 rounded-lg")} />
)

/* ── A · chevron on hover ────────────────────────────────────────────────── */
function VariantA() {
  const [open, setOpen] = useState<string[]>(["Ops", "Engineering"])
  const toggle = (n: string) => setOpen((o) => (o.includes(n) ? o.filter((x) => x !== n) : [...o, n]))
  return (
    <Rail>
      {CREWS.map((c) => (
        <div key={c.name}>
          <button
            type="button"
            onClick={() => toggle(c.name)}
            className="group mx-1.5 flex w-[calc(100%-12px)] items-center gap-2 rounded-md px-2 py-1.5 hover:bg-white/[.04]"
          >
            <ChevronRight
              className={cn(
                "h-3 w-3 shrink-0 text-muted-foreground-soft opacity-0 transition-all group-hover:opacity-100",
                open.includes(c.name) && "rotate-90",
              )}
            />
            <c.icon className="h-4 w-4 shrink-0 text-primary" />
            <span className="type-row flex-1 truncate text-left font-semibold">{c.name}</span>
            <span className="type-meta tabular-nums text-muted-foreground-soft">{c.agents.length}</span>
            <Marks running={c.running} error={c.error} />
          </button>
          {open.includes(c.name) && (
            <div className="ml-[1.1rem] border-l border-border/70 pl-1">
              {c.agents.map((a) => (
                <div key={a.name} className="mx-1.5 flex items-center gap-2 rounded-md px-2 py-1.5">
                  <Face a={a} px="h-7 w-7" />
                  <span className="min-w-0 flex-1">
                    <span className="type-row block truncate font-medium">{a.name}</span>
                    <span className="type-meta block truncate text-muted-foreground">{a.role}</span>
                  </span>
                  <span className="type-meta text-muted-foreground-soft">Idle</span>
                </div>
              ))}
            </div>
          )}
        </div>
      ))}
    </Rail>
  )
}

/* ── B · crew as a section label ─────────────────────────────────────────── */
function VariantB() {
  return (
    <Rail>
      {CREWS.map((c) => (
        <div key={c.name} className="mb-1">
          <div className="flex items-center gap-2 px-3 pb-1 pt-2.5">
            <span className="type-section flex-1 truncate text-muted-foreground-soft">{c.name}</span>
            <span className="type-meta tabular-nums text-muted-foreground-soft">{c.agents.length}</span>
            <Marks running={c.running} error={c.error} />
          </div>
          {c.agents.map((a) => (
            <div key={a.name} className="mx-1.5 flex items-center gap-2 rounded-md px-2 py-1.5">
              <Face a={a} px="h-7 w-7" />
              <span className="min-w-0 flex-1">
                <span className="type-row block truncate font-medium">{a.name}</span>
                <span className="type-meta block truncate text-muted-foreground">{a.role}</span>
              </span>
              <span className="type-meta text-muted-foreground-soft">Idle</span>
            </div>
          ))}
        </div>
      ))}
    </Rail>
  )
}

/* ── C · one line per agent ──────────────────────────────────────────────── */
function VariantC() {
  return (
    <Rail>
      {CREWS.map((c) => (
        <div key={c.name} className="mb-1">
          <div className="flex items-center gap-2 px-3 pb-1 pt-2.5">
            <span className="type-section flex-1 truncate text-muted-foreground-soft">{c.name}</span>
            <span className="type-meta tabular-nums text-muted-foreground-soft">{c.agents.length}</span>
            <Marks running={c.running} error={c.error} />
          </div>
          {c.agents.map((a) => (
            <div key={a.name} className="mx-1.5 flex items-center gap-2 rounded-md px-2 py-1">
              <Face a={a} px="h-6 w-6" />
              <span className="type-row min-w-0 flex-1 truncate">
                <span className="font-medium">{a.name}</span>
                <span className="type-meta ml-1.5 text-muted-foreground">{a.role}</span>
              </span>
              <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-muted-foreground/40" title="idle" />
            </div>
          ))}
        </div>
      ))}
    </Rail>
  )
}

/* ── D · faces first, role only when selected ────────────────────────────── */
function VariantD() {
  const [sel, setSel] = useState("Sam")
  return (
    <Rail>
      {CREWS.map((c) => (
        <div key={c.name} className="mb-1">
          <div className="flex items-center gap-2 px-3 pb-1 pt-2.5">
            <span className="type-section flex-1 truncate text-muted-foreground-soft">{c.name}</span>
            <span className="type-meta tabular-nums text-muted-foreground-soft">{c.agents.length}</span>
            <Marks running={c.running} error={c.error} />
          </div>
          {c.agents.map((a) => (
            <button
              key={a.name}
              type="button"
              onClick={() => setSel(a.name)}
              className={cn(
                "mx-1.5 flex w-[calc(100%-12px)] items-center gap-2 rounded-md px-2 py-1 text-left",
                sel === a.name ? "bg-primary/10" : "hover:bg-white/[.04]",
              )}
            >
              <Face a={a} px="h-7 w-7" />
              <span className="min-w-0 flex-1">
                <span className="type-row block truncate font-medium">{a.name}</span>
                {sel === a.name && (
                  <span className="type-meta block truncate text-muted-foreground">{a.role}</span>
                )}
              </span>
              <span className="type-meta text-muted-foreground-soft">Idle</span>
            </button>
          ))}
        </div>
      ))}
    </Rail>
  )
}

/* ── E · a navigation register of its own ────────────────────────────────── */
function VariantE() {
  const [open, setOpen] = useState<string[]>(["Ops", "Engineering"])
  const toggle = (n: string) => setOpen((o) => (o.includes(n) ? o.filter((x) => x !== n) : [...o, n]))
  return (
    <Rail>
      {CREWS.map((c) => (
        <div key={c.name}>
          <button
            type="button"
            onClick={() => toggle(c.name)}
            className="mx-1.5 flex w-[calc(100%-12px)] items-center gap-2 rounded-md px-2 py-1 hover:bg-white/[.04]"
          >
            <c.icon className="h-3.5 w-3.5 shrink-0 text-primary" />
            <span className="flex-1 truncate text-left text-[0.8125rem] font-semibold leading-5">{c.name}</span>
            <span className="text-[0.6875rem] tabular-nums leading-4 text-muted-foreground-soft">
              {c.agents.length}
            </span>
            <Marks running={c.running} error={c.error} />
          </button>
          {open.includes(c.name) && (
            <div className="ml-[0.95rem] border-l border-border/70 pl-1">
              {c.agents.map((a) => (
                <div key={a.name} className="mx-1.5 flex items-center gap-2 rounded-md px-2 py-1">
                  <Face a={a} px="h-6 w-6" />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-[0.8125rem] font-medium leading-[1.15rem]">{a.name}</span>
                    <span className="block truncate text-[0.6875rem] leading-[0.95rem] text-muted-foreground">
                      {a.role}
                    </span>
                  </span>
                  <span className="text-[0.6875rem] leading-4 text-muted-foreground-soft">Idle</span>
                </div>
              ))}
            </div>
          )}
        </div>
      ))}
    </Rail>
  )
}


/* ── what actually shipped: D's role-on-selected + A's hover chevron ─────── */
function VariantShipped() {
  const [open, setOpen] = useState<string[]>(["Ops", "Engineering"])
  const [sel, setSel] = useState("Sam")
  const toggle = (n: string) => setOpen((o) => (o.includes(n) ? o.filter((x) => x !== n) : [...o, n]))
  return (
    <Rail>
      {CREWS.map((c) => (
        <div key={c.name}>
          <button
            type="button"
            onClick={() => toggle(c.name)}
            className="group/crew mx-1.5 flex w-[calc(100%-12px)] items-center gap-2 rounded-md px-2 py-1.5 hover:bg-white/[.04]"
          >
            <ChevronRight
              className={cn(
                "h-3 w-3 shrink-0 text-muted-foreground-soft transition-all",
                open.includes(c.name)
                  ? "rotate-90 opacity-0 group-hover/crew:opacity-100"
                  : "opacity-100",
              )}
            />
            <span className="grid h-7 w-7 shrink-0 place-items-center rounded-lg bg-primary/15">
              <c.icon className="h-3.5 w-3.5 text-primary" />
            </span>
            <span className="type-row flex-1 truncate text-left font-semibold">{c.name}</span>
            <span className="type-meta tabular-nums text-muted-foreground-soft">{c.agents.length}</span>
            <Marks running={c.running} error={c.error} />
          </button>
          {open.includes(c.name) && (
            <div className="ml-[1.1rem] border-l border-border/70 pl-1">
              {c.agents.map((a) => (
                <button
                  key={a.name}
                  type="button"
                  onClick={() => setSel(a.name)}
                  className={cn(
                    "mx-1.5 flex w-[calc(100%-12px)] items-center gap-2 rounded-md px-2 py-1.5 text-left",
                    sel === a.name ? "bg-primary/10" : "hover:bg-white/[.04]",
                  )}
                >
                  <Face a={a} px="h-8 w-8" />
                  <span className="min-w-0 flex-1">
                    <span className="type-row block truncate font-medium leading-tight">{a.name}</span>
                    {sel === a.name && (
                      <span className="type-meta block truncate text-muted-foreground">{a.role}</span>
                    )}
                  </span>
                  <span className="type-meta text-muted-foreground-soft">Idle</span>
                </button>
              ))}
            </div>
          )}
        </div>
      ))}
    </Rail>
  )
}

function Rail({ children }: { children: React.ReactNode }) {
  return (
    <div className="w-[272px] shrink-0 overflow-hidden rounded-xl border border-border/60 bg-card py-1.5">
      {children}
    </div>
  )
}

const VARIANTS = [
  {
    id: "A",
    title: "A · chevron only on hover",
    height: "unchanged",
    blurb:
      "Least disruptive. The chevron stops being ink at rest but is still there when you reach for " +
      "it, so collapsing still works with a mouse. The crew mark drops from 40px to a bare 16px " +
      "icon — the rail is quieter without any behaviour changing.",
    render: () => <VariantA />,
  },
  {
    id: "B",
    title: "B · crew as a section label",
    height: "−18%",
    blurb:
      "The crew stops being a row and becomes a heading, the way the admin rail already labels " +
      "PLATFORM and ORGANIZATIONS. No icon, no chevron, no guide line — a heading does not need to " +
      "explain that what follows belongs to it. Cost: crews are no longer collapsible or " +
      "selectable, so a crew page needs another way in.",
    render: () => <VariantB />,
  },
  {
    id: "C",
    title: "C · one line per agent",
    height: "−38%",
    blurb:
      "Where the height actually is. Name and role share a line, portrait drops to 24px, status " +
      "becomes a dot. Nine rows fit in the space six used to take. Cost: long role titles truncate " +
      "early, and the portrait is back to the size where a line-drawing style is a smudge.",
    render: () => <VariantC />,
  },
  {
    id: "D",
    title: "D · faces first",
    height: "−22%",
    blurb:
      "The role line only appears on the row you have selected. Scanning is by face and name, which " +
      "is how people actually find an agent they already know; the role is there when you need to " +
      "confirm. Cost: rows change height as you move, which some people find restless.",
    render: () => <VariantD />,
  },
  {
    id: "E",
    title: "E · a navigation register",
    height: "−28%",
    blurb:
      "Keeps every affordance and shrinks the type instead: 13px names, 11px roles, tighter rows, " +
      "24px portraits. A rail is a different reading task from a detail card, so it gets its own " +
      "register — but if this wins it becomes a NAMED role in globals.css, not sizes typed into " +
      "this file. That distinction is the whole reason the product stopped drifting.",
    render: () => <VariantE />,
  },
]

export default function DesignBench() {
  const [id, setId] = useState("A")
  const active = VARIANTS.find((v) => v.id === id) ?? VARIANTS[0]

  return (
    <div className="@container min-h-screen space-y-6 px-6 py-6 md:px-8 lg:px-12">
      <div className="rounded-xl border border-warn/30 bg-warn/[.06] px-4 py-3">
        <p className="type-row text-warn">
          Wireframe bench — not a product screen. Delete this route before the branch merges.
        </p>
      </div>

      <div>
        <h1 className="type-title">The crews rail — five ways to make it smaller</h1>
        <p className="type-row mt-2 max-w-3xl text-muted-foreground">
          Measured on the current rail: an agent row is 44px, a crew row 52px, nine rows fill 420px.
          The height is in the <b className="font-medium text-foreground">agent</b> rows, because
          there are more of them — shrinking the crew mark feels tidier and saves almost nothing.
        </p>
        <p className="type-row mt-2 max-w-3xl text-muted-foreground">
          About the chevron: nearly redundant, not entirely. Clicking a crew row already selects it{" "}
          <i>and</i> expands it — it does not collapse. The chevron is the only way back with a
          mouse. So each variant says what it does about that instead of deleting it and hoping.
        </p>
        <p className="type-row mt-2 max-w-3xl text-foreground">
          <b className="font-medium">Settled:</b> D&rsquo;s role-on-selected plus A&rsquo;s
          hover chevron. The measurement decided it — the row was 49px with a 29px portrait and a
          38px text column, so the <i>text</i> set the height, not the face. Cutting the second line
          made the portrait the tallest thing in the row and took it to 40px at the same time. Not
          C: −38% is the biggest number here, and it buys it by putting the portrait back to 24px,
          which is where a line-drawing style stops being a face.
        </p>
      </div>

      <div className="flex flex-wrap gap-2">
        {VARIANTS.map((v) => (
          <button
            key={v.id}
            type="button"
            onClick={() => setId(v.id)}
            className={cn(
              "type-row rounded-lg border px-3 py-1.5 transition-colors",
              v.id === id
                ? "border-primary bg-primary/15 text-primary-hover"
                : "border-border text-muted-foreground hover:border-foreground/25 hover:text-foreground",
            )}
          >
            {v.title}
            <span className="type-meta ml-2 text-muted-foreground-soft">{v.height}</span>
          </button>
        ))}
      </div>

      <p className="type-row max-w-3xl text-muted-foreground">{active.blurb}</p>

      <div className="flex flex-wrap items-start gap-6">
        <div>
          <p className="type-meta mb-1.5 text-muted-foreground-soft">Proposed</p>
          {active.render()}
        </div>
        <div>
          <p className="type-meta mb-1.5 text-success">Shipped — this is the rail today</p>
          <VariantShipped />
        </div>
        <div>
          <p className="type-meta mb-1.5 text-muted-foreground-soft">Before, for comparison</p>
          <Rail>
            {CREWS.map((c) => (
              <div key={c.name}>
                <div className="mx-1.5 flex items-center gap-2 rounded-md px-2 py-1.5">
                  <ChevronRight className="h-3 w-3 shrink-0 rotate-90 text-muted-foreground-soft" />
                  <span className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-primary/15">
                    <c.icon className="h-5 w-5 text-primary" />
                  </span>
                  <span className="type-row flex-1 truncate font-semibold">{c.name}</span>
                  <span className="type-meta tabular-nums text-muted-foreground-soft">{c.agents.length}</span>
                  <Marks running={c.running} error={c.error} />
                </div>
                <div className="ml-[1.1rem] border-l border-border/70 pl-1">
                  {c.agents.map((a) => (
                    <div key={a.name} className="mx-1.5 flex items-center gap-2 rounded-md px-2 py-1.5">
                      <Face a={a} px="h-8 w-8" />
                      <span className="min-w-0 flex-1">
                        <span className="type-row block truncate font-medium">{a.name}</span>
                        <span className="type-meta block truncate text-muted-foreground">{a.role}</span>
                      </span>
                      <span className="type-meta text-muted-foreground-soft">Idle</span>
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </Rail>
        </div>
      </div>

      <div className="flex items-center gap-2">
        <Clock className="h-3.5 w-3.5 text-muted-foreground-soft" />
        <span className="type-meta text-muted-foreground-soft">
          Nothing in the product has changed. Say a letter.
        </span>
      </div>
    </div>
  )
}
