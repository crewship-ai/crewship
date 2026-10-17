"use client"

// Cards of the routine page (routine-page.tsx): the six answers "in one
// look", what a routine can reach (Access) and its flat metadata. The page
// composes them into its tabs.

import * as React from "react"
import Link from "next/link"
import { ArrowUpRight, Eye, Globe, KeyRound, PenSquare, Puzzle, ShieldAlert } from "lucide-react"

import { cn } from "@/lib/utils"
import { relTime } from "@/lib/time"
import { DetailCard, EntityChip } from "@/components/ui/detail"
import type { PipelineRunRecord } from "@/hooks/use-pipeline-run-records"
import { crewshipActionsInDefinition } from "@/lib/automations"
import { integrationLabel } from "@/lib/integration-labels"
import { credentialTypeLabel } from "@/lib/credential-labels"
import { brandIconForType, BrandGlyph } from "./brand-icons"
import { routineInputSpecs } from "@/lib/routine-inputs"
import { routineEffects } from "@/lib/routine-effects"
import { isRecord, asString } from "@/lib/routine-step-describe"
import { describeTimeout, foreachBody, nameLookup, stepChips } from "@/lib/routine-steps-layout"
import { RoutineReachCard } from "./routine-reach-card"
import { YourAccess } from "@/components/features/access/your-access"
import type { RoutineDetail } from "./routines-detail-panel"

/* ------------------------------------------------------------------ *
 *  In one look                                                        *
 * ------------------------------------------------------------------ */

const listOf = (items: string[]) =>
  items.length <= 1 ? items.join("") : `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`

/** Six answers, every one derived from the definition — nothing invented. */
export function inOneLook(
  routine: Pick<RoutineDetail, "definition" | "description" | "manifest">,
  files: { path: string; language: string }[],
): { label: string; text: string }[] {
  const dsl = isRecord(routine.definition) ? routine.definition : {}
  const steps = Array.isArray(dsl.steps) ? dsl.steps.filter(isRecord) : []
  const all = [...steps, ...steps.flatMap(foreachBody)]
  const nameOf = nameLookup(dsl)
  const inputs = routineInputSpecs(dsl)
  const outputs = Array.isArray(dsl.outputs) ? dsl.outputs.filter(isRecord) : []

  const get = outputs.length
    ? outputs
        .map((o) => {
          const label = typeof o.label === "string" ? o.label : readableName(o.name || "Result")
          return typeof o.description === "string" && o.description ? `${label} — ${o.description}` : label
        })
        .join("; ") + "."
    : routine.description || "The results of the steps below — no result labels are declared."

  const provide = inputs.length
    ? inputs.map((i) => `${i.label || readableName(i.name)}${i.required ? "" : " (optional)"}`).join(", ")
    : "Nothing — Run starts immediately."

  const agentSlugs = [
    ...new Set([
      ...(routine.manifest?.agents ?? []),
      ...all.map((s) => asString(s.agent_slug)).filter(Boolean),
      ...all.map((s) => (isRecord(s.outcomes) ? asString(s.outcomes.grader_agent_slug) : "")).filter(Boolean),
    ]),
  ]
  const people = all.filter((s) => s.type === "wait" && (!isRecord(s.wait) || !asString(s.wait.kind) || asString(s.wait.kind) === "approval"))
  const scripts = Math.max(
    files.filter((f) => !["yaml", "json", "md"].includes(f.language)).length,
    all.filter((s) => s.type === "script").length,
  )
  const who: string[] = []
  if (agentSlugs.length) who.push(`${listOf(agentSlugs)} (${agentSlugs.length === 1 ? "agent" : "agents"})`)
  if (people.length) who.push(people.length === 1 ? "a person who decides" : `${people.length} decisions by people`)
  if (scripts) who.push(`${scripts} ${scripts === 1 ? "script" : "scripts"} on the crew share`)
  if (all.some((s) => s.type === "notify")) who.push("notifications")
  if (all.some((s) => s.type === "http")) who.push("external services")
  if (all.some((s) => s.type === "crewship")) who.push("Crewship pages and issues")
  if (all.some((s) => s.type === "call_pipeline")) who.push("another routine")
  const whoText = who.length ? who.join(", ") : "Local data preparation only."

  const checked = all
    .map((s) => ({ s, chips: stepChips(s, nameOf) }))
    .filter(({ chips }) => chips.checks)
    .map(({ s, chips }) =>
      chips.checks!.grader
        ? `${chips.checks!.count} ${chips.checks!.count === 1 ? "rule" : "rules"} by ${chips.checks!.grader} on “${nameOf(String(s.id))}”`
        : `${chips.checks!.count} ${chips.checks!.count === 1 ? "check" : "checks"} on “${nameOf(String(s.id))}”`,
    )
  const checkedText = checked.length ? checked.join("; ") + "." : "No checks declared — completion alone does not prove quality."

  const needs = people.map((s) => {
    const when = typeof s.if === "string" && s.if.trim() ? `Only when ${s.if.trim()}` : `At “${nameOf(String(s.id))}”`
    const timeout = typeof s.timeout_seconds === "number" && s.timeout_seconds > 0 ? ` · answer within ${describeTimeout(s.timeout_seconds)}` : ""
    return when + timeout
  })
  const needsText = needs.length ? needs.join("; ") : "Never — it runs without a decision."

  const effects = routineEffects(dsl)
  const crewship = crewshipActionsInDefinition(dsl)
  const touch: string[] = []
  if (effects.http) touch.push(`Calls ${effects.hosts.length ? listOf(effects.hosts) : "external services"}.`)
  if (crewship.length) touch.push(`Writes to Crewship (${crewship.join(", ")}).`)
  if (all.some((s) => s.type === "script" || s.type === "code")) touch.push("Runs scripts on the crew.")
  if (all.some((s) => s.type === "notify")) touch.push("Sends notifications.")
  if (effects.credentials.length) touch.push(`Uses ${listOf(effects.credentials)} from the crew vault.`)
  if (!touch.length) touch.push(effects.agents.length ? "Agents act with their own tools; no external writes are declared." : "No external writes declared.")
  touch.push("Stopping does not undo what already happened.")
  if (typeof dsl.max_cost_usd === "number" && dsl.max_cost_usd > 0) touch.push(`Cost cap $${dsl.max_cost_usd} per run.`)

  return [
    { label: "What you get", text: get },
    { label: "What you provide", text: provide },
    { label: "Who does the work", text: whoText },
    { label: "What is checked", text: checkedText },
    { label: "When it needs you", text: needsText },
    { label: "What it can touch", text: touch.join(" ") },
  ]
}

