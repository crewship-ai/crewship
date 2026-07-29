"use client"

import { useState } from "react"
import {
  AlertTriangle, Check, CircleDot, Clock, Gauge, ListTodo, Shield,
  ShieldCheck, UserCog, X,
} from "lucide-react"

import { DetailCard, Pill } from "@/components/ui/detail"
import { cn } from "@/lib/utils"

// =============================================================================
// /design — a live wireframe bench, not a product screen.
//
// Renders with the real kit and the real type roles, so whatever is picked here
// is already implementable. Unlinked from the sidebar; delete before merge.
//
// ── the question on the bench: the Keeper admin screens ─────────────────────
//
// Measured on dev3 (2026-07-29), not guessed:
//
//   crewship keeper status
//     Status:       enabled
//     Ollama URL:   http://127.0.0.1:11434
//     Model:        qwen2.5:7b
//     Ollama:       offline          ← the judge has no brain
//     Gov model:    — (server default)
//     Watchdog:     disabled
//   crewship keeper requests → (no results)   ← nothing ever evaluated
//
// The capability is all there and the CLI drives it fine: provider is
// ollama | anthropic | openai_compat, the endpoint (IP:port) and the API key
// both come from vault credentials, and with no judge the gatekeeper denies at
// risk 10 rather than failing open. Setting it to anthropic/claude-haiku-4-5
// worked first try.
//
// What is wrong is the screen. Today:
//
//   Admin → Keeper          status rows + 4 KPI cards + the whole 752-line
//                           governance form + recent requests + live activity
//   Admin → Keeper reviews  the pending-review queue, on its own
//
// Two nav entries for one subject, and on the first one the single most
// important fact — "the judge is offline, so everything denies" — is a grey
// status row between an Ollama URL you cannot edit there and a counter.
// =============================================================================

const JUDGE_OFFLINE = {
  provider: "ollama",
  model: "qwen2.5:7b",
  endpoint: "http://127.0.0.1:11434",
  reachable: false,
}

const JUDGE_ONLINE = {
  provider: "anthropic",
  model: "claude-haiku-4-5",
  endpoint: "vault: ANTHROPIC_API_KEY",
  reachable: true,
}

function Row({
  label, hint, children, tone,
}: { label: string; hint?: string; children: React.ReactNode; tone?: "warn" }) {
  return (
    <div className="grid min-h-[38px] grid-cols-1 items-center gap-3.5 border-b border-border px-3 py-1.5 last:border-b-0 md:grid-cols-[minmax(0,1fr)_248px]">
      <div className="min-w-0">
        <span className={cn("type-row block font-medium", tone === "warn" && "text-warn")}>{label}</span>
        {hint && <span className="type-meta mt-0.5 block leading-snug text-muted-foreground-soft">{hint}</span>}
      </div>
      <div className="flex min-w-0 items-center gap-2">{children}</div>
    </div>
  )
}

const Val = ({ children, mono }: { children: React.ReactNode; mono?: boolean }) => (
  <span className={cn("type-row truncate", mono && "font-mono")}>{children}</span>
)

