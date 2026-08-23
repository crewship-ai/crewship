"use client"

/**
 * /design — the unification proposal, as a page in the product rather than a
 * mockup in a browser tab.
 *
 * It exists because the argument cannot be made in prose: "the create surfaces
 * are inconsistent" is a shrug, and twelve doors opening from one shell is
 * not. Everything under "Every door" is live — clicking `Crews → New agent`
 * opens the real CreateSurface with that form's whole field set, and the
 * Phone switch renders the same component in a handset so the mobile version
 * is reviewable without a phone.
 *
 * The page is also the adoption checklist. When the audit table has no amber
 * rows left, this page has no reason to exist and should be deleted along with
 * components/features/design.
 */

import * as React from "react"
import {
  AlertTriangle,
  ArrowRight,
  Check,
  Blocks,
  Columns3,
  ListChecks,
  Monitor,
  Palette,
  Ruler,
  ScrollText,
  Smartphone,
  Sparkles,
  SquareStack,
  Type,
} from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import { SubBar, SubBarPrimary, SubBarSecondary } from "@/components/layout/sub-bar"
import { ConceptIcon } from "@/components/ui/concept-icon"
import { DashboardCard } from "@/components/features/dashboard/dashboard-card"
import { CONCEPT_ACCENT } from "@/lib/concept-accents"
import {
  CreateSurfaceLoading,
  CreateSurfaceNotice,
  CreateSurfacePill,
  CreateSurfaceRefusal,
  CreateSurfaceTile,
} from "@/components/layout/create-surface"
import { DIVERGENCE, DOORS as AUDIT_ROWS, TOKEN_DRIFT, type Shell } from "./audit"
import { PARITY, SWEEP, parityTotals, summarise, type Severity, type UiState } from "./parity"
import { DOORS, DOORS_BY_PAGE, DoorDialog, DoorPhone, type Door } from "./surfaces/registry"

type Device = "desktop" | "phone"

export function DesignLayout() {
  const [device, setDevice] = React.useState<Device>("desktop")
  const [door, setDoor] = React.useState<Door | null>(null)
  const phoneRef = React.useRef<HTMLDivElement>(null)

  const openDoor = React.useCallback(
    (d: Door) => {
      setDoor(d)
      if (device === "phone") {
        // The handset renders inline; scroll it into view rather than leaving
        // the user wondering where the click went.
        window.requestAnimationFrame(() =>
          phoneRef.current?.scrollIntoView({ behavior: "smooth", block: "center" }),
        )
      }
    },
    [device],
  )

  const close = React.useCallback(() => setDoor(null), [])

  const switchDevice = React.useCallback((next: Device) => {
    setDevice(next)
    // A door open as a modal cannot stay open as a handset and vice versa —
    // close it so the switch is unambiguous.
    setDoor(null)
  }, [])

  return (
    <div className="flex h-[calc(100vh-48px)] flex-col bg-background">
      <SubBar
        icon={Palette}
        title="Design"
        section="Create surfaces"
        ariaLabel="Design"
        description={`${DIVERGENCE.doors} doors · ${DIVERGENCE.shells} shells · ${DIVERGENCE.widths} widths → 1 shell · 4 widths`}
        actions={
          <>
            <SubBarSecondary
              icon={Monitor}
              onClick={() => switchDevice("desktop")}
              aria-pressed={device === "desktop"}
              className={cn(device === "desktop" && "bg-primary/15 text-primary-hover")}
            >
              <span className="hidden sm:inline">Desktop</span>
            </SubBarSecondary>
            <SubBarSecondary
              icon={Smartphone}
              onClick={() => switchDevice("phone")}
              aria-pressed={device === "phone"}
              className={cn(device === "phone" && "bg-primary/15 text-primary-hover")}
            >
              <span className="hidden sm:inline">Phone</span>
            </SubBarSecondary>
            {/* By id, not by position. DOORS_BY_PAGE[4].doors[1] silently
                opened a different door the moment a page or a door was
                reordered — and it would have done so without failing a test. */}
            <SubBarPrimary
              icon={Sparkles}
              onClick={() => {
                const d = DOORS.find((x) => x.id === "new-agent")
                if (d) openDoor(d)
              }}
            >
              New agent
            </SubBarPrimary>
          </>
        }
      />

      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto max-w-[1180px] space-y-8 p-4 md:p-6">
          <Thesis />
          <Doors device={device} onOpen={openDoor} active={door} />
          {device === "phone" && (
            <div ref={phoneRef}>
              <PhoneStage door={door} onClose={close} />
            </div>
          )}
          <Parity />
          <Palette2 />
          <Anatomy />
          <AuditTable />
          <Parts />
          <TokenDrift />
          <Adoption />
        </div>
      </div>

      {device === "desktop" && <DoorDialog door={door} open={door !== null} onClose={close} />}
    </div>
  )
}

/* ---------------------------------------------------------------- Thesis -- */