export function InOneLookCard({ routine, files }: { routine: RoutineDetail; files: { path: string; language: string }[] }) {
  const cells = React.useMemo(() => inOneLook(routine, files), [routine, files])
  return (
    <DetailCard title="In one look" icon={Eye} bare data-testid="routine-in-one-look">
      <dl className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3">
        {cells.map((cell, i) => (
          <div
            key={cell.label}
            data-testid={`routine-look-${cell.label.toLowerCase().replace(/[^a-z]+/g, "-")}`}
            className={cn(
              "border-hairline px-4 py-3",
              i % 3 !== 2 && "lg:border-r",
              i < 3 && "lg:border-b",
              i % 2 === 0 && "sm:border-r lg:border-r-0",
              i < 4 && "sm:border-b",
              i < 5 && "border-b sm:border-b-0",
              i === 2 && "lg:border-r-0",
              i === 3 && "lg:border-r",
            )}
          >
            <dt className="mb-1 text-[10.5px] font-semibold uppercase tracking-wider text-muted-foreground-soft">{cell.label}</dt>
            <dd className="text-[13px] leading-relaxed text-foreground/90">{cell.text}</dd>
          </div>
        ))}
      </dl>
    </DetailCard>
  )
}

/** One line under the Last run facts: what stopped it, or what it produced. */
export function lastRunReason(run: PipelineRunRecord | null, nameOf: (id: string) => string): string | undefined {
  if (!run) return undefined
  const failure = (run as PipelineRunRecord & { failure?: { step_name?: string; summary?: string } }).failure
  if (failure?.summary) return failure.step_name ? `Stopped at “${failure.step_name}”: ${failure.summary}` : failure.summary
  if (run.status === "failed" || run.outcome === "FAILED") {
    const step = run.failed_at_step ? `Stopped at “${nameOf(run.failed_at_step)}”` : "Could not finish"
    return run.error_message ? `${step}: ${run.error_message.split("\n")[0].slice(0, 160)}` : step
  }
  if (run.status === "waiting") return run.current_step_id ? `Waiting for a person at “${nameOf(run.current_step_id)}”.` : "Waiting for a person."
  if (run.status === "cancelled") return "Stopped. What already happened was not undone."
  if (run.output) return run.output.split("\n")[0].slice(0, 160)
  return undefined
}

/* ------------------------------------------------------------------ *
 *  Pieces                                                             *
 * ------------------------------------------------------------------ */

const readableName = (value: unknown) =>
  String(value ?? "")
    .replace(/[_-]+/g, " ")
    .replace(/^\w/, (c) => c.toUpperCase())

/**
 * Everything the routine can reach, as one row of chips.
 *
 * Four cards answering one question — "what could this do if it
 * misbehaved?" — meant nobody read the third. Real brand marks where
 * one exists: a chip reading "Gmail" beside a generic puzzle piece has
 * stopped carrying its own meaning.
 */