/* ── band 1: can it judge at all ──────────────────────────────────────────── */
function CanItJudge({ judge }: { judge: typeof JUDGE_OFFLINE }) {
  return (
    <div className="space-y-3.5">
      {!judge.reachable && (
        <DetailCard tone="warn" className="bg-warn/[.06]">
          <div className="flex flex-wrap items-center gap-3">
            <AlertTriangle className="h-4 w-4 shrink-0 text-warn" />
            <div className="type-row min-w-0 flex-1 basis-72 leading-snug">
              <b className="font-semibold">The judge is unreachable, so the Keeper is denying everything.</b>{" "}
              {judge.model} at {judge.endpoint} did not answer. Access requests fall through to
              DENY at risk 10 — safe, but nothing is being evaluated.
              <span className="type-meta mt-0.5 block text-muted-foreground">
                This is the state dev3 has been in. It was a grey status row.
              </span>
            </div>
          </div>
        </DetailCard>
      )}

      <div className="grid gap-3.5 @xl:grid-cols-2">
        <DetailCard bare icon={Shield} title="Watchdog" subtitle="this workspace">
          <Row label="Enabled" hint="Off by default — opt in per workspace.">
            <Pill tone="default">off</Pill>
          </Row>
          <Row label="Security contact" hint="Must be an OWNER or ADMIN of this workspace.">
            <Val>— (MANAGER fanout)</Val>
          </Row>
          <Row label="Second approver" hint="A human must co-sign an ALLOW.">
            <Pill tone="default">off</Pill>
          </Row>
        </DetailCard>

        <DetailCard
          bare
          icon={judge.reachable ? ShieldCheck : AlertTriangle}
          title="The judge"
          subtitle={judge.reachable ? "reachable" : "unreachable"}
          tone={judge.reachable ? "success" : "warn"}
          footer="Endpoint and API key both come from vault credentials, so the IP:port of a local Ollama is a credential, not a config file."
        >
          <Row label="Provider">
            <Val>{judge.provider}</Val>
          </Row>
          <Row label="Model">
            <Val mono>{judge.model}</Val>
          </Row>
          <Row label="Endpoint" hint="ENDPOINT_URL credential, or the server default.">
            <Val mono>{judge.endpoint}</Val>
          </Row>
          <Row label="Answering" tone={judge.reachable ? undefined : "warn"}>
            {judge.reachable
              ? <Pill tone="success"><Check className="h-3 w-3" />yes</Pill>
              : <Pill tone="warn"><X className="h-3 w-3" />no</Pill>}
          </Row>
        </DetailCard>
      </div>
    </div>
  )
}

/* ── band 2: what it decides ──────────────────────────────────────────────── */
function WhatItDecides() {
  return (
    <div className="grid gap-3.5 @xl:grid-cols-2 @6xl:grid-cols-3">
      <DetailCard bare icon={Gauge} title="Thresholds">
        <Row label="Notify on DENY at risk ≥" hint="1–10.">
          <Val>7</Val>
        </Row>
        <Row label="Auto-issue leases" hint="Grants expire instead of standing forever.">
          <Pill tone="default">off</Pill>
        </Row>
        <Row label="Lease TTL">
          <Val>—</Val>
        </Row>
      </DetailCard>

      <DetailCard bare icon={ListTodo} title="Watch rules" subtitle="presets + custom">
        <Row label="Presets"><Val>3 of 7 on</Val></Row>
        <Row label="Custom rules"><Val>2</Val></Row>
        <Row label="Applies to"><Val>every agent in the workspace</Val></Row>
      </DetailCard>

      <DetailCard
        bare icon={UserCog} title="Who hears about it"
        footer="Verified in internal/api/keeper_governance.go: the contact must hold OWNER or ADMIN, and the MANAGER fanout stays as a fallback so a missing contact never means nobody."
      >
        <Row label="Security contact"><Val>— not set</Val></Row>
        <Row label="Falls back to"><Val>MANAGER fanout</Val></Row>
        <Row label="Delivered as"><Val>blocking inbox item</Val></Row>
      </DetailCard>
    </div>
  )
}

/* ── band 3: what it has decided ─────────────────────────────────────────── */
function WhatItDecided({ merged }: { merged: boolean }) {
  return (
    <div className="space-y-3.5">
      {merged && (
        <DetailCard
          bare icon={ListTodo} title="Waiting for a human" subtitle="0"
          footer="This is the whole of the old 'Keeper reviews' page. It is the only part of the subject that is ever actionable, so it belongs at the top of the evidence, not behind a second nav entry."
        >
          <p className="type-row px-4 py-6 text-center text-muted-foreground-soft">
            Nothing is waiting.
          </p>
        </DetailCard>
      )}

      <div className="grid gap-3.5 @xl:grid-cols-4">
        {[
          ["Requests", "0", "lifetime"],
          ["Allowed", "0", "decisions"],
          ["Denied", "0", "decisions"],
          ["Escalated", "0", "to a human"],
        ].map(([label, value, note]) => (
          <div key={label} className="rounded-xl border border-border/60 bg-card px-4 py-3">
            <div className="type-meta uppercase tracking-wide text-muted-foreground-soft">{label}</div>
            <div className="type-title mt-0.5 tabular-nums">{value}</div>
            <div className="type-meta text-muted-foreground-soft">{note}</div>
          </div>
        ))}
      </div>

      <DetailCard
        bare icon={CircleDot} title="Decision history" subtitle="0"
        footer="Append-only. Every row keeps the prompt and the raw model response, which is what makes a denial arguable after the fact."
      >
        <p className="type-row px-4 py-6 text-center text-muted-foreground-soft">
          Nothing has been evaluated on this instance yet.
        </p>
      </DetailCard>
    </div>
  )
}