function Thesis() {
  return (
    <section className="overflow-hidden rounded-xl border border-hairline bg-gradient-to-br from-primary/[0.10] via-card to-card">
      <div className="grid gap-6 p-5 md:grid-cols-[1.35fr_1fr] md:p-7">
        <div className="space-y-3">
          <span className="inline-flex items-center gap-1.5 rounded-full border border-primary/30 bg-primary/15 px-2.5 py-1 text-[11px] font-medium uppercase tracking-wider text-primary-hover">
            <Blocks className="h-3 w-3" />
            Proposal
          </span>
          <h2 className="text-xl font-semibold leading-tight text-foreground md:text-2xl">
            The doors are already identical.
            <br />
            <span className="text-muted-foreground">The rooms behind them are not.</span>
          </h2>
          <p className="max-w-[62ch] text-sm leading-relaxed text-muted-foreground">
            Every action in this product opens from the same sub-bar row — same height, same order, one soft
            primary and one ghost. That part was unified and it holds. What each of those buttons then opens was
            written {DIVERGENCE.doors} separate times: {DIVERGENCE.shells} different modal shells,{" "}
            {DIVERGENCE.widths} widths, two overlays, {DIVERGENCE.primaries} ways of drawing the confirm button.
            Two surfaces frost the page behind them and the other ten dim it, which is why those two feel like a
            different application.
          </p>
          <p className="max-w-[62ch] text-sm leading-relaxed text-foreground/85">
            The fix is not a redesign, and it is not a reduction. One shell —{" "}
            <code className="rounded bg-foreground/[0.07] px-1.5 py-0.5 font-mono text-xs">CreateSurface</code> —
            built out of the shape <em>New issue</em> already has, carrying every field the twelve carry today.
            Four fixed widths, one overlay, one footer, ⌘↵ everywhere, and a bottom sheet on a phone.
          </p>
        </div>

        <div className="grid grid-cols-2 gap-2.5 self-start">
          <Stat value={DIVERGENCE.doors} label="doors audited" tone="neutral" />
          <Stat value={DIVERGENCE.shells} label="modal shells" after="1" tone="bad" />
          <Stat value={DIVERGENCE.widths} label="widths" after="4" tone="bad" />
          <Stat value={DIVERGENCE.primaries} label="confirm buttons" after="1" tone="bad" />
          <Stat value={`${DIVERGENCE.cmdEnter}/${DIVERGENCE.doors}`} label="submit on ⌘↵" after="all" tone="bad" />
          <Stat value={`${DIVERGENCE.mobile}/${DIVERGENCE.doors}`} label="usable on a phone" after="all" tone="bad" />
        </div>
      </div>
    </section>
  )
}

function Stat({
  value,
  label,
  after,
  tone,
}: {
  value: React.ReactNode
  label: string
  after?: string
  tone: "neutral" | "bad"
}) {
  return (
    <div className="rounded-lg border border-hairline bg-card/60 px-3 py-2.5">
      <div className="flex items-baseline gap-1.5">
        <span
          className={cn(
            "text-2xl font-semibold tabular-nums leading-none",
            tone === "bad" ? "text-warn" : "text-foreground",
          )}
        >
          {value}
        </span>
        {after && (
          <>
            <ArrowRight className="h-3 w-3 shrink-0 text-muted-foreground-soft" />
            <span className="text-sm font-medium text-success">{after}</span>
          </>
        )}
      </div>
      <div className="mt-1 text-[11px] uppercase tracking-wider text-muted-foreground">{label}</div>
    </div>
  )
}

/* ----------------------------------------------------------------- Doors -- */