export function AccessCard({
  routine,
  crewshipActions,
  workspaceId,
  share,
}: {
  routine: RoutineDetail
  crewshipActions: string[]
  workspaceId: string
  /** The Share button, in the card's header. */
  share?: React.ReactNode
}) {
  const m = routine.manifest
  const integrations = [
    ...new Set(m?.integrations ?? routine.integrations_required ?? []),
  ]
  const credentials = m?.credentials ?? []
  const agentSlugs = [...new Set(m?.agents ?? [])]
  const hosts = [...new Set(m?.egress ?? [])]
  return (
    <DetailCard
      title="Access"
      subtitle="what this can reach"
      icon={ShieldAlert}
      tone="warn"
      action={share}
    >
      <div className="space-y-4">
        <YourAccess
          url={`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/pipelines/${encodeURIComponent(routine.slug)}/access/me`}
          actions={[{ key: "read", label: "Read" }, { key: "run", label: "Run" }, { key: "manage", label: "Manage" }]}
        />
        {agentSlugs.length > 0 && (
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">
              Agents and what they reach
            </h3>
            <RoutineReachCard bare workspaceId={workspaceId} agentSlugs={agentSlugs} />
          </div>
        )}
        {credentials.length > 0 && (
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">
              Required credentials{" "}
              <span className="font-normal">
                · accounts are resolved when the run starts
              </span>
            </h3>
            <div className="flex flex-wrap gap-1.5">
              {credentials.map((credential, index) => (
                <Link
                  key={`${credential.type}:${credential.scope}:${index}`}
                  href="/credentials"
                  title="Open Credentials to find a matching account"
                  className="inline-flex items-center gap-1.5 rounded-full border border-warn/20 bg-warn/10 px-2.5 py-1 text-xs text-warn hover:bg-warn/20"
                >
                  <KeyRound className="h-3.5 w-3.5" />
                  {credential.type === "AI_CLI_TOKEN"
                    ? "AI CLI token"
                    : credentialTypeLabel(credential.type)}
                  {credential.scope && (
                    <span className="text-muted-foreground">· {credential.scope}</span>
                  )}
                  <ArrowUpRight className="h-3 w-3" />
                </Link>
              ))}
            </div>
          </div>
        )}
        {integrations.length > 0 && (
          <div>
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">
              Integrations
            </h3>
            <div className="flex flex-wrap gap-1.5">
              {integrations.map((integration) => {
                const brand = brandIconForType(integration)
                return (
                  <EntityChip
                    key={integration}
                    href="/integrations"
                    icon={
                      brand
                        ? () => (
                            <BrandGlyph
                              brand={brand}
                              fallback={Puzzle}
                              className="h-3 w-3"
                            />
                          )
                        : Puzzle
                    }
                    label={integrationLabel(integration)}
                    tone="warn"
                  />
                )
              })}
            </div>
          </div>
        )}
        {hosts.length > 0 && (
          <details className="border-t border-border/60 pt-3">
            <summary className="flex cursor-pointer items-center gap-2 text-xs">
              <Globe className="h-3.5 w-3.5 text-warn" />
              Allowed network hosts{" "}
              <span className="text-muted-foreground">{hosts.length}</span>
            </summary>
            <ul className="mt-2 space-y-1 text-xs text-muted-foreground">
              {hosts.map((host) => (
                <li key={host} className="break-all">
                  {host}
                </li>
              ))}
            </ul>
            <p className="mt-2 text-[11px] text-muted-foreground">
              Declared network access; this is not a connection health check.
            </p>
          </details>
        )}
        {crewshipActions.length > 0 && (
          <div data-testid="routine-crewship-actions">
            <h3 className="mb-2 text-xs font-medium text-muted-foreground">
              Writes to Crewship
            </h3>
            <div className="flex flex-wrap gap-1.5">
              {crewshipActions.map((action) => (
                <EntityChip key={action} icon={PenSquare} label={action} tone="warn" />
              ))}
            </div>
          </div>
        )}
        {!integrations.length && !credentials.length && !hosts.length && (
          <p className="text-xs text-muted-foreground">
            No external integrations, credentials or network hosts declared.
          </p>
        )}
      </div>
    </DetailCard>
  )
}

/**
 * The flat facts.
 *
 * Low weight on their own, which is why they sit inside the Versions
 * card rather than taking one of their own — but paired with the version
 * history they answer "what changed since the run that worked", which is
 * the question you have precisely when something has broken.
 */
export function Metadata({ routine, steps }: { routine: RoutineDetail; steps: number }) {
  const rows: [string, string][] = [
    ["DSL version", routine.dsl_version],
    ["Visibility", routine.workspace_visible ? "workspace" : "private"],
    ["Hash", routine.definition_hash ? `${routine.definition_hash.slice(0, 10)}…` : "—"],
    ["Steps", String(steps)],
    ["Created", routine.created_at ? relTime(routine.created_at) : "—"],
    ["Updated", routine.updated_at ? relTime(routine.updated_at) : "—"],
  ]
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-2.5 text-[11px]">
      {rows.map(([k, v]) => (
        <div key={k}>
          <dt className="text-[10px] uppercase tracking-wider text-muted-foreground-soft">
            {k}
          </dt>
          <dd className="mt-0.5 truncate font-mono text-foreground/85">{v}</dd>
        </div>
      ))}
    </dl>
  )
}