function Band({ title, note, children }: { title: string; note: string; children: React.ReactNode }) {
  return (
    <section>
      <div className="mb-2 flex items-baseline gap-2">
        <h2 className="type-section text-foreground/70">{title}</h2>
        <span className="type-meta text-muted-foreground-soft">{note}</span>
      </div>
      {children}
    </section>
  )
}

export default function DesignBench() {
  const [online, setOnline] = useState(false)
  const judge = online ? JUDGE_ONLINE : JUDGE_OFFLINE

  return (
    <div className="@container min-h-screen space-y-6 px-6 py-6 md:px-8 lg:px-12">
      <div className="rounded-xl border border-warn/30 bg-warn/[.06] px-4 py-3">
        <p className="type-row text-warn">
          Wireframe bench — not a product screen. Delete this route before the branch merges.
        </p>
      </div>

      <div>
        <h1 className="type-title">Keeper — one screen instead of two</h1>
        <p className="type-row mt-2 max-w-3xl text-muted-foreground">
          Measured on dev3 rather than guessed. The capability is all there and the CLI drives it
          fine: provider is <b className="font-medium text-foreground">ollama | anthropic |
          openai_compat</b>, the endpoint (an IP:port) and the API key both come from vault
          credentials, and with no judge the gatekeeper denies at risk 10 rather than failing open.
          Pointing it at <code className="font-mono">anthropic / claude-haiku-4-5</code> worked
          first try.
        </p>
        <p className="type-row mt-2 max-w-3xl text-muted-foreground">
          What is wrong is the screen. <b className="font-medium text-foreground">Admin → Keeper</b> carries
          status rows, four counters, the entire 752-line governance form, recent requests and a
          live feed; <b className="font-medium text-foreground">Admin → Keeper reviews</b> carries the
          queue on its own. Two nav entries for one subject — and on the first one, the single most
          important fact, <i>the judge is offline so everything denies</i>, is a grey row between an
          Ollama URL you cannot edit there and a counter.
        </p>
        <p className="type-row mt-2 max-w-3xl text-muted-foreground">
          Three bands, same shape as the agent screen: can it judge · what it decides · what it has
          decided. The queue is the only actionable part, so it opens the evidence band instead of
          living behind a second nav entry.
        </p>
      </div>

      <button
        type="button"
        onClick={() => setOnline((v) => !v)}
        className="type-row rounded-lg border border-border px-3 py-1.5 text-muted-foreground transition-colors hover:border-foreground/25 hover:text-foreground"
      >
        {online ? "Show it as dev3 actually is (judge offline)" : "Show it with a reachable judge"}
      </button>

      <div className="space-y-5 rounded-xl border border-border/60 bg-background p-4">
        <Band title="Can it judge" note="is it on, and does the model answer">
          <CanItJudge judge={judge} />
        </Band>
        <Band title="What it decides" note="the rules it applies, and who hears about them">
          <WhatItDecides />
        </Band>
        <Band title="What it has decided" note="the queue, the counters, the audit trail">
          <WhatItDecided merged />
        </Band>
      </div>

      <div className="flex items-center gap-2">
        <Clock className="h-3.5 w-3.5 text-muted-foreground-soft" />
        <span className="type-meta text-muted-foreground-soft">
          Not implemented yet — this is the proposal. Nothing in the product has changed.
        </span>
      </div>
    </div>
  )
}
