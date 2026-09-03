/**
 * What a crew needs from a person, as rows with a verb (README §1 item 1,
 * audit-fleet.md §6 P1 2).
 *
 * The API already knew every one of these — a rebuild pending, a credential
 * whose CLI is not in the image, an MCP server with auth_status "missing",
 * an agent in error — and the crew canvas showed none of them, or showed the
 * name without the gap. One derivation, one strip, each row carrying the one
 * action that resolves it: Build now · Install · Connect · Inspect · Review.
 */
import type { StatusTone } from "@/lib/format-status"
import { entityHref } from "@/lib/entity-links"
import type { ProvisioningState } from "./explorer-groups"

export interface NeedAgent {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  expired_at?: string | null
}

export interface NeedGap {
  credential_id: string
  credential_name: string
  /** The CLI binary the credential is read by, e.g. "gh". */
  tool: string
  /** Devcontainer feature ref that installs it, and its short id. */
  feature: string
  feature_id: string
}

export interface NeedIntegration {
  id: string
  name: string
  display_name: string
  transport: string
  auth_status: string
  agent_binding_count: number
}

export type NeedAction =
  | { kind: "build"; label: string }
  | { kind: "install"; label: string; feature: string; featureId: string; tool: string }
  | { kind: "link"; label: string; href: string }

export interface CrewNeed {
  id: string
  tone: Extract<StatusTone, "danger" | "warn">
  icon: "build" | "credential" | "integration" | "agent" | "decision"
  title: string
  detail: string
  action: NeedAction
}

const WAITING = new Set(["PENDING_REVIEW", "WAITING", "PAUSED", "AWAITING_APPROVAL"])

export function deriveCrewNeeds({
  crewSlug,
  agents,
  provisioning,
  provisioningError,
  gaps,
  integrations,
}: {
  crewSlug: string
  agents: NeedAgent[]
  provisioning?: ProvisioningState
  provisioningError?: string
  gaps: NeedGap[]
  integrations: NeedIntegration[]
}): CrewNeed[] {
  const danger: CrewNeed[] = []
  const warn: CrewNeed[] = []
  const live = agents.filter((a) => !a.expired_at)

  for (const a of live) {
    if (a.status === "ERROR") {
      danger.push({
        id: `agent-error:${a.id}`,
        tone: "danger",
        icon: "agent",
        title: `${a.name} is in error`,
        detail: a.role_title ? `${a.role_title} · its last run did not finish.` : "Its last run did not finish.",
        action: { kind: "link", label: "Inspect", href: entityHref({ kind: "agent", slug: a.slug }) },
      })
    }
  }

  if (provisioning === "failed") {
    danger.push({
      id: "build-failed",
      tone: "danger",
      icon: "build",
      title: "Last container build failed",
      detail: provisioningError?.trim() || "Fix the runtime config in Settings and try again.",
      action: { kind: "build", label: "Retry build" },
    })
  } else if (provisioning === "needs_provision") {
    warn.push({
      id: "needs-provision",
      tone: "warn",
      icon: "build",
      title: "Container image needs rebuild",
      detail: "Runtime config changed — agents in this crew cannot start until the image is rebuilt.",
      action: { kind: "build", label: "Build now" },
    })
  }

  // One row per missing tool, naming every credential that waits on it.
  const byTool = new Map<string, NeedGap[]>()
  for (const g of gaps) {
    const list = byTool.get(g.tool) ?? []
    list.push(g)
    byTool.set(g.tool, list)
  }
  for (const [tool, list] of byTool) {
    const names = list.map((g) => g.credential_name).join(", ")
    warn.push({
      id: `gap:${tool}`,
      tone: "warn",
      icon: "credential",
      title: `${tool} is missing from the image`,
      detail: `${names} ${list.length === 1 ? "is" : "are"} bound to this crew, but nothing in it can read ${list.length === 1 ? "it" : "them"} without ${tool}.`,
      action: { kind: "install", label: `Install ${tool}`, feature: list[0].feature, featureId: list[0].feature_id, tool },
    })
  }

  for (const i of integrations) {
    if (i.auth_status !== "missing" && i.auth_status !== "expired") continue
    const who = i.agent_binding_count === 1 ? "1 agent" : `${i.agent_binding_count} agents`
    warn.push({
      id: `integration:${i.id}`,
      tone: i.auth_status === "expired" ? "danger" : "warn",
      icon: "integration",
      title: i.auth_status === "expired" ? `${i.display_name || i.name}'s credential expired` : `${i.display_name || i.name} has no credential`,
      detail: `MCP server bound to ${who} · ${i.transport}.`,
      action: {
        kind: "link",
        label: i.auth_status === "expired" ? "Reconnect" : "Connect",
        href: entityHref({ kind: "integrations", tab: "tools", section: "crew-tools", server: i.id }),
      },
    })
  }

  for (const a of live) {
    if (WAITING.has(a.status)) {
      warn.push({
        id: `waiting:${a.id}`,
        tone: "warn",
        icon: "decision",
        title: `${a.name} is waiting on your decision`,
        detail: "Until you decide, the agent is stopped on that step.",
        action: { kind: "link", label: "Review", href: entityHref({ kind: "inbox", agentSlug: a.slug }) },
      })
    }
  }

  const out = [...danger, ...warn]
  // Expired integrations rank with the danger rows.
  return out.sort((a, b) => (a.tone === b.tone ? 0 : a.tone === "danger" ? -1 : 1))
    .map((n) => ({ ...n, id: `${crewSlug}:${n.id}` }))
}

/** Add a devcontainer feature to a crew's config JSON, keeping everything
 *  else as it was. Returns the new JSON text. */
export function withDevcontainerFeature(configJSON: string | null, feature: string): string {
  let raw: Record<string, unknown> = {}
  if (configJSON && configJSON.trim()) {
    try {
      const parsed: unknown = JSON.parse(configJSON)
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) raw = parsed as Record<string, unknown>
    } catch {
      // Unparseable config: start from the features alone rather than
      // refusing — the rebuild that follows validates the result.
      raw = {}
    }
  }
  const features = raw.features && typeof raw.features === "object" && !Array.isArray(raw.features)
    ? { ...(raw.features as Record<string, unknown>) }
    : {}
  if (!(feature in features)) features[feature] = {}
  return JSON.stringify({ ...raw, features }, null, 2)
}