function Doors({
  device,
  onOpen,
  active,
}: {
  device: Device
  onOpen: (d: Door) => void
  active: Door | null
}) {
  return (
    <section className="space-y-3">
      <SectionHead
        icon={SquareStack}
        title="Every door"
        hint={device === "phone" ? "opens in the handset below" : "opens the real component"}
      >
        Not archetypes — the actual buttons, page by page, each carrying the field set of the component it
        replaces. Open two from different pages in a row: the header sits in the same place, the footer sits in
        the same place, and the primary action is the same button. The{" "}
        <strong className="text-foreground/85">{device === "phone" ? "Desktop" : "Phone"}</strong> switch in the
        bar above shows the other version.
      </SectionHead>

      <div className="grid items-start gap-3 md:grid-cols-2">
        {DOORS_BY_PAGE.map((group) => (
          <div key={group.page} className="rounded-xl border border-hairline bg-card p-3">
            <div className="mb-2.5 flex items-center gap-2">
              <ConceptIcon concept={group.concept} variant="chip" size="sm" />
              <span className="text-[13px] font-semibold text-foreground">{group.page}</span>
              <span className="ml-auto text-[11px] text-muted-foreground-soft">
                {group.doors.length === 0
                  ? "no create action"
                  : `${group.doors.length} door${group.doors.length > 1 ? "s" : ""}`}
              </span>
            </div>

            {group.doors.length === 0 ? (
              <p className="rounded-lg border border-dashed border-border/60 px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground">
                Activity is the one page with nothing to create, and that is correct. It is listed so the audit
                is not read as “Activity was missed”.
              </p>
            ) : (
              <div className="space-y-2">
                {group.doors.map((d) => (
                  <button
                    key={d.id}
                    type="button"
                    onClick={() => onOpen(d)}
                    aria-pressed={active?.id === d.id}
                    // The label spans are adjacent inline nodes, so the
                    // computed name would run together as
                    // "New agentlgCompose16 settings". Name it explicitly.
                    aria-label={`${d.page} — ${d.action}`}
                    className={cn(
                      "group/door flex w-full items-start gap-3 rounded-lg border p-3 text-left transition-colors",
                      active?.id === d.id
                        ? "border-primary/40 bg-primary/[0.08]"
                        : "border-hairline bg-foreground/[0.02] hover:border-primary/30 hover:bg-primary/[0.04]",
                    )}
                  >
                    <ConceptIcon concept={d.concept} variant="chip" size="md" />
                    <span className="min-w-0 flex-1">
                      <span className="flex flex-wrap items-center gap-1.5">
                        <span className="text-[13px] font-medium text-foreground">{d.action}</span>
                        <span className="rounded border border-hairline bg-foreground/[0.05] px-1.5 py-px font-mono text-[10px] text-muted-foreground">
                          {d.size}
                        </span>
                        <span className="rounded border border-primary/25 bg-primary/10 px-1.5 py-px text-[10px] text-primary-hover">
                          {d.archetype}
                        </span>
                        <span className="ml-auto text-[10px] tabular-nums text-muted-foreground-soft">
                          {d.fields.length} settings
                        </span>
                      </span>
                      <span className="mt-1 block text-xs leading-relaxed text-muted-foreground">{d.blurb}</span>
                      <span className="mt-1.5 flex flex-wrap gap-1">
                        {d.fields.slice(0, 6).map((f) => (
                          <span
                            key={f}
                            className="rounded bg-foreground/[0.05] px-1.5 py-px text-[10px] text-muted-foreground-soft"
                          >
                            {f}
                          </span>
                        ))}
                        {d.fields.length > 6 && (
                          <span className="px-1 text-[10px] text-muted-foreground-soft">
                            +{d.fields.length - 6} more
                          </span>
                        )}
                      </span>
                    </span>
                  </button>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </section>
  )
}

/* ------------------------------------------------------------ PhoneStage -- */

function PhoneStage({ door, onClose }: { door: Door | null; onClose: () => void }) {
  return (
    <section className="space-y-3">
      <SectionHead icon={Smartphone} title="On a phone" hint="the same component, 390px wide">
        A bottom sheet, not a centred card and not a full-screen takeover. The primary action sits at the thumb,
        the page stays visible above it, the height is <code className="font-mono">dvh</code> so Safari&apos;s
        collapsing toolbar cannot hide the footer, and every control is 44px. Steps become a progress bar; pills
        become one scrolling row; two-column grids become one column.
      </SectionHead>

      {door ? (
        <>
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
            <ConceptIcon concept={door.concept} size="sm" />
            <span className="font-medium text-foreground">
              {door.page} → {door.action}
            </span>
            <span className="text-muted-foreground-soft">· {door.fields.length} settings, all present</span>
            <Button variant="ghost" size="sm" className="ml-auto h-7 text-xs" onClick={onClose}>
              Reset
            </Button>
          </div>
          <DoorPhone door={door} onClose={onClose} />
        </>
      ) : (
        <div className="rounded-xl border border-dashed border-border/60 px-4 py-10 text-center">
          <Smartphone className="mx-auto h-6 w-6 text-muted-foreground-soft" />
          <p className="mt-2 text-xs text-muted-foreground">
            Pick a door above and it renders here, in a handset, at the real breakpoint.
          </p>
        </div>
      )}
    </section>
  )
}

/* ---------------------------------------------------------------- Parity -- */

const UI_LABEL: Record<UiState, { text: string; className: string }> = {
  create: { text: "in the dialog", className: "text-success" },
  detail: { text: "after creating", className: "text-warn" },
  none: { text: "nowhere", className: "text-destructive" },
}

const SEVERITY_LABEL: Record<Severity, { text: string; className: string }> = {
  blocker: { text: "blocker", className: "bg-destructive/15 text-destructive" },
  gap: { text: "gap", className: "bg-warn/15 text-warn" },
  deferred: { text: "deferred", className: "bg-info/15 text-info" },
  fine: { text: "fine", className: "bg-success/15 text-success" },
}

function Parity() {
  const totals = parityTotals(PARITY)
  const perSurface = summarise(PARITY)
  const [onlyGaps, setOnlyGaps] = React.useState(true)

  const rows = onlyGaps ? PARITY.filter((r) => r.ui !== "create") : PARITY

  return (
    <section className="space-y-3">
      <SectionHead icon={ScrollText} title="Can the UI do what the CLI can?" hint="parity ledger">
        The unification so far answers whether the twelve doors <em>look</em> alike. This answers the harder
        question underneath: does somebody who only has the web UI have the same product as somebody with a
        terminal. CLAUDE.md already fixes one direction of that contract — every API endpoint gets a CLI
        command — and nothing has ever fixed the other, so each dialog exposes whatever its author happened to
        need. Every row below was read out of the API handler or the CLI command it cites.
      </SectionHead>

      {/* The headline number. It is not a row in the table because it is not a
          capability — it is the shape of the whole surface. */}
      <div className="overflow-hidden rounded-xl border border-destructive/30 bg-gradient-to-br from-destructive/[0.08] via-card to-card p-5">
        <div className="grid gap-5 md:grid-cols-[1fr_auto] md:items-center">
          <div className="space-y-2">
            <div className="flex flex-wrap items-baseline gap-2">
              <span className="text-3xl font-semibold tabular-nums leading-none text-destructive">
                {SWEEP.noWebUi}
              </span>
              <span className="text-sm text-foreground">
                endpoints a person can reach from a terminal and not from the browser
              </span>
            </div>
            <p className="max-w-[70ch] text-xs leading-relaxed text-muted-foreground">
              That is {Math.round((SWEEP.noWebUi / SWEEP.candidatePaths) * 100)}% of the{" "}
              {SWEEP.candidatePaths} routes a screen could plausibly exist for — sidecar IPC, auth plumbing
              and health checks already excluded. The reverse gap is {SWEEP.uiAheadOfCli}: exactly two
              endpoints have a UI and no CLI command, and both are convenience aggregates rather than
              capabilities. CLAUDE.md&apos;s rule that every endpoint gets a CLI command is holding. Nothing
              has ever held the other direction.
            </p>
            <p className="max-w-[70ch] text-[11px] leading-relaxed text-muted-foreground-soft">
              Method: walked from the built CLI binary and the router registrations, then cross-referenced
              against every <code className="font-mono">/api/v1/…</code> literal in the frontend with each
              non-match hand-verified. The read-side sweep came back complete; three commissioned
              mutation-side sub-audits did not return, so <strong className="text-foreground/70">treat 90 as
              a floor</strong>.
            </p>
          </div>

          <div className="grid grid-cols-2 gap-2.5 md:w-[300px]">
            <Stat value={SWEEP.cliLeafCommands} label="CLI commands" tone="neutral" />
            <Stat value={SWEEP.uniquePaths} label="HTTP routes" tone="neutral" />
            <Stat value={SWEEP.candidatePaths} label="could have a screen" tone="neutral" />
            <Stat value={SWEEP.noWebUi} label="have none" tone="bad" />
          </div>
        </div>
      </div>

      {PARITY.length === 0 ? (
        <CreateSurfaceNotice tone="warn" icon={AlertTriangle}>
          <strong className="text-foreground">Not measured yet.</strong> This table is deliberately empty rather
          than encouraging: an audit that has not run is not the same as an audit that found nothing.
        </CreateSurfaceNotice>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-6">
            <Stat value={totals.capabilities} label="capabilities" tone="neutral" />
            <Stat value={totals.reachable} label="reachable in the UI" tone="neutral" />
            <Stat value={totals.deferred} label="only after creating" tone="bad" />
            <Stat value={totals.unreachable} label="nowhere in the UI" tone="bad" />
            <Stat value={totals.blockers} label="blockers" tone="bad" />
            <Stat value={PARITY.filter((r) => r.fixed).length} label="closed today" tone="neutral" />
          </div>

          {/* Per-surface bars: which door is furthest behind its own endpoint. */}
          <div className="grid gap-2 md:grid-cols-2">
            {perSurface.map((s) => {
              const pct = s.total > 0 ? (s.create / s.total) * 100 : 0
              return (
                <div key={s.surface} className="rounded-lg border border-hairline bg-card p-3">
                  <div className="flex items-baseline gap-2">
                    <span className="truncate text-xs font-medium text-foreground">{s.surface}</span>
                    <span className="ml-auto shrink-0 text-[11px] tabular-nums text-muted-foreground">
                      {s.create}/{s.total} in the dialog
                    </span>
                  </div>
                  <div className="mt-2 flex h-1.5 w-full overflow-hidden rounded-full bg-muted">
                    <span className="bg-success" style={{ width: `${pct}%` }} />
                    <span className="bg-warn" style={{ width: `${(s.detail / s.total) * 100}%` }} />
                    <span className="bg-destructive" style={{ width: `${(s.none / s.total) * 100}%` }} />
                  </div>
                  {/* Not "cannot be created": several blockers are on surfaces
                      that create nothing (Chat, Timeline, Inbox). A blocker is
                      a capability the browser silently fails at or cannot
                      complete, whatever the surface. */}
                  {s.blockers > 0 && (
                    <div className="mt-1.5 text-[11px] text-destructive">
                      {s.blockers} blocker{s.blockers > 1 ? "s" : ""} — silently fails, or cannot be finished
                      without a terminal
                    </div>
                  )}
                </div>
              )
            })}
          </div>

          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={() => setOnlyGaps((v) => !v)}
              aria-pressed={onlyGaps}
            >
              {onlyGaps ? `Showing ${rows.length} gaps` : `Showing all ${PARITY.length}`}
            </Button>
            <span className="text-[11px] text-muted-foreground-soft">
              green = in the dialog · amber = only after creating · red = nowhere
            </span>
          </div>

          <div className="overflow-x-auto rounded-xl border border-hairline bg-card">
            <table className="w-full min-w-[900px] border-collapse text-xs">
              <thead>
                <tr className="border-b border-hairline text-left">
                  {["Surface", "Capability", "Exists in", "Reference", "In the UI", "", "Why it matters"].map(
                    (h, i) => (
                      <th
                        key={i}
                        className="px-3 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground"
                      >
                        {h}
                      </th>
                    ),
                  )}
                </tr>
              </thead>
              <tbody>
                {rows.map((r, i) => {
                  const ui = UI_LABEL[r.ui]
                  const sev = SEVERITY_LABEL[r.severity]
                  return (
                    <tr
                      key={`${r.surface}-${r.capability}-${i}`}
                      className="border-b border-hairline/60 last:border-0 hover:bg-foreground/[0.02]"
                    >
                      <td className="whitespace-nowrap px-3 py-2 text-foreground/80">{r.surface}</td>
                      <td className="px-3 py-2 text-foreground">{r.capability}</td>
                      <td className="px-3 py-2 font-mono text-[11px] uppercase text-muted-foreground">
                        {r.where}
                      </td>
                      <td className="px-3 py-2 font-mono text-[10px] text-muted-foreground-soft">
                        {r.ref}
                        {r.cli && <span className="block text-primary-hover">{r.cli}</span>}
                      </td>
                      <td className={cn("whitespace-nowrap px-3 py-2 text-[11px]", ui.className)}>{ui.text}</td>
                      <td className="px-3 py-2">
                        <span className={cn("rounded px-1.5 py-0.5 text-[10px] font-medium", sev.className)}>
                          {sev.text}
                        </span>
                      </td>
                      <td className="px-3 py-2 leading-relaxed text-muted-foreground">
                        {r.note}
                        {r.fixed && (
                          <span className="mt-1.5 flex items-start gap-1.5 rounded-md border border-success/25 bg-success/[0.07] p-1.5">
                            <Check className="mt-px h-3 w-3 shrink-0 text-success" />
                            <span className="min-w-0">
                              <span className="font-medium text-success">Closed {r.fixed.on}</span>
                              <span className="text-foreground/75"> — {r.fixed.how}</span>
                            </span>
                          </span>
                        )}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
    </section>
  )
}

/* --------------------------------------------------------------- Palette -- */

const RAIL_GROUPS: { label: string; concepts: string[] }[] = [
  { label: "Plan", concepts: ["dashboard", "inbox", "sessions", "issues", "routines", "pages"] },
  { label: "Run", concepts: ["activity", "journal"] },
  { label: "Build", concepts: ["crews", "skills", "credentials", "integrations"] },
  { label: "System", concepts: ["marketplace", "design", "settings", "admin"] },
]

function Palette2() {
  return (
    <section className="space-y-3">
      <SectionHead icon={Palette} title="A colour per concept" hint="from the semantic palette, not a new one">
        <code className="font-mono">concept-icons.ts</code> already fixed which glyph a concept wears.{" "}
        <code className="font-mono">concept-accents.ts</code> fixes what colour it is, and every value is one of
        the tokens globals.css already declares —{" "}
        <code className="font-mono">--primary · --info · --notice · --success · --warn · --gold · --purple ·
        --destructive</code>{" "}
        — so nothing here is a second palette to keep in step. Concepts in the same rail group get different
        hues, because that is what makes a group scannable; concepts that mean the same thing on two screens get
        the same hue even when they sit far apart.
      </SectionHead>

      <div className="grid gap-3 md:grid-cols-[1fr_260px]">
        <div className="rounded-xl border border-hairline bg-card p-4">
          <div className="grid gap-x-4 gap-y-3 sm:grid-cols-2">
            {RAIL_GROUPS.map((g) => (
              <div key={g.label}>
                <div className="mb-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground-soft">
                  {g.label}
                </div>
                <div className="space-y-1">
                  {g.concepts.map((c) => (
                    <div key={c} className="flex items-center gap-2">
                      <ConceptIcon concept={c} variant="chip" size="sm" />
                      <span className="text-xs capitalize text-foreground/85">{c}</span>
                      <span className="ml-auto font-mono text-[10px] text-muted-foreground-soft">
                        {(CONCEPT_ACCENT as Record<string, string>)[c]}
                      </span>
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </div>
        </div>

        {/* Before / after, because "colour the rail" is easy to picture wrong. */}
        <div className="rounded-xl border border-hairline bg-card p-4">
          <div className="mb-2.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
            The rail
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <div className="mb-1.5 text-[10px] text-muted-foreground-soft">now</div>
              <div className="space-y-1.5">
                {["issues", "routines", "crews", "credentials"].map((c) => (
                  <div key={c} className="flex items-center gap-2">
                    <ConceptIcon concept={c} accent="slate" size="sm" />
                    <span className="text-[11px] capitalize text-muted-foreground">{c}</span>
                  </div>
                ))}
              </div>
            </div>
            <div>
              <div className="mb-1.5 text-[10px] text-primary-hover">proposed</div>
              <div className="space-y-1.5">
                {["issues", "routines", "crews", "credentials"].map((c) => (
                  <div key={c} className="flex items-center gap-2">
                    <ConceptIcon concept={c} size="sm" />
                    <span className="text-[11px] capitalize text-foreground/85">{c}</span>
                  </div>
                ))}
              </div>
            </div>
          </div>
          <p className="mt-3 text-[11px] leading-relaxed text-muted-foreground-soft">
            The rail itself is unchanged so far — this is the proposal, not a shipped switch. Wiring it is one
            line in <code className="font-mono">app-sidebar.tsx</code>.
          </p>
        </div>
      </div>
    </section>
  )
}

/* --------------------------------------------------------------- Anatomy -- */

const ANATOMY: { part: string; rule: string }[] = [
  { part: "Header", rule: "Coloured concept chip, context › title, one close button. Back is a footer button, never an arrow that appears here on step 2. On a phone the context drops and the title survives." },
  { part: "Steps", rule: "Chips on a pointer device — completed ones clickable, forward ones not. On a phone, a labelled progress bar, because five chips do not fit and a strip that scrolls sideways hides what it exists to show." },
  { part: "Body", rule: "The only scrollport. Section / Grid / Field / Choice / ToggleRow / Disclosure — enough to lay out a twenty-field form without one bespoke wrapper. Grids collapse to one column on a phone." },
  { part: "Pills", rule: "Optional metadata on the bottom edge. Wraps on a pointer device; one scrolling row on a phone, so six pills do not eat a third of the sheet." },
  { part: "Footer", rule: "Outside the scrollport. Hint · Cancel · at most one secondary · exactly one primary, always in that order. On a phone the hint goes, the buttons go full width at 44px, and the padding clears the home indicator." },
]

function Anatomy() {
  return (
    <section className="space-y-3">
      <SectionHead icon={Ruler} title="Anatomy" hint="one shell, five parts, two layouts">
        The geometry the twelve surfaces had each chosen for themselves, fixed once. Nothing here is
        configurable per page — that is the point of it.
      </SectionHead>

      <div className="grid gap-4 md:grid-cols-[minmax(0,460px)_1fr]">
        <div className="self-start overflow-hidden rounded-lg border border-hairline bg-card shadow-lg">
          <AnatomyRow label="Header">
            <div className="flex items-center gap-2 px-3 py-2.5">
              <ConceptIcon concept="issues" variant="chip" size="sm" />
              <span className="text-xs text-muted-foreground">Platform</span>
              <span className="text-muted-foreground-soft">›</span>
              <span className="text-xs font-medium text-foreground">New issue</span>
              <span className="ml-auto text-muted-foreground-soft">×</span>
            </div>
          </AnatomyRow>
          <AnatomyRow label="Steps">
            <div className="flex items-center gap-1 px-3 py-1.5">
              <span className="rounded-full bg-primary/15 px-2 py-0.5 text-[10px] font-medium text-primary-hover">
                1 Identity
              </span>
              <span className="h-px w-3 bg-border" />
              <span className="px-2 py-0.5 text-[10px] text-muted-foreground-soft">2 Lineup</span>
              <span className="h-px w-3 bg-border" />
              <span className="px-2 py-0.5 text-[10px] text-muted-foreground-soft">3 Review</span>
            </div>
          </AnatomyRow>
          <AnatomyRow label="Body">
            <div className="space-y-2 px-3 py-4">
              <div className="text-sm font-medium text-muted-foreground/50">Issue title</div>
              <div className="h-1.5 w-4/5 rounded bg-foreground/[0.06]" />
              <div className="h-1.5 w-3/5 rounded bg-foreground/[0.06]" />
              <div className="h-1.5 w-2/3 rounded bg-foreground/[0.06]" />
            </div>
          </AnatomyRow>
          <AnatomyRow label="Pills">
            <div className="flex flex-wrap gap-1.5 px-3 py-2">
              <CreateSurfacePill concept="issues" readOnly className="h-6 text-[11px]">
                Backlog
              </CreateSurfacePill>
              <CreateSurfacePill className="h-6 text-[11px]">Priority</CreateSurfacePill>
              <CreateSurfacePill concept="crews" set className="h-6 text-[11px]">
                keeper
              </CreateSurfacePill>
            </div>
          </AnatomyRow>
          <AnatomyRow label="Footer" last>
            <div className="flex items-center gap-2 px-3 py-2.5">
              <span className="text-[10px] text-muted-foreground-soft">⌘↵ · Esc</span>
              <span className="flex-1" />
              <span className="rounded px-2 py-1 text-[11px] text-muted-foreground">Cancel</span>
              <span className="rounded-md bg-primary px-2.5 py-1 text-[11px] font-medium text-primary-foreground">
                Create
              </span>
            </div>
          </AnatomyRow>
        </div>

        <div className="space-y-2">
          {ANATOMY.map((a) => (
            <div key={a.part} className="rounded-lg border border-hairline bg-card p-3">
              <div className="mb-1 text-[11px] font-semibold uppercase tracking-wider text-primary-hover">
                {a.part}
              </div>
              <p className="text-xs leading-relaxed text-muted-foreground">{a.rule}</p>
            </div>
          ))}

          <div className="grid grid-cols-4 gap-2 pt-1">
            {[
              { k: "sm", px: "480", use: "one question" },
              { k: "md", px: "640", use: "a form" },
              { k: "lg", px: "800", use: "form + preview" },
              { k: "xl", px: "960", use: "a catalogue" },
            ].map((s) => (
              <div key={s.k} className="rounded-lg border border-hairline bg-card px-2.5 py-2 text-center">
                <div className="font-mono text-xs font-semibold text-foreground">{s.k}</div>
                <div className="font-mono text-[11px] tabular-nums text-primary-hover">{s.px}</div>
                <div className="mt-0.5 text-[10px] leading-tight text-muted-foreground">{s.use}</div>
              </div>
            ))}
          </div>
          <p className="flex items-start gap-1.5 text-[11px] leading-relaxed text-muted-foreground-soft">
            <Smartphone className="mt-px h-3 w-3 shrink-0" />
            Below <code className="font-mono">sm</code> the width stops mattering: every surface becomes the same
            bottom sheet. One of twelve behaves that way today.
          </p>
        </div>
      </div>
    </section>
  )
}

function AnatomyRow({
  label,
  last = false,
  children,
}: {
  label: string
  last?: boolean
  children: React.ReactNode
}) {
  return (
    <div className={cn("grid grid-cols-[56px_1fr] items-stretch", !last && "border-b border-hairline")}>
      <span className="flex items-center justify-end border-r border-hairline bg-foreground/[0.02] pr-2 font-mono text-[9px] uppercase tracking-wider text-primary-hover/60">
        {label}
      </span>
      <div className="min-w-0">{children}</div>
    </div>
  )
}

/* ----------------------------------------------------------- Audit table -- */

const SHELL_LABEL: Record<Shell, { text: string; className: string }> = {
  radix: { text: "Radix Dialog", className: "text-muted-foreground" },
  "hand-rolled": { text: "hand-rolled", className: "text-warn" },
  "hand-rolled-blur": { text: "hand-rolled + blur", className: "text-destructive" },
}

function AuditTable() {
  return (
    <section className="space-y-3">
      <SectionHead
        icon={ListChecks}
        title="What each door used to open"
        hint={`all ${DIVERGENCE.migrated} of ${DIVERGENCE.doors} have since landed on the shell`}
      >
        Every row was read out of the component named in it, and every row is now history: all twelve doors
        mount the shared shell. The before is kept rather than rewritten, because the argument for the shell IS
        the eleven widths and the two overlays — an audit that erases itself as the work lands leaves this page
        asserting a conclusion with nothing behind it. Amber was a divergence worth naming; red was one a user
        could see without being told what to look for.
      </SectionHead>

      <div className="overflow-x-auto rounded-xl border border-hairline bg-card">
        <table className="w-full min-w-[880px] border-collapse text-xs">
          <thead>
            <tr className="border-b border-hairline text-left">
              {["Page", "Action", "Shell", "Width", "Confirm button", "⌘↵", "Phone", "Becomes"].map((h) => (
                <th
                  key={h}
                  className="px-3 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground"
                >
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {AUDIT_ROWS.map((d) => {
              const shell = SHELL_LABEL[d.shell]
              const varying = d.width.includes("→")
              return (
                <tr
                  key={`${d.page}-${d.action}`}
                  className="border-b border-hairline/60 last:border-0 hover:bg-foreground/[0.02]"
                >
                  <td className="px-3 py-2">
                    <span className="flex items-center gap-1.5 font-medium text-foreground/80">
                      <ConceptIcon concept={d.page.toLowerCase()} size="sm" className="h-3 w-3" />
                      {d.page}
                    </span>
                  </td>
                  <td className="px-3 py-2 text-foreground">{d.action}</td>
                  <td className={cn("px-3 py-2 font-mono text-[11px]", shell.className)}>{shell.text}</td>
                  <td
                    className={cn(
                      "px-3 py-2 font-mono text-[11px] tabular-nums",
                      varying ? "text-warn" : "text-muted-foreground",
                    )}
                  >
                    {d.width}
                  </td>
                  <td className="px-3 py-2 font-mono text-[11px] text-muted-foreground">{d.primary}</td>
                  <td className="px-3 py-2">
                    <Mark ok={d.cmdEnter} />
                  </td>
                  <td className="px-3 py-2">
                    <Mark ok={d.mobile} />
                  </td>
                  <td className="px-3 py-2">
                    {d.migrated ? (
                      <span
                        title={d.migrated.note}
                        className="flex items-center gap-1 whitespace-nowrap rounded border border-success/30 bg-success/10 px-1.5 py-0.5 font-mono text-[10px] text-success"
                      >
                        <Check className="h-3 w-3" />
                        {d.proposed}
                      </span>
                    ) : (
                      <span className="rounded border border-primary/30 bg-primary/10 px-1.5 py-0.5 font-mono text-[10px] text-primary-hover">
                        {d.proposed}
                      </span>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
      <div className="grid gap-2 md:grid-cols-2">
        {AUDIT_ROWS.filter((d) => d.migrated?.note).map((d) => (
          <div key={`${d.page}-${d.action}`} className="rounded-lg border border-hairline bg-card p-3">
            <div className="flex items-center gap-1.5">
              <Check className="h-3 w-3 shrink-0 text-success" />
              <span className="text-[11px] font-medium text-foreground">
                {d.page} → {d.action}
              </span>
            </div>
            <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">{d.migrated!.note}</p>
          </div>
        ))}
      </div>
    </section>
  )
}

function Mark({ ok }: { ok: boolean }) {
  return ok ? (
    <span className="text-success" aria-label="yes">
      ✓
    </span>
  ) : (
    <span className="text-destructive/70" aria-label="no">
      ✕
    </span>
  )
}

/* ----------------------------------------------------------------- Parts -- */

function Parts() {
  return (
    <section className="space-y-3">
      <SectionHead icon={Columns3} title="The parts" hint="every piece a surface is allowed to use">
        If a create surface needs something that is not here, that is a gap in the kit — not a licence to write
        one more bespoke component in a feature folder.
      </SectionHead>

      <div className="grid items-start gap-3 md:grid-cols-2">
        <DashboardCard title="Actions" icon={Sparkles}>
          <div className="space-y-3">
            <PartRow label="Sub-bar (the door)">
              <SubBarSecondary icon={Palette}>Import</SubBarSecondary>
              <SubBarPrimary icon={Sparkles}>New issue</SubBarPrimary>
            </PartRow>
            <PartRow label="Footer (the room)">
              <Button variant="ghost" size="sm" className="h-8 text-xs">
                Cancel
              </Button>
              <Button variant="outline" size="sm" className="h-8 text-xs">
                Back
              </Button>
              <Button size="sm" className="h-8 text-xs">
                Create issue
              </Button>
            </PartRow>
            <PartRow label="On a phone">
              <Button variant="ghost" size="sm" className="h-11 flex-1 text-sm">
                Cancel
              </Button>
              <Button size="sm" className="h-11 flex-[2] text-sm">
                Create issue
              </Button>
            </PartRow>
            <p className="text-[11px] leading-relaxed text-muted-foreground-soft">
              The sub-bar primary is <code className="font-mono">soft</code> (tinted); the footer primary is{" "}
              <code className="font-mono">default</code> (solid). Deliberate: a solid button on every page&apos;s
              toolbar reads as an alarm, and a tinted one at the end of a form reads as optional.
            </p>
          </div>
        </DashboardCard>

        <DashboardCard title="Pills" icon={Type}>
          <div className="flex flex-wrap gap-1.5">
            <CreateSurfacePill concept="issues" readOnly>
              Backlog
            </CreateSurfacePill>
            <CreateSurfacePill>Project</CreateSurfacePill>
            <CreateSurfacePill concept="credentials" set>
              ANTHROPIC_API_KEY
            </CreateSurfacePill>
          </div>
          <p className="mt-3 text-[11px] leading-relaxed text-muted-foreground-soft">
            Three states, one component: read-only (a fact the surface will not let you change), unset (muted,
            invites a click), set (full strength, carries a value). 28px on a pointer device, 36px on a phone.
          </p>
        </DashboardCard>

        <DashboardCard title="Tiles" icon={SquareStack}>
          <div className="space-y-2">
            <CreateSurfaceTile
              concept="crews"
              title="Reviewer pair"
              description="One writes, one reviews. The default for code work."
              meta="2 agents"
              selected
            />
            <CreateSurfaceTile
              concept="routines"
              title="Nightly sweep"
              description="Runs at 03:00, closes stale issues."
              meta="412 runs"
            />
          </div>
        </DashboardCard>

        <DashboardCard title="States the shell owns" icon={AlertTriangle}>
          <div className="space-y-3">
            <div>
              <div className="mb-1.5 text-[11px] uppercase tracking-wider text-muted-foreground">
                Refusal — what the server said when it said no
              </div>
              <div className="overflow-hidden rounded-lg border border-hairline">
                <CreateSurfaceRefusal
                  message="The crew was not created."
                  fields={[
                    { field: "slug", reason: "already in use in this workspace" },
                    { field: "container_cpus", reason: "must be between 0.01 and 512" },
                  ]}
                />
              </div>
            </div>
            <div>
              <div className="mb-1.5 text-[11px] uppercase tracking-wider text-muted-foreground">
                Loading — for the doors that fetch before they can draw
              </div>
              <div className="rounded-lg border border-hairline p-3">
                <CreateSurfaceLoading rows={2} />
              </div>
            </div>
            <p className="text-[11px] leading-relaxed text-muted-foreground-soft">
              Neither existed in any of the twelve. A 400 arrived as a toast that had faded by the time you
              looked up, or as nothing; a surface waiting on crews and agents drew its own spinner. The third
              new state has no picture because it is an interaction —{" "}
              <strong className="text-foreground/80">type into any door above and press Esc</strong>.
            </p>
          </div>
        </DashboardCard>

        <DashboardCard title="Notices" icon={ListChecks}>
          <div className="space-y-2">
            <CreateSurfaceNotice tone="info">
              Defaults are sane. Change these only when a crew has proven it needs more.
            </CreateSurfaceNotice>
            <CreateSurfaceNotice tone="ok">
              Written encrypted, and readable only by the containers you named.
            </CreateSurfaceNotice>
            <CreateSurfaceNotice tone="warn">
              A custom image is rebuilt on first run and can take several minutes.
            </CreateSurfaceNotice>
            <CreateSurfaceNotice tone="error">
              <strong className="text-foreground">routine:nightly-sweep</strong> — no routine of that slug exists
              here.
            </CreateSurfaceNotice>
          </div>
        </DashboardCard>
      </div>
    </section>
  )
}

function PartRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <span className="w-32 shrink-0 text-[11px] uppercase tracking-wider text-muted-foreground">{label}</span>
      {children}
    </div>
  )
}

/* ----------------------------------------------------------- Token drift -- */

function TokenDrift() {
  return (
    <section className="space-y-3">
      <SectionHead icon={Palette} title="One level down" hint="the same disease, in the tokens">
        Unifying the shells and stopping there would leave these in place, and they are what makes two surfaces
        built from the same components still fail to match.
      </SectionHead>

      <div className="grid gap-2">
        {TOKEN_DRIFT.map((t) => (
          <div
            key={t.what}
            className="grid gap-2 rounded-lg border border-hairline bg-card p-3 md:grid-cols-[200px_1fr_auto]"
          >
            <div className="text-xs font-semibold text-foreground">{t.what}</div>
            <div className="min-w-0">
              <div className="truncate font-mono text-[11px] text-muted-foreground">{t.detail}</div>
              <div className="mt-1 flex items-center gap-1.5 text-[11px]">
                <ArrowRight className="h-3 w-3 shrink-0 text-success" />
                <span className="text-foreground/85">{t.fix}</span>
              </div>
            </div>
            <span className="shrink-0 self-start rounded border border-warn/30 bg-warn/10 px-1.5 py-0.5 font-mono text-[10px] text-warn">
              {t.count}
            </span>
          </div>
        ))}
      </div>
    </section>
  )
}

/* -------------------------------------------------------------- Adoption -- */

const PHASES = [
  {
    n: 1,
    title: "Kill the two hand-rolled shells",
    body: "Import page and New page are the only surfaces that blur the background, and neither has a focus trap. Moving them onto CreateSurface removes the blur, the second corner radius and an accessibility bug in one change.",
    files: "page-import-dialog.tsx · page-editor.tsx",
    risk: "low",
  },
  {
    n: 2,
    title: "Fix the width that moves",
    body: "New crew grows 680 → 940px between step 1 and step 2, which walks the footer out from under the cursor mid-flow. Pin it at lg.",
    files: "create-crew-dialog.tsx",
    risk: "low",
  },
  {
    n: 3,
    title: "One footer, one primary, ⌘↵ everywhere",
    body: "Four of the twelve draw their confirm button as a raw <button>. CreateSurfaceFooter replaces them and brings the keyboard contract to the nine surfaces that do not have it.",
    files: "create-issue · create-project · create-agent · create-crew",
    risk: "low",
  },
  {
    n: 4,
    title: "Turn on the phone layout",
    body: "One switch per surface once it is on the shell — the bottom sheet, the 44px targets and the safe-area padding come with CreateSurface rather than being written per page. Eleven of twelve gain a usable phone version.",
    files: "all twelve",
    risk: "low — the shell carries it",
  },
  {
    n: 5,
    title: "Colour the concepts",
    body: "concept-accents.ts plus <ConceptIcon> in the rail, the sub-bars and the surfaces. No new tokens: every accent is one globals.css already declares.",
    files: "app-sidebar.tsx · sub-bar.tsx · the twelve surfaces",
    risk: "low — reversible in one line",
  },
  {
    n: 6,
    title: "Retire border-white/[…] and the second type scale",
    body: "Mechanical: border-hairline for all ten alphas, .type-page-* folded into .type-*. Worth doing last, when there is one shell to check it against.",
    files: "~260 call sites",
    risk: "medium — wide, shallow",
  },
]

function Adoption() {
  return (
    <section className="space-y-3">
      <SectionHead icon={ListChecks} title="How it lands" hint="six changes, cheapest first">
        Nothing here changes what a surface does — same fields, same endpoints, same acceptance tests. That is
        what makes it safe to do in this order and stop after any step.
      </SectionHead>

      <div className="space-y-2">
        {PHASES.map((p) => (
          <div key={p.n} className="flex gap-3 rounded-lg border border-hairline bg-card p-3.5">
            <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full border border-primary/30 bg-primary/15 text-[11px] font-semibold tabular-nums text-primary-hover">
              {p.n}
            </span>
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-[13px] font-semibold text-foreground">{p.title}</span>
                <span
                  className={cn(
                    "rounded px-1.5 py-0.5 text-[10px] font-medium",
                    p.risk.startsWith("low") ? "bg-success/15 text-success" : "bg-warn/15 text-warn",
                  )}
                >
                  {p.risk}
                </span>
              </div>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{p.body}</p>
              <div className="mt-1.5 font-mono text-[10px] text-muted-foreground-soft">{p.files}</div>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}

/* ------------------------------------------------------------------ misc -- */

function SectionHead({
  icon: Icon,
  title,
  hint,
  children,
}: {
  icon: React.ComponentType<{ className?: string }>
  title: string
  hint?: string
  children?: React.ReactNode
}) {
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap items-baseline gap-2">
        <h3 className="flex items-center gap-2 text-base font-semibold text-foreground">
          <Icon className="h-4 w-4 text-muted-foreground" />
          {title}
        </h3>
        {hint && <span className="text-[11px] text-muted-foreground-soft">— {hint}</span>}
      </div>
      {children && <p className="max-w-[80ch] text-xs leading-relaxed text-muted-foreground">{children}</p>}
    </div>
  )
}
