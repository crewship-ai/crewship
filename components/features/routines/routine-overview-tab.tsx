"use client"

import type { RoutineDetail } from "./routines-detail-panel"
import { Badge } from "@/components/ui/badge"

// RoutineOverviewTab — read-only meta inspector. Mirrors what
// orchestration's pipeline-detail-sheet showed under Overview, expanded
// with declared egress / credentials / inputs blocks pulled from the
// routine's DSL (when available).

export function RoutineOverviewTab({ routine }: { routine: RoutineDetail }) {
  const def = routine.definition as Record<string, unknown> | undefined
  const inputs = (def?.["inputs"] as Array<Record<string, unknown>>) ?? []
  const outputs = (def?.["outputs"] as Array<Record<string, unknown>>) ?? []
  const egress = (def?.["egress_targets"] as string[]) ?? []
  const creds = (def?.["credentials_required"] as Array<Record<string, unknown>>) ?? []
  const tier = def?.["execution_tier"] as Record<string, unknown> | undefined
  const steps = (def?.["steps"] as Array<Record<string, unknown>>) ?? []

  return (
    <div className="space-y-4 text-xs">
      {/* Description */}
      {routine.description && (
        <p className="text-foreground/90">{routine.description}</p>
      )}

      {/* Metadata grid */}
      <Section title="Identity">
        <Row label="Slug" value={routine.slug} mono />
        <Row label="DSL version" value={routine.dsl_version} />
        <Row label="Definition hash" value={routine.definition_hash.slice(0, 16) + "…"} mono />
        <Row label="Visibility" value={routine.workspace_visible ? "workspace-visible" : "private"} />
        {routine.ephemeral && <Row label="Type" value="ephemeral (auto-generated)" />}
      </Section>

      <Section title="Authorship">
        <Row label="Authored via" value={routine.authored_via.replace(/_/g, " ")} />
        <Row label="Author crew" value={routine.author_crew_id || "—"} mono />
        <Row label="Author agent" value={routine.author_agent_id || "—"} mono />
        <Row label="Created" value={new Date(routine.created_at).toLocaleString()} />
        <Row label="Updated" value={new Date(routine.updated_at).toLocaleString()} />
      </Section>

      <Section title="Activity">
        <Row label="Total invocations" value={String(routine.invocation_count)} />
        {routine.last_invoked_at && (
          <Row
            label="Last invoked"
            value={`${new Date(routine.last_invoked_at).toLocaleString()}${
              routine.last_invocation_status ? ` (${routine.last_invocation_status})` : ""
            }`}
          />
        )}
        <Row label="Step count" value={String(steps.length)} />
      </Section>

      {tier && (
        <Section title="Execution tier">
          {tier["preferred"] != null && <Row label="Preferred" value={String(tier["preferred"])} />}
          {Array.isArray(tier["fallback"]) && (tier["fallback"] as unknown[]).length > 0 && (
            <Row label="Fallback chain" value={(tier["fallback"] as string[]).join(" → ")} />
          )}
        </Section>
      )}

      {inputs.length > 0 && (
        <Section title={`Inputs (${inputs.length})`}>
          <ul className="space-y-1">
            {inputs.map((inp, i) => (
              <li key={i} className="rounded bg-muted/30 px-2 py-1 font-mono">
                <span className="text-blue-300">{String(inp["name"])}</span>
                <span className="text-muted-foreground"> · {String(inp["type"])}</span>
                {inp["required"] === true && (
                  <Badge variant="outline" className="ml-1.5 px-1 py-0 text-[9px]">required</Badge>
                )}
                {"default" in inp && (
                  <span className="ml-1.5 text-muted-foreground">= {JSON.stringify(inp["default"])}</span>
                )}
                {typeof inp["description"] === "string" && (
                  <p className="mt-0.5 text-[11px] font-sans text-muted-foreground/80">
                    {String(inp["description"])}
                  </p>
                )}
              </li>
            ))}
          </ul>
        </Section>
      )}

      {outputs.length > 0 && (
        <Section title={`Outputs (${outputs.length})`}>
          <ul className="space-y-1">
            {outputs.map((out, i) => (
              <li key={i} className="rounded bg-muted/30 px-2 py-1 font-mono">
                <span className="text-emerald-300">{String(out["name"])}</span>
                <span className="text-muted-foreground"> · {String(out["type"])}</span>
              </li>
            ))}
          </ul>
        </Section>
      )}

      {egress.length > 0 && (
        <Section title="Declared egress">
          <div className="flex flex-wrap gap-1.5">
            {egress.map((host) => (
              <Badge key={host} variant="outline" className="font-mono text-[10px]">
                {host}
              </Badge>
            ))}
          </div>
        </Section>
      )}

      {creds.length > 0 && (
        <Section title="Credentials required">
          <ul className="space-y-1">
            {creds.map((c, i) => (
              <li key={i} className="rounded bg-muted/30 px-2 py-1 font-mono">
                <span className="text-amber-300">{String(c["type"])}</span>
                {typeof c["scope"] === "string" && (
                  <span className="ml-1.5 text-muted-foreground">scope: {String(c["scope"])}</span>
                )}
              </li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  )
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div>
      <h3 className="mb-1.5 text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
        {title}
      </h3>
      <div className="space-y-1">{children}</div>
    </div>
  )
}

function Row({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex items-baseline gap-3">
      <span className="w-36 shrink-0 text-muted-foreground">{label}</span>
      <span className={mono ? "font-mono break-all" : ""}>{value}</span>
    </div>
  )
}
