import { createElement } from "react"
import { IncomingTargetAvatar } from "./incoming-target-avatar"
import { ArrowDownToLine, Bot, FileText, Workflow } from "lucide-react"
import type { ExplorerItem, ExplorerSection } from "./explorer"
import type { PipelineWebhook } from "@/hooks/use-pipeline-webhooks"

export type IncomingKind = "routine" | "agent" | "page"
export type IncomingSection = "endpoints" | IncomingKind
export interface IncomingTarget {
  id: string
  slug: string
  name: string
  kind: IncomingKind
  avatar_seed?: string | null
  avatar_style?: string | null
  avatar_url?: string | null
  crew?: { avatar_style?: string | null } | null
  icon?: string | null
  color?: string | null
  crew_id?: string
  webhook_secret_set?: boolean
}
export interface IncomingEndpoint {
  id: string
  name: string
  target: IncomingTarget
  sender: string
  signed: boolean
  enabled: boolean
  fired?: number
  last?: string
  path: string
  routine?: PipelineWebhook
}
export const INCOMING_KINDS = [
  {
    key: "routine" as const,
    label: "Routines",
    icon: Workflow,
    hint: "Run a routine, including issue.create, issue.update or issue.comment steps.",
  },
  {
    key: "agent" as const,
    label: "Agents",
    icon: Bot,
    hint: "Start an agent background run; this does not append to an existing chat.",
  },
  {
    key: "page" as const,
    label: "Pages",
    icon: FileText,
    hint: "Write a Page panel. Endpoints are listed per Page, not in workspace totals.",
  },
]
export function incomingSection(value: string | null): IncomingSection {
  return value === "routine" || value === "agent" || value === "page"
    ? value
    : "endpoints"
}
export function incomingRows(
  targets: IncomingTarget[],
  hooks: PipelineWebhook[],
): IncomingEndpoint[] {
  const rows: IncomingEndpoint[] = hooks.map((h) => ({
    id: h.id,
    name: h.name,
    target: targets.find(
      (t) => t.kind === "routine" && t.id === h.target_pipeline_id,
    ) ?? {
      id: h.target_pipeline_id,
      slug: h.target_pipeline_slug ?? h.target_pipeline_id,
      name: h.target_pipeline_slug ?? h.target_pipeline_id,
      kind: "routine",
    },
    sender:
      h.ingress_profile === "github"
        ? "GitHub"
        : h.ingress_profile === "unsigned"
          ? "Secret URL"
          : "Crewship",
    signed: h.signing_secret_set,
    enabled: h.enabled,
    fired: h.fire_count,
    last: h.last_fired_at,
    path: `/api/v1/webhooks/•••••${h.ingress_profile === "github" ? "/github-pull-request" : ""}`,
    routine: h,
  }))
  for (const t of targets.filter(
    (t) => t.kind === "agent" && t.webhook_secret_set && !!t.crew_id,
  ))
    rows.push({
      id: t.id,
      name: t.name,
      target: t,
      sender: "Crewship",
      signed: true,
      enabled: !!t.crew_id,
      path: `/api/v1/webhooks/${encodeURIComponent(t.crew_id ?? "")}/${encodeURIComponent(t.id)}/trigger`,
    })
  return rows
}
export function incomingExplorer(
  targets: IncomingTarget[],
  rows: IncomingEndpoint[],
  section: IncomingSection,
  search: string,
) {
  const sections: ExplorerSection<IncomingSection>[] = [
    {
      key: "endpoints",
      label: "Endpoints",
      icon: ArrowDownToLine,
      count: rows.length,
      hint: "Known routine and agent endpoints; Pages are listed per Page.",
    },
    ...INCOMING_KINDS.map((k) => ({
      ...k,
      count:
        k.key === "page"
          ? "per Page"
          : new Set(
              rows
                .filter((r) => r.target.kind === k.key)
                .map((r) => r.target.id),
            ).size,
    })),
  ]
  const items: ExplorerItem[] = targets
    .filter(
      (t) =>
        (section === "endpoints" || t.kind === section) &&
        `${t.name} ${t.slug}`.toLowerCase().includes(search.toLowerCase()),
    )
    .map((t) => ({
      id: `${t.kind}:${t.id}`,
      label: t.name,
      leading: createElement(IncomingTargetAvatar, { target: t }),
      sublabel: `${t.kind} · ${t.slug}`,
      dot: rows.some(
        (r) => r.target.id === t.id && r.target.kind === t.kind && r.enabled,
      )
        ? "bg-success"
        : "bg-muted-foreground/40",
    }))
  return { sections, items }
}

// Accept old slug links, but canonicalize selection to the persistent row ID.
export function resolveIncomingTarget(targets: IncomingTarget[], kind: IncomingSection, value: string | null) {
  return targets.find(t => t.kind === kind && t.id === value)
    ?? targets.find(t => t.kind === kind && t.slug === value)
}
