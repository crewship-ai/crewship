"use client"

import Link from "next/link"

import * as React from "react"
import { motion } from "motion/react"
import {
  Activity,
  ChevronLeft,
  Users,
  FlaskConical,
  RefreshCw,
  AlertTriangle,
  Trash2,
  CheckCircle2,
  XCircle,
  Clock,
  Pencil,
  Eye,
  EyeOff,
  Boxes,
  Cpu,
  Hash,
  Info,
  KeyRound,
  Layers,
  ListTree,
  PackageX,
  Plug,
  ShieldCheck,
  TerminalSquare,
  UserCircle2,
} from "lucide-react"
import { toast } from "sonner"
import { Spinner } from "@/components/ui/spinner"

import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { formatDate, formatRelativeTime } from "@/lib/time"
import {
  Appear,
  DetailCard,
  FieldLabel,
  Pill,
  StatStrip,
  type DetailTone,
  type StatItem,
} from "@/components/ui/detail"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { getBrand, brandColor } from "@/lib/credential-providers/registry"
import { credentialTypeLabel } from "@/lib/credentials/item-types"
import { EXPIRY_WARNING_DAYS, daysUntilExpiry } from "@/lib/credentials/facets"
import { tierMeta, tierOf, type CredentialTierLevel } from "@/lib/credentials/tiers"
import type { CredentialCrewRef, CredentialToolGap } from "@/hooks/use-credential-readiness"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Capability } from "@/lib/capabilities"
import { useAbilities } from "@/hooks/use-abilities"
import { cn } from "@/lib/utils"
import { entityHref } from "@/lib/entity-links"
import { apiFetch } from "@/lib/api-fetch"
import { RevealDialog } from "./reveal-dialog"

interface CredentialSummary {
  id: string
  name: string
  description: string | null
  type: string
  provider: string
  status: string
  scope: string
  account_label: string | null
  account_email: string | null
  // username is the cleartext half of USERPASS credentials. null for
  // every other type — the Overview tab renders a Username row only
  // when this is populated, so it stays out of the way for the other
  // 8 types that don't have one.
  username: string | null
  token_expires_at: string | null
  last_checked_at: string | null
  last_used_at: string | null
  last_used_ips: string[]
  last_error: string | null
  tags: string[]
  created_at: string
  updated_at: string
  agent_names: string[]
  /** Same agents as `agent_names`, in the same order — the server builds both
   *  in one pass (splitAgentRefs). Optional so an older payload still decodes;
   *  absent falls the avatar back to seeding from the name. */
  agent_ids?: string[]
  _count_agent_credentials: number
  mcp_used: boolean
  /** Server-declared: does Crewship maintain a real upstream probe for this
   *  credential's (provider, type)? Gates the "Test now" action so it is never
   *  a placebo. Optional so older payloads decode; absent reads as "no probe",
   *  which hides the button rather than offering one that cannot answer. */
  testable?: boolean
  /** Crews this credential is linked to — used to find the agents that
   *  inherit it through a crew grant rather than an explicit assignment. */
  crew_ids?: string[]
  /**
   * Classification (STANDARD / RESTRICTED / SEALED).
   *
   * GET /api/v1/credentials does NOT return this today — `credentialResponse`
   * in internal/api/credentials.go now carries `sensitivity`, so this is
   * normally
   * undefined, and every consumer below has to treat "unknown" as its own
   * state rather than as STANDARD. The moment the field is added to the read
   * payload, the gating here starts working with no other change.
   */
  sensitivity?: string | null
  /** Keeper tier, 1–4 — see lib/credentials/tiers.ts. Absent on an older
   *  server, which the badge renders as unclassified rather than as L1. */
  security_level?: number
  security_level_label?: string
}

interface AuditEvent {
  id: string
  event_type: string
  agent_id: string | null
  ip_address: string | null
  metadata: Record<string, unknown> | null
  occurred_at: string
  /** Who did it — "agent", "user" or "system". Resolved server-side (see
   *  resolveAuditActor) because the actor was recorded in two shapes and, in
   *  metadata, under five different keys. Optional so an older server's
   *  timeline still renders, unattributed. */
  actor_kind?: "agent" | "user" | "crew" | "system"
  actor_id?: string
  actor_name?: string
}

interface RotationRow {
  id: string
  credential_id: string
  grace_seconds: number
  rotated_at: string
  expires_at: string
  rotated_by: string
  status: "ACTIVE" | "EXPIRED" | "CANCELLED"
  old_value_gone: boolean
}

/** GET /api/v1/credentials/{id}/fields. `value` is non-null ONLY for a
 *  non-secret field — the server never returns a secret's bytes, in any form. */
interface CredentialFieldRow {
  key: string
  is_secret: boolean
  ordinal: number
  value: string | null
  created_at: string
  updated_at: string
}

/** GET /api/v1/credentials/bindings?credential_id= */
interface BindingRow {
  id: string
  credential_id: string
  credential_name: string
  scope: string
  crew_id: string | null
  agent_id: string | null
  slot: string
  created_at: string
}

/** One row of GET /api/v1/agents/{id}/credentials, narrowed to this credential. */
interface AssignmentRow {
  agentName: string
  /** For the link to the agent's canvas; null on a row the list did not carry. */
  agentSlug: string | null
  envVarName: string
  /** "explicit" (an agent_credentials row) or "crew" (inherited via the crew). */
  grantSource: string
  expiresAt?: string
  expired: boolean
}

/** Reveal classifications, weakest first — the order is the rank comparison
 *  that decides whether a change is a raise (MANAGER+) or a lower (admin). */
const SENSITIVITY_LEVELS = ["STANDARD", "RESTRICTED", "SEALED"] as const

/**
 * How much of the timeline is on screen before you ask for more.
 *
 * A busy credential is read every couple of minutes, so fifty events is fifty
 * rows of "USE · 3m ago" — a card three screens tall that pushes everything
 * below it out of reach and answers nothing the first row did not. Ten is the
 * shape of the question people actually ask ("what happened recently?"), and
 * the header says how many there are so the ten never reads as all of them.
 */
const AUDIT_PREVIEW = 10

/** What we ask the server for. The header appends "+" at exactly this number,
 *  because a full page means the timeline is longer than we fetched. */
const AUDIT_FETCH_LIMIT = 50

/** How many agents we will interrogate for grant provenance. See loadAssignments. */
const MAX_ASSIGNMENT_LOOKUPS = 12

export interface CredentialDetailSheetProps {
  workspaceId: string
  credential: CredentialSummary | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onRefresh: () => void
  onRotate: (cred: CredentialSummary) => void
  /** Optional handler — opens the full Edit dialog. When omitted the
   *  Edit button is hidden (legacy callers). */
  onEdit?: (cred: CredentialSummary) => void
  /** Returns to the list. Renders the back breadcrumb when supplied. */
  onBack?: () => void
  /** Crews that can use this credential but lack the CLI that reads it.
   *  Comes from the page's readiness hook — see ReadinessCard. */
  toolGaps?: CredentialToolGap[]
  /** False when no crew answered the readiness endpoint. "No gap" and "nobody
   *  looked" must never render the same. */
  readinessKnown?: boolean
  /** crew id → name + icon + colour. A slot bound to a crew printed the raw
   *  cuid before this: an identifier nobody recognises, in the one place the
   *  page is meant to say WHO can read the secret. */
  crewsById?: Record<string, CredentialCrewRef>
}

export function CredentialDetailSheet({
  workspaceId, credential, open, onOpenChange, onRefresh, onRotate, onEdit, onBack,
  toolGaps = [], readinessKnown = false, crewsById = {},
}: CredentialDetailSheetProps) {
  const [audit, setAudit] = React.useState<AuditEvent[]>([])
  const [auditLoading, setAuditLoading] = React.useState(false)
  const [auditExpanded, setAuditExpanded] = React.useState(false)
  const [rotations, setRotations] = React.useState<RotationRow[]>([])
  const [confirmDelete, setConfirmDelete] = React.useState(false)
  const [testing, setTesting] = React.useState(false)
  const [testResult, setTestResult] = React.useState<{ valid: boolean; error?: string } | null>(null)
  const [fields, setFields] = React.useState<CredentialFieldRow[]>([])
  const [fieldsLoading, setFieldsLoading] = React.useState(false)
  const [bindings, setBindings] = React.useState<BindingRow[]>([])
  const [assignments, setAssignments] = React.useState<AssignmentRow[]>([])
  const [revealEnabled, setRevealEnabled] = React.useState(false)
  const [revealOpen, setRevealOpen] = React.useState(false)
  // The classification a PUT .../sensitivity last returned. Starts unset and
  // takes precedence over the value the list/get payload carried, so the pill
  // and the SEALED gate follow a change made in this sheet without a refetch.
  const [sensitivity, setSensitivity] = React.useState<string | null>(null)
  const [sensitivitySaving, setSensitivitySaving] = React.useState(false)
  const [sensitivityError, setSensitivityError] = React.useState<string | null>(null)

  // Hide affordances users can't perform rather than letting them
  // click through to a 403. Mirrors the backend gating exactly:
  //   * Test + value update (PATCH)  → MANAGER+  → CASL "update"
  //   * Rotate w/ grace overlap      → OWNER/ADMIN via role OR any
  //     member holding the credential.rotate capability
  //     (requireRoleOrCapabilityOrForbid in credential_rotation.go,
  //     #1028) → CASL "manage" OR hasCapability
  //   * Delete                       → OWNER/ADMIN (credentials.go)
  //     → CASL "delete"
  // MANAGER has update but neither manage nor delete, so they keep
  // the value-update flow — and see Rotate only when explicitly
  // granted credential.rotate (#1034).
  const { abilities, hasCapability } = useAbilities()
  const canUpdate = abilities.can("update", "Credential")
  const canRotate = abilities.can("manage", "Credential") || hasCapability(Capability.CredentialRotate)
  const canDelete = abilities.can("delete", "Credential")
  // Lowering a classification is OWNER/ADMIN (credentials_reveal.go
  // SetSensitivity: the lower branch re-checks with "manage"); raising is
  // MANAGER+. Two gates because the server has two.
  const canLowerSensitivity = abilities.can("manage", "Credential")

  const effectiveSensitivity = sensitivity ?? credential?.sensitivity ?? null

  /**
   * Reveal, gated exactly the way credentials_reveal.go gates it:
   *
   *   L1 the workspace switch  → GET /credentials/reveal-policy
   *   L2 role floor MANAGER+   → CASL "update" (revealRoleFloor = "update")
   *   L2 the capability        → credentials:reveal, which no role implies
   *   L0 classification        → SEALED never, by anyone
   *
   * All four, not any of them. The capability is the one people expect to be
   * implied by being an OWNER and is deliberately not — so an OWNER without
   * it must not see this button.
   */
  const canReveal =
    canUpdate &&
    hasCapability(Capability.CredentialReveal) &&
    revealEnabled &&
    effectiveSensitivity !== "SEALED"

  /**
   * Which of the four gates is shut, in the order they bind.
   *
   * Only meaningful for a reader who is otherwise allowed to act — below the
   * MANAGER floor there is nothing to explain, because reveal is not a thing
   * that tier does. SEALED is checked first because it is the one no
   * configuration can open: the answer there is rotation, not a setting.
   */
  const revealBlockedReason =
    effectiveSensitivity === "SEALED"
      ? "SEALED can never be revealed, by any role. The break-glass is rotation, not disclosure."
      : !revealEnabled
        ? "Reveal is switched off for this workspace. An owner can turn it on under Settings → Access & secrets."
        : "Revealing a value needs the credentials:reveal capability, which no role grants on its own."

  React.useEffect(() => {
    if (!open || !credential) {
      setAudit([])
      setAuditExpanded(false)
      setRotations([])
      setTestResult(null)
      setFields([])
      setBindings([])
      setAssignments([])
      setSensitivity(null)
      setSensitivityError(null)
      setRevealEnabled(false)
      setRevealOpen(false)
    }
  }, [open, credential])

  // The workspace reveal switch. MANAGER+ may read it (GetPolicy's own gate),
  // so anyone below "update" is never asked — a 403 here would be read as
  // "disabled", which happens to be right, but asking is still noise.
  React.useEffect(() => {
    if (!open || !credential || !canUpdate) return
    let cancelled = false
    apiFetch(`/api/v1/credentials/reveal-policy?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((body: { enabled?: boolean } | null) => {
        if (!cancelled) setRevealEnabled(Boolean(body?.enabled))
      })
      .catch(() => {
        // Unreachable server → treat reveal as off. Failing closed on a
        // disclosure control is the only acceptable direction.
        if (!cancelled) setRevealEnabled(false)
      })
    return () => { cancelled = true }
  }, [open, credential, canUpdate, workspaceId])

  // Every sub-resource, on open.
  //
  // These used to be gated on which tab was showing, which is what tabs are
  // for: five panes, five fetches, one at a time. The page has no tabs any
  // more — audit, fields, slots, agents and rotations are all on it at once —
  // so they all load at once. Two are still gated, on PERMISSION rather than
  // on layout: the audit log is MANAGER+ and rotation history is only rendered
  // for someone who can rotate, and asking for either without the role is a
  // 403 in the console for a section that will not appear.
  React.useEffect(() => {
    if (!open || !credential) return
    const cid = credential.id
    const ws = encodeURIComponent(workspaceId)
    // Switching credentials in the rail fires five requests deep, so a response
    // for the secret you have already left arriving after the one you are
    // looking at is the ordinary case, not an exotic one. Without this you read
    // one credential's audit under another's name.
    let cancelled = false

    if (canUpdate) {
      setAuditLoading(true)
      apiFetch(`/api/v1/credentials/${cid}/audit?workspace_id=${ws}&limit=${AUDIT_FETCH_LIMIT}`)
        .then((r) => (r.ok ? r.json() : []))
        .then((data: AuditEvent[]) => !cancelled && setAudit(Array.isArray(data) ? data : []))
        .catch(() => !cancelled && setAudit([]))
        .finally(() => !cancelled && setAuditLoading(false))
    }

    setFieldsLoading(true)
    apiFetch(`/api/v1/credentials/${cid}/fields?workspace_id=${ws}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((data: CredentialFieldRow[]) => !cancelled && setFields(Array.isArray(data) ? data : []))
      .catch(() => !cancelled && setFields([]))
      .finally(() => !cancelled && setFieldsLoading(false))

    apiFetch(`/api/v1/credentials/bindings?workspace_id=${ws}&credential_id=${cid}`)
      .then((r) => (r.ok ? r.json() : null))
      .then((body: { bindings?: BindingRow[] } | null) =>
        !cancelled && setBindings(Array.isArray(body?.bindings) ? body.bindings : []))
      .catch(() => !cancelled && setBindings([]))

    void loadAssignments(workspaceId, credential)
      .then((rows) => !cancelled && setAssignments(rows))
      .catch(() => !cancelled && setAssignments([]))

    if (canRotate) {
      apiFetch(`/api/v1/credentials/${cid}/rotations?workspace_id=${ws}`)
        .then((r) => (r.ok ? r.json() : []))
        .then((data: RotationRow[]) => !cancelled && setRotations(Array.isArray(data) ? data : []))
        .catch(() => !cancelled && setRotations([]))
    }

    return () => {
      cancelled = true
    }
  }, [open, credential, workspaceId, canRotate, canUpdate])

  if (!credential) return null

  const brand = getBrand(credential.provider)
  const BrandIcon = brand.Icon
  const tierLevel: CredentialTierLevel | null = tierOf(credential)
  // The card's border colour IS the tier. A page that says "critical" in grey
  // has said it in a way nobody scanning will register.
  const tierTone: DetailTone =
    tierLevel === 4 ? "destructive" : tierLevel === 3 ? "warn" : tierLevel === 2 ? "blue" : "default"

  // The server ships agent ids and names as two parallel arrays (splitAgentRefs),
  // so a binding that names an agent by id can still be shown by name.
  const agentNameById = new Map<string, string>(
    (credential.agent_ids ?? []).map((id, i) => [id, credential.agent_names[i] ?? id]),
  )

  // Assignments carry the slug the binding rows lack; a slot that names an
  // agent links through it when the same agent is on both lists.
  const agentSlugByName = new Map<string, string | null>(assignments.map((a) => [a.agentName, a.agentSlug]))

  const shownAudit = auditExpanded ? audit : audit.slice(0, AUDIT_PREVIEW)

  const missingTools = Array.from(new Set(toolGaps.map((g) => g.tool).filter(Boolean)))
  const readinessTone: DetailTone =
    toolGaps.length > 0 ? "warn" : readinessKnown ? "success" : "default"
  const readinessLabel =
    toolGaps.length > 0
      ? missingTools.length > 0
        ? `Needs ${missingTools.join(", ")}`
        : "Tool missing"
      : readinessKnown
        ? "Ready"
        : "Readiness unknown"

  // The figures band. Same six-slot shape as the issue's, answering the
  // questions a vault gets asked instead of the ones a tracker does.
  const expiryDays = daysUntilExpiry(credential)
  const facts: StatItem[] = [
    { label: "Created", value: formatRelativeTime(credential.created_at) },
    {
      label: "Last used",
      value: credential.last_used_at ? formatRelativeTime(credential.last_used_at) : "never",
    },
    {
      label: "Expires",
      value:
        expiryDays === null ? "—" : expiryDays < 0 ? "expired" : expiryDays === 0 ? "today" : `${expiryDays}d`,
      tone:
        expiryDays === null
          ? "default"
          : expiryDays < 0
            ? "destructive"
            : expiryDays < EXPIRY_WARNING_DAYS
              ? "warn"
              : "default",
    },
    { label: "Used by", value: credential._count_agent_credentials || "—" },
    { label: "Fields", value: fields.length || "—" },
    {
      label: "Readiness",
      value: readinessLabel,
      tone: toolGaps.length > 0 ? "warn" : readinessKnown ? "success" : "default",
    },
  ]

  const handleTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await apiFetch(`/api/v1/credentials/${credential.id}/test?workspace_id=${workspaceId}`, {
        method: "POST",
      })
      if (!res.ok) {
        setTestResult({ valid: false, error: "Test request failed" })
        return
      }
      const data = await res.json()
      setTestResult({ valid: data.valid, error: data.error })
    } catch {
      setTestResult({ valid: false, error: "Network error" })
    } finally {
      setTesting(false)
    }
  }

  /**
   * Delete this credential.
   *
   * The three outcomes are deliberately distinct, and this is the only place
   * they are handled now that the list's own delete went with the table:
   *
   *   · ok      — refresh and close.
   *   · 404     — another admin deleted it between the dialog opening and this
   *               request landing (#1162). That is the outcome the user wanted,
   *               so it is a success with a note, not a failure. Silently
   *               closing would be worse: the row is already gone and nothing
   *               would explain why.
   *   · anything else — say so and STAY OPEN. The previous version dropped
   *     every failure on the floor: a 403 closed the dialog and left the
   *     credential in place, which reads exactly like a successful delete.
   */
  const handleDelete = async () => {
    const name = credential.name
    try {
      const res = await apiFetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
        method: "DELETE",
      })
      if (res.ok || res.status === 404) {
        if (res.status === 404) toast.success(`${name} was already deleted`)
        onRefresh()
        onOpenChange(false)
      } else {
        toast.error(`Couldn't delete ${name}.`)
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `Couldn't delete ${name}.`)
    } finally {
      setConfirmDelete(false)
    }
  }

  const setClassification = async (next: string) => {
    setSensitivitySaving(true)
    setSensitivityError(null)
    try {
      const res = await apiFetch(
        `/api/v1/credentials/${credential.id}/sensitivity?workspace_id=${workspaceId}`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ sensitivity: next }),
        },
      )
      const data = (await res.json().catch(() => ({}))) as { sensitivity?: string; error?: string }
      if (!res.ok) {
        setSensitivityError(typeof data.error === "string" ? data.error : `Request failed (${res.status})`)
        return
      }
      setSensitivity(data.sensitivity ?? next)
    } catch {
      setSensitivityError("Network error")
    } finally {
      setSensitivitySaving(false)
    }
  }

  return (
    <>
      {/* Master-detail INLINE, the way /integrations does it: the rail selects,
          the main pane becomes that credential, and a breadcrumb goes back.
          Not a modal — a modal keeps the list behind a scrim and makes the
          reader dismiss one secret before looking at the next, which is the
          wrong rhythm for a page whose job is moving between them.
          Add-a-credential stays a dialog on purpose: a create is a task you
          finish or abandon, an inspect is somewhere you navigate. */}
      <div className="flex h-full flex-col">
        {onBack && (
          <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2">
            <button
              type="button"
              onClick={onBack}
              className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            >
              <ChevronLeft className="h-3.5 w-3.5" />
              Back to credentials
            </button>
            {/* Not font-mono: this is the breadcrumb's nav context, matching
                /integrations. The monospace name belongs to the identity
                header below, which is the credential itself rather than a
                trail back to the list. */}
            <span className="truncate text-xs font-medium text-foreground/85">
              {credential.name}
            </span>
          </div>
        )}
        {/* One page, no tabs.
         *
         * It had five: Overview, Fields, Used by, Audit, Settings. Tabs are a
         * way of admitting a screen holds more than fits, and this one does
         * not — a credential is a value, its parts, who can read it, how hard
         * it is guarded, and what has happened to it. Five of those five are
         * things you want to see AT ONCE when you are deciding whether to
         * rotate something, and behind a tab each of them costs a click plus
         * the memory of what the other tab said.
         *
         * The shape is the issue detail's, deliberately and to the pixel where
         * the content allows: identity card, figures band, then a wide column
         * of the substance beside a narrow column of properties. Two screens in
         * one product that both mean "here is one thing in full" should not
         * look like two products. Everything is built from the same
         * components/ui/detail kit, so they cannot drift apart by accident.
         */}
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="flex flex-col gap-4 p-4">
            {/* ── Identity ─────────────────────────────────────────────── */}
            <Appear order={0}>
              <DetailCard>
                <div className="flex flex-col gap-3">
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="flex min-w-0 items-start gap-3">
                      <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border border-border/60 bg-surface-raised">
                        <BrandIcon
                          className="h-5 w-5"
                          style={{ color: brandColor(brand) }}
                          aria-label={brand.label}
                        />
                      </div>
                      <div className="min-w-0">
                        <h1 className="truncate font-mono text-lg font-semibold tracking-tight">
                          {credential.name}
                        </h1>
                        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                          <span className="font-mono">{credentialTypeLabel(credential.type)}</span>
                          <span aria-hidden>·</span>
                          <span>{brand.label}</span>
                          <span aria-hidden>·</span>
                          <span>{credential.scope === "CREW" ? "Crew-scoped" : "Workspace"}</span>
                          {credential.account_label && (
                            <>
                              <span aria-hidden>·</span>
                              <span className="font-medium">{credential.account_label}</span>
                            </>
                          )}
                          {credential.username && (
                            <>
                              <span aria-hidden>·</span>
                              <span className="font-mono">{credential.username}</span>
                            </>
                          )}
                        </div>
                      </div>
                    </div>
                    {onEdit && canUpdate && (
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => onEdit(credential)}
                        className="shrink-0"
                      >
                        <Pencil className="mr-1.5 h-3 w-3" />
                        Edit
                      </Button>
                    )}
                  </div>

                  {credential.description && (
                    <p className="max-w-[80ch] text-[13px] leading-relaxed text-foreground/85">
                      {credential.description}
                    </p>
                  )}

                  {/* The chips answer, in one line, the three questions asked
                      about a secret before any other: how hard is it guarded,
                      who may see the value, and does it work. */}
                  <div className="flex flex-wrap items-center gap-1.5">
                    {tierLevel !== null ? (
                      <Pill tone={tierTone}>
                        <ShieldCheck className="h-3 w-3" />
                        {credential.security_level_label || tierMeta(tierLevel).label}
                      </Pill>
                    ) : (
                      <Pill tone="default">
                        <ShieldCheck className="h-3 w-3" />
                        Tier not reported
                      </Pill>
                    )}
                    {effectiveSensitivity && (
                      <Pill
                        tone={
                          effectiveSensitivity === "SEALED"
                            ? "destructive"
                            : effectiveSensitivity === "RESTRICTED"
                              ? "warn"
                              : "default"
                        }
                      >
                        {effectiveSensitivity === "SEALED" ? (
                          <EyeOff className="h-3 w-3" />
                        ) : (
                          <Eye className="h-3 w-3" />
                        )}
                        {effectiveSensitivity}
                      </Pill>
                    )}
                    <Pill tone={readinessTone}>
                      {toolGaps.length > 0 ? (
                        <PackageX className="h-3 w-3" />
                      ) : readinessKnown ? (
                        <CheckCircle2 className="h-3 w-3" />
                      ) : (
                        <PackageX className="h-3 w-3" />
                      )}
                      {readinessLabel}
                    </Pill>
                    {brand.cli && (
                      <Pill tone="blue">
                        <TerminalSquare className="h-3 w-3" />
                        CLI
                      </Pill>
                    )}
                    {credential.mcp_used && (
                      <Pill tone="blue">
                        <Plug className="h-3 w-3" />
                        MCP
                      </Pill>
                    )}
                    {(credential.tags ?? []).map((t) => (
                      <Pill key={t} tone="default">
                        <Hash className="h-3 w-3" />
                        {t}
                      </Pill>
                    ))}
                  </div>
                </div>
              </DetailCard>
            </Appear>

            {/* ── The figures band ─────────────────────────────────────── */}
            <Appear order={1}>
              <StatStrip items={facts} />
            </Appear>

            <div className="grid grid-cols-1 gap-4 xl:grid-cols-3 2xl:grid-cols-4">
              {/* ── The substance ─────────────────────────────────────── */}
              <div className="flex flex-col gap-4 xl:col-span-2 2xl:col-span-3">
                {credential.last_error && (
                  <Appear order={2}>
                    <DetailCard title="Last error" icon={AlertTriangle} tone="destructive">
                      <p className="font-mono text-[12px] leading-relaxed text-foreground/85">
                        {credential.last_error}
                      </p>
                    </DetailCard>
                  </Appear>
                )}

                {/*
                  The value block. §2.6 L8 — and this ordering is a security
                  decision, not a layout preference: ROTATE is the primary
                  action and reveal is the secondary one, because most
                  legitimate reasons to want a value are really reasons to
                  replace it. A control that is used rarely is a control that
                  keeps working; making rotation the path of least resistance
                  is what keeps the reveal count low enough that each one is
                  worth investigating.
                */}
                <Appear order={3}>
                  <DetailCard
                    title="Value"
                    icon={KeyRound}
                    subtitle="encrypted at rest"
                    action={
                      // Test now is only meaningful where the server maintains
                      // an upstream probe (credential.testable — see
                      // probeSupportedProviders) and requires update
                      // permission. Mirrors the BE gating in TestStored, so
                      // nobody clicks into a 403. Deliberately NOT gated on
                      // brand .cli like the badge above: that flag marks the
                      // CLIs Crewship drives in the container, which excluded
                      // GitHub/GitLab/Vercel despite real probes.
                      credential.testable && canUpdate ? (
                        <span className="inline-flex items-center gap-2">
                          {testResult && (
                            <span
                              className={cn(
                                "inline-flex items-center gap-1 text-[11px]",
                                testResult.valid ? "text-success" : "text-destructive",
                              )}
                            >
                              {testResult.valid ? (
                                <CheckCircle2 className="h-3 w-3" />
                              ) : (
                                <XCircle className="h-3 w-3" />
                              )}
                              {testResult.valid ? "Valid" : testResult.error || "Invalid"}
                            </span>
                          )}
                          <Button size="sm" variant="outline" onClick={handleTest} disabled={testing}>
                            {testing ? (
                              <Spinner className="mr-1.5 h-3 w-3" />
                            ) : (
                              <FlaskConical className="mr-1.5 h-3 w-3" />
                            )}
                            Test now
                          </Button>
                        </span>
                      ) : undefined
                    }
                  >
                    <div className="rounded-md border border-white/10 bg-background px-3 py-2 font-mono text-xs text-muted-foreground">
                      ••••••••••••••••
                    </div>

                    {canRotate && (
                      <div className="mt-2.5 space-y-1.5">
                        <Button size="sm" className="w-full justify-start" onClick={() => onRotate(credential)}>
                          <RefreshCw className="mr-1.5 h-3.5 w-3.5" />
                          Rotate and show the new value
                        </Button>
                        <p className="text-[10px] text-muted-foreground">
                          Mints a new value, shows it once, and lets the old one drain through the
                          grace window. Nothing existing is disclosed.
                        </p>
                      </div>
                    )}

                    {/* Reveal, or why not.
                     *
                     * It used to render nothing at all when any of the four
                     * gates was shut, which teaches the reader that this
                     * product cannot show a secret — and then a colleague on
                     * the same screen has the button. Each gate is a different
                     * fix (a workspace switch, a capability grant, a
                     * classification), so each one says which. */}
                    {canReveal ? (
                      <div className="mt-2 space-y-1">
                        <Button
                          size="sm"
                          variant="ghost"
                          className="-ml-2 w-full justify-start text-[11px] text-muted-foreground hover:text-foreground"
                          onClick={() => setRevealOpen(true)}
                        >
                          <Eye className="mr-1.5 h-3 w-3" />
                          Reveal the existing value…
                        </Button>
                        <p className="text-[10px] text-muted-foreground">
                          Requires a written reason and is recorded in the tamper-evident journal
                          before the value is returned.
                        </p>
                      </div>
                    ) : (
                      canUpdate && (
                        <p className="mt-2 flex items-start gap-1.5 text-[10px] text-muted-foreground">
                          <EyeOff className="mt-px h-3 w-3 shrink-0" aria-hidden="true" />
                          <span>{revealBlockedReason}</span>
                        </p>
                      )
                    )}

                    {/* Replacing the value lives in Edit, not here.
                     *
                     * There were three ways to change one secret on one screen:
                     * rotate, an inline "replace the value" input, and the Value
                     * field in the Edit dialog. Two of those did the same PATCH.
                     * Rotate is a different operation — it keeps the old value
                     * alive through a grace window — so it stays; the plain swap
                     * belongs where every other property of this credential is
                     * changed. */}
                    {canUpdate && onEdit && (
                      <p className="mt-3 border-t border-hairline pt-3 text-[10px] text-muted-foreground">
                        To paste a new value without a grace window, use{" "}
                        <button
                          type="button"
                          onClick={() => onEdit(credential)}
                          className="font-medium text-primary hover:underline"
                        >
                          Edit
                        </button>
                        {" "}— leave the field empty there to keep the existing one.
                      </p>
                    )}
                  </DetailCard>
                </Appear>

                {/*
                  Fields. A secret part is listed by KEY and marked "secret" —
                  there is no masked placeholder standing in for a value,
                  because the server does not return one: credential_fields.go
                  omits `encrypted_value` from the SELECT entirely, so there are
                  no bytes here to render even by accident. Non-secret parts
                  (region, account id) ARE shown, which is the entire reason
                  they are stored in the clear.
                */}
                <Appear order={4}>
                  <DetailCard
                    title="Fields"
                    icon={ListTree}
                    subtitle={fields.length > 0 ? String(fields.length) : undefined}
                  >
                    {fieldsLoading ? (
                      <div className="py-6 text-center">
                        <Spinner className="inline h-4 w-4 text-muted-foreground" />
                      </div>
                    ) : fields.length === 0 ? (
                      <p className="text-[12px] text-muted-foreground">
                        This credential is a single value — no extra parts.
                      </p>
                    ) : (
                      <dl className="grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
                        {fields.map((f) => (
                          <div
                            key={f.key}
                            className="flex items-baseline gap-2 text-[12px]"
                          >
                            <dt className="min-w-0 shrink-0 font-mono text-foreground/90">{f.key}</dt>
                            <dd className="min-w-0 flex-1 truncate text-right">
                              {f.is_secret ? (
                                <Badge
                                  variant="outline"
                                  className="border-warn/40 px-1 text-[9px] text-warn"
                                >
                                  secret
                                </Badge>
                              ) : (
                                <span className="font-mono text-[11px] text-muted-foreground">
                                  {f.value}
                                </span>
                              )}
                            </dd>
                          </div>
                        ))}
                      </dl>
                    )}
                  </DetailCard>
                </Appear>

                <Appear order={5}>
                  <DetailCard
                    title="Used by"
                    icon={Users}
                    subtitle={
                      credential._count_agent_credentials > 0
                        ? `${credential._count_agent_credentials}`
                        : undefined
                    }
                  >
                    {/* Bindings — (scope, slot) → this credential. This is the
                        answer to "which env var will the container actually
                        see", which before P3 had no answer short of booting
                        the agent. */}
                    {bindings.length > 0 && (
                      <div className="mb-3 space-y-1.5">
                        <FieldLabel>Slots</FieldLabel>
                        <ul className="space-y-1">
                          {bindings.map((b) => {
                            const crew = b.crew_id ? crewsById[b.crew_id] : undefined
                            const agentName = b.agent_id ? agentNameById.get(b.agent_id) : undefined
                            return (
                              <li
                                key={b.id}
                                className="flex items-center gap-2 rounded-md border border-white/10 bg-background px-3 py-2 text-xs"
                              >
                                <Badge variant="outline" className="px-1 text-[9px]">
                                  {b.scope}
                                </Badge>
                                <span className="font-mono">{b.slot}</span>
                                {/* Who the slot reaches, drawn as itself. A raw
                                    cuid in this column made the one row that
                                    answers "who can read this?" unreadable. */}
                                {b.crew_id && (
                                  <UsedByTarget href={crew?.slug ? entityHref({ kind: "crew", slug: crew.slug }) : undefined}>
                                    <CrewIcon
                                      icon={crew?.icon ?? ""}
                                      color={crew?.color ?? undefined}
                                      size="sm"
                                      className="!h-4 !w-4 !rounded shrink-0"
                                    />
                                    <span className="truncate">
                                      {crew?.name ?? b.crew_id}
                                    </span>
                                  </UsedByTarget>
                                )}
                                {!b.crew_id && b.agent_id && (
                                  <UsedByTarget href={agentName && agentSlugByName.get(agentName) ? entityHref({ kind: "agent", slug: agentSlugByName.get(agentName)! }) : undefined}>
                                    <AgentAvatar seed={b.agent_id} className="h-4 w-4 shrink-0" alt="" />
                                    <span className="truncate">{agentName ?? b.agent_id}</span>
                                  </UsedByTarget>
                                )}
                              </li>
                            )
                          })}
                        </ul>
                      </div>
                    )}

                    {assignments.length > 0 ? (
                      <ul className="space-y-1.5">
                        {assignments.map((a) => (
                          <li
                            key={`${a.agentName}:${a.envVarName}:${a.grantSource}`}
                            className="flex items-center gap-2 rounded-md border border-white/10 bg-background px-3 py-2 text-[13px]"
                          >
                            <AgentAvatar seed={a.agentName} className="h-4 w-4 shrink-0" alt="" />
                            {a.agentSlug ? (
                              <Link href={entityHref({ kind: "agent", slug: a.agentSlug })} className="truncate hover:underline">{a.agentName}</Link>
                            ) : (
                              <span className="truncate">{a.agentName}</span>
                            )}
                            <span className="truncate font-mono text-[10px] text-muted-foreground">
                              {a.envVarName}
                            </span>
                            {/* grant_source decides where the revoke lives: an
                                explicit grant has an assignment id and its own
                                DELETE; a crew-derived one has no row at all and
                                can only be taken away by unlinking the crew. */}
                            <Badge
                              variant="outline"
                              className={cn(
                                "ml-auto shrink-0 px-1 text-[9px]",
                                a.grantSource === "crew" ? "border-info/40 text-info" : "opacity-70",
                              )}
                              title={
                                a.grantSource === "crew"
                                  ? "Inherited from the crew — unlink the crew to take it away"
                                  : "Granted to this agent directly"
                              }
                            >
                              {a.grantSource === "crew" ? "crew grant" : "explicit"}
                            </Badge>
                            {a.expiresAt && (
                              <Badge
                                variant="outline"
                                className={cn(
                                  "shrink-0 px-1 text-[9px]",
                                  a.expired
                                    ? "border-destructive/40 text-destructive"
                                    : "border-warn/40 text-warn",
                                )}
                              >
                                {a.expired ? "lease expired" : "leased"}
                              </Badge>
                            )}
                          </li>
                        ))}
                      </ul>
                    ) : credential.agent_names.length > 0 ? (
                      <ul className="space-y-1.5">
                        {credential.agent_names.map((name, i) => (
                          <li
                            key={name}
                            className="flex items-center gap-2 rounded-md border border-white/10 bg-background px-3 py-2 text-[13px]"
                          >
                            <AgentAvatar
                              seed={credential.agent_ids?.[i] ?? name}
                              className="h-4 w-4 shrink-0"
                              alt=""
                            />
                            {name}
                          </li>
                        ))}
                      </ul>
                    ) : (
                      <p className="text-[12px] text-muted-foreground">
                        No agent holds this credential yet.
                      </p>
                    )}

                    {credential.mcp_used && (
                      <p className="mt-3 flex items-center gap-2 rounded-md border border-info/25 bg-info/[0.05] px-3 py-2 text-[11px]">
                        <span className="min-w-0 flex-1">Also referenced by one or more MCP server integrations.</span>
                        <Link href={entityHref({ kind: "integrations", tab: "tools", section: "crew-tools" })} className="shrink-0 text-primary-hover hover:underline">
                          Crew tools →
                        </Link>
                      </p>
                    )}
                  </DetailCard>
                </Appear>

                {/* Audit last, and only for a reader allowed to read it. GET
                    /audit is MANAGER+ because it exposes the IPs behind admin
                    actions; rendering the section for a MEMBER would turn a 403
                    into "this credential has no history", which is a different
                    and false statement. */}
                {canUpdate && (
                  <Appear order={6}>
                    <DetailCard
                      title="Audit"
                      icon={Activity}
                      subtitle={
                        audit.length === 0
                          ? undefined
                          : auditExpanded || audit.length <= AUDIT_PREVIEW
                            ? `${audit.length}${audit.length === AUDIT_FETCH_LIMIT ? "+" : ""}`
                            : `${AUDIT_PREVIEW} of ${audit.length}${audit.length === AUDIT_FETCH_LIMIT ? "+" : ""}`
                      }
                      action={
                        audit.length > AUDIT_PREVIEW ? (
                          <button
                            type="button"
                            onClick={() => setAuditExpanded((v) => !v)}
                            className="text-primary hover:underline"
                          >
                            {auditExpanded ? "Show less" : `Show all ${audit.length}`}
                          </button>
                        ) : undefined
                      }
                    >
                      {auditLoading ? (
                        <div className="py-6 text-center">
                          <Spinner className="inline h-4 w-4 text-muted-foreground" />
                        </div>
                      ) : audit.length === 0 ? (
                        <p className="text-[12px] text-muted-foreground">
                          Nothing has happened to this credential yet.
                        </p>
                      ) : (
                        <ul className="space-y-0.5">
                          {shownAudit.map((e, idx) => (
                            <motion.li
                              key={e.id}
                              initial={{ opacity: 0, y: 4 }}
                              animate={{ opacity: 1, y: 0 }}
                              transition={{ duration: 0.12, delay: Math.min(idx, 12) * 0.015 }}
                              className="flex flex-wrap items-center gap-2 rounded-md px-1.5 py-1.5 text-xs transition-colors hover:bg-white/[0.03]"
                            >
                              <AuditActor event={e} crewsById={crewsById} />
                              <Badge variant="outline" className="px-1.5 font-mono text-[10px]">
                                {e.event_type}
                              </Badge>
                              {e.ip_address && (
                                <span className="font-mono text-[10px] text-muted-foreground-soft">
                                  {e.ip_address}
                                </span>
                              )}
                              <span className="ml-auto text-[10px] text-muted-foreground-soft">
                                {formatRelativeTime(e.occurred_at)}
                              </span>
                            </motion.li>
                          ))}
                        </ul>
                      )}
                    </DetailCard>
                  </Appear>
                )}
              </div>

              {/* ── Properties ────────────────────────────────────────── */}
              <div className="flex flex-col gap-4">
                <Appear order={7}>
                  <DetailCard title="Properties">
                    <dl className="space-y-0.5">
                      <Row icon={Info} label="Type">
                        {credentialTypeLabel(credential.type)}
                      </Row>
                      <Row icon={Boxes} label="Provider">
                        <span className="inline-flex items-center gap-1.5">
                          <BrandIcon className="h-3.5 w-3.5" style={{ color: brandColor(brand) }} />
                          {brand.label}
                        </span>
                      </Row>
                      <Row icon={Layers} label="Scope">
                        {credential.scope === "CREW" ? "Crew-scoped" : "Workspace"}
                      </Row>
                      {credential.username && (
                        <Row icon={UserCircle2} label="Username">
                          <span className="font-mono">{credential.username}</span>
                        </Row>
                      )}
                      <Row icon={Clock} label="Created">
                        {formatDate(credential.created_at)}
                      </Row>
                      <Row icon={Clock} label="Last used">
                        {credential.last_used_at ? (
                          formatRelativeTime(credential.last_used_at)
                        ) : (
                          <span className="text-muted-foreground-soft">never</span>
                        )}
                      </Row>
                      {credential.token_expires_at && (
                        <Row icon={Clock} label="Expires">
                          {formatDate(credential.token_expires_at)}
                        </Row>
                      )}
                    </dl>

                    {credential.last_used_ips.length > 0 && (
                      <div className="mt-3 border-t border-hairline pt-3">
                        <FieldLabel>Last {credential.last_used_ips.length} IPs</FieldLabel>
                        <ul className="mt-1.5 flex flex-wrap gap-1.5">
                          {credential.last_used_ips.map((ip) => (
                            <li
                              key={ip}
                              className="inline-flex items-center gap-1.5 rounded-full border border-white/[0.08] px-2 py-0.5 font-mono text-[10px] text-foreground/80"
                            >
                              <span className="h-1 w-1 rounded-full bg-success/60" />
                              {ip}
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                  </DetailCard>
                </Appear>

                {/* The tier gets a card of its own, toned by how much it costs
                    to be wrong about. "L4" is an identifier; the blast radius
                    and the consequence are what an operator can act on. */}
                <Appear order={8}>
                  <DetailCard title="Keeper tier" icon={ShieldCheck} tone={tierTone}>
                    {tierLevel === null ? (
                      <p className="text-[11px] text-muted-foreground">
                        This server did not report a tier, so the console cannot say how Keeper
                        guards this credential. Set one with{" "}
                        <code className="font-mono text-foreground/80">
                          crewship credential update --security-level
                        </code>
                        .
                      </p>
                    ) : (
                      <>
                        <div className="flex items-center gap-2">
                          <span
                            aria-hidden="true"
                            className={cn("h-2 w-2 shrink-0 rounded-full", tierMeta(tierLevel).dotClass)}
                          />
                          <span className="font-mono text-[13px] text-foreground/90">
                            {credential.security_level_label || tierMeta(tierLevel).label}
                          </span>
                        </div>
                        <p className="mt-2 text-[11px] text-muted-foreground">
                          {tierMeta(tierLevel).blast}
                        </p>
                        <p className="mt-1.5 text-[11px] text-foreground/70">
                          {tierMeta(tierLevel).consequence}
                        </p>
                      </>
                    )}
                  </DetailCard>
                </Appear>

                {/* Readiness has THREE states, not two. "No gap reported" and
                    "nobody reported" look identical in the data and mean
                    opposite things — a green tick we did not earn is exactly
                    the false reassurance the readiness endpoint exists to
                    remove. */}
                <Appear order={9}>
                  <DetailCard
                    title="Readiness"
                    icon={toolGaps.length > 0 ? PackageX : CheckCircle2}
                    tone={readinessTone}
                  >
                    {toolGaps.length > 0 ? (
                      <>
                        <div className="flex items-center gap-2 text-warn">
                          <PackageX className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                          <span className="text-[13px]">
                            {missingTools.length > 0
                              ? `Needs ${missingTools.join(", ")}`
                              : "A tool is missing"}
                          </span>
                        </div>
                        <ul className="mt-2 space-y-1">
                          {toolGaps.map((gap) => (
                            <li key={`${gap.crewId}-${gap.tool}`} className="text-[11px] text-muted-foreground">
                              <span className="text-foreground/80">{gap.crewName}</span>
                              {gap.featureId && (
                                <>
                                  {" — add "}
                                  <code className="font-mono text-foreground/70">{gap.featureId}</code>
                                  {" and rebuild"}
                                </>
                              )}
                            </li>
                          ))}
                        </ul>
                      </>
                    ) : readinessKnown ? (
                      <>
                        <div className="flex items-center gap-2 text-success">
                          <CheckCircle2 className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                          <span className="text-[13px]">Ready</span>
                        </div>
                        <p className="mt-2 text-[11px] text-muted-foreground">
                          Every crew that can use this credential also has the CLI that reads it.
                        </p>
                      </>
                    ) : (
                      <p className="text-[11px] text-muted-foreground">
                        No crew has reported its tool inventory yet, so we cannot say whether the CLI
                        that reads this credential is present. This is not the same as
                        &ldquo;nothing is missing&rdquo;.
                      </p>
                    )}
                  </DetailCard>
                </Appear>

                {/* Classification. Raising is MANAGER+ and unaudited (it only
                    ever removes reach); lowering is OWNER/ADMIN and journaled
                    as a precondition, because it hands out a key that did not
                    exist a second earlier. */}
                {canUpdate && (
                  <Appear order={10}>
                    <DetailCard
                      title="Classification"
                      icon={Eye}
                      footer={
                        effectiveSensitivity === null
                          ? "The current classification is not reported by the credentials API — picking one sets it."
                          : effectiveSensitivity === "SEALED"
                            ? "SEALED can never be revealed, by any role. Break-glass is rotation, not disclosure."
                            : "Raise it at any time; lowering it is an audited, admin-only action."
                      }
                    >
                      <div className="flex flex-wrap gap-1.5">
                        {SENSITIVITY_LEVELS.map((level) => {
                          const current = effectiveSensitivity
                          const lowering =
                            current !== null &&
                            SENSITIVITY_LEVELS.indexOf(level) <
                              SENSITIVITY_LEVELS.indexOf(current as (typeof SENSITIVITY_LEVELS)[number])
                          const blocked = lowering && !canLowerSensitivity
                          return (
                            <button
                              key={level}
                              type="button"
                              disabled={sensitivitySaving || blocked || level === current}
                              aria-pressed={level === current}
                              onClick={() => setClassification(level)}
                              title={
                                blocked
                                  ? "Lowering a classification is a workspace-admin action"
                                  : undefined
                              }
                              className={cn(
                                "rounded-full border px-2.5 py-0.5 text-[11px] transition-colors disabled:opacity-40",
                                level === current
                                  ? "border-primary/50 bg-primary/10 text-primary-hover"
                                  : "border-white/10 text-muted-foreground hover:text-foreground",
                              )}
                            >
                              {level}
                            </button>
                          )
                        })}
                      </div>
                      {sensitivityError && (
                        <span className="mt-2 inline-flex items-center gap-1 text-[11px] text-destructive">
                          <XCircle className="h-3 w-3" />
                          {sensitivityError}
                        </span>
                      )}
                    </DetailCard>
                  </Appear>
                )}

                {/* Rotation is gated on canRotate ALONE, deliberately not
                    nested under canUpdate: PATCH is MANAGER+, while rotate
                    additionally accepts any member holding credential.rotate
                    (requireRoleOrCapabilityOrForbid, #1028) — the grant that
                    lets an oncall MEMBER replace a leaked token without blanket
                    vault reach. Nesting it hid the action from precisely that
                    tier. */}
                {canRotate && (
                  <Appear order={11}>
                    <DetailCard
                      title="Rotation"
                      icon={RefreshCw}
                      subtitle={rotations.length > 0 ? String(rotations.length) : undefined}
                      footer="Issues a new value and keeps the old one working for the grace window, so agents mid-run don't break."
                    >
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() => onRotate(credential)}
                        className="w-full justify-start"
                      >
                        <RefreshCw className="mr-1.5 h-3 w-3" />
                        Rotate with grace overlap…
                      </Button>
                      {rotations.length > 0 && (
                        <ul className="mt-2.5 space-y-1">
                          {rotations.slice(0, 5).map((r) => (
                            <li key={r.id} className="flex items-center gap-2 text-xs">
                              <Badge
                                variant="outline"
                                className={cn(
                                  "px-1.5 text-[10px]",
                                  r.status === "ACTIVE" && "border-primary/40 text-primary",
                                  r.status === "EXPIRED" && "border-success/30 text-success",
                                  r.status === "CANCELLED" && "border-warn/30 text-warn",
                                )}
                              >
                                {r.status}
                              </Badge>
                              <span className="text-muted-foreground">
                                {formatRelativeTime(r.rotated_at)}
                              </span>
                              <span className="ml-auto font-mono text-[10px] text-muted-foreground">
                                {Math.round(r.grace_seconds / 3600)}h grace
                              </span>
                            </li>
                          ))}
                        </ul>
                      )}
                    </DetailCard>
                  </Appear>
                )}

                {/* Say which of the three write gates the reader is behind,
                    rather than one blanket refusal. They are genuinely
                    different: a MEMBER holding credential.rotate can replace a
                    leaked token, a MANAGER can rewrite a value but not rotate
                    with overlap or delete, and only a role with none of the
                    three has nothing to do here. */}
                {!(canUpdate && canRotate && canDelete) && (
                  <Appear order={12}>
                    <DetailCard title="Permissions">
                      <p className="text-[11px] text-muted-foreground">
                        {/* Three gates, three different sentences. Saying
                            "rotation requires an admin" to someone holding
                            credential.rotate is worse than saying nothing: the
                            button is right there. */}
                        {!canUpdate && !canRotate
                          ? "You don't have permission to modify this credential."
                          : !canUpdate
                            ? "You can rotate this credential. Replacing its value outright requires a workspace manager."
                            : canRotate
                              ? "Deleting this credential requires a workspace admin."
                              : "Rotation with grace overlap and deletion require a workspace admin."}
                      </p>
                    </DetailCard>
                  </Appear>
                )}

                {canDelete && (
                  <Appear order={13}>
                    <DetailCard
                      title="Danger zone"
                      icon={Trash2}
                      tone="destructive"
                      footer="Agents that use this credential start failing immediately. This cannot be undone."
                    >
                      <Button
                        size="sm"
                        variant="outline"
                        className="w-full justify-start border-destructive/30 text-destructive hover:bg-destructive/[0.05]"
                        onClick={() => setConfirmDelete(true)}
                      >
                        <Trash2 className="mr-1.5 h-3.5 w-3.5" />
                        Delete credential
                      </Button>
                    </DetailCard>
                  </Appear>
                )}
              </div>
            </div>
          </div>
        </div>
      </div>

      <RevealDialog
        workspaceId={workspaceId}
        credentialId={credential.id}
        credentialName={credential.name}
        sensitivity={effectiveSensitivity}
        open={revealOpen}
        onOpenChange={setRevealOpen}
        onRotateInstead={() => {
          setRevealOpen(false)
          onRotate(credential)
        }}
      />

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete credential?</AlertDialogTitle>
            <AlertDialogDescription>
              <span className="font-mono">{credential.name}</span> will be permanently deleted.
              Agents that use this credential will start failing immediately. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Cancel>Cancel</Cancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={handleDelete}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

/**
 * Who actually holds this credential, and under which rule.
 *
 * There is no "list the assignments of credential X" endpoint — grant
 * provenance lives on GET /agents/{agentId}/credentials, one agent at a time.
 * So this asks only the agents that could plausibly hold it: the ones named on
 * the credential (explicit grants) and the members of the crews it is linked
 * to (crew grants), capped so a large workspace cannot turn one sheet into
 * dozens of requests. Agents outside both sets cannot have it.
 *
 * grant_source itself always comes from the server. Deriving it here from
 * scope + crew membership would be a second opinion, and the two would drift.
 */
async function loadAssignments(
  workspaceId: string,
  credential: CredentialSummary,
): Promise<AssignmentRow[]> {
  const res = await apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
  if (!res.ok) return []
  const agents = await res.json()
  if (!Array.isArray(agents)) return []

  const crewIds = new Set(credential.crew_ids ?? [])
  const names = new Set(credential.agent_names ?? [])
  const candidates = (agents as { id?: string; name?: string; slug?: string; crew_id?: string | null }[])
    .filter((a) => typeof a?.id === "string")
    .filter((a) => names.has(a.name ?? "") || (a.crew_id ? crewIds.has(a.crew_id) : false))
    .slice(0, MAX_ASSIGNMENT_LOOKUPS)

  const rows: AssignmentRow[] = []
  await Promise.all(
    candidates.map(async (agent) => {
      try {
        const r = await apiFetch(
          `/api/v1/agents/${encodeURIComponent(agent.id!)}/credentials` +
            `?workspace_id=${encodeURIComponent(workspaceId)}`,
        )
        if (!r.ok) return
        const list = await r.json()
        if (!Array.isArray(list)) return
        for (const row of list as {
          credential_id?: string
          env_var_name?: string
          grant_source?: string
          expires_at?: string
          expired?: boolean
        }[]) {
          if (row?.credential_id !== credential.id) continue
          rows.push({
            agentName: agent.name ?? agent.id!,
            agentSlug: agent.slug ?? null,
            envVarName: row.env_var_name ?? "",
            grantSource: row.grant_source ?? "explicit",
            expiresAt: row.expires_at,
            expired: Boolean(row.expired),
          })
        }
      } catch {
        // One unreachable agent must not blank the whole list.
      }
    }),
  )
  return rows.sort((a, b) => a.agentName.localeCompare(b.agentName))
}

/**
 * One property row in the right-hand column.
 *
 * Deliberately identical to the issue detail's Row — icon, fixed-width label,
 * truncating value. Two screens that both mean "here is one thing in full"
 * should not spell the same row two ways.
 */
/**
 * Who an audit event belongs to.
 *
 * An agent gets the same avatar it has on every other page — the row that says
 * a secret was read is the row where recognising the reader matters most. A
 * human gets a person glyph rather than a generated face, because a synthesised
 * avatar for a colleague is a picture of someone who does not exist. An
 * unattributed row says "system" instead of leaving a gap, so "nobody signed
 * this" and "we forgot to render it" cannot be confused.
 */
function AuditActor({
  event,
  crewsById,
}: {
  event: AuditEvent
  crewsById: Record<string, CredentialCrewRef>
}) {
  // An older server sends no actor block at all; the agent_id column is the one
  // attribution that predates it, so it stands in for both the kind and the id.
  const actorId = event.actor_id || event.agent_id || ""
  const kind = event.actor_kind ?? (event.agent_id ? "agent" : "system")
  const label = event.actor_name || actorId || "unknown"


  if (kind === "agent") {
    return (
      <span className="inline-flex min-w-0 items-center gap-1.5" title={`Agent · ${label}`}>
        <AgentAvatar seed={actorId || label} className="h-4 w-4 shrink-0" alt="" />
        <span className="max-w-[140px] truncate text-foreground/85">{label}</span>
      </span>
    )
  }
  if (kind === "crew") {
    // A sidecar read. There is no agent to name — the sidecar serves a whole
    // container — so the crew that owns it is the truthful attribution.
    const crew = crewsById[actorId]
    return (
      <span className="inline-flex min-w-0 items-center gap-1.5" title={`Sidecar · ${crew?.name ?? label}`}>
        <CrewIcon
          icon={crew?.icon ?? ""}
          color={crew?.color ?? undefined}
          size="sm"
          className="!h-4 !w-4 !rounded shrink-0"
        />
        <span className="max-w-[140px] truncate text-foreground/85">{crew?.name ?? label}</span>
      </span>
    )
  }
  if (kind === "user") {
    return (
      <span className="inline-flex min-w-0 items-center gap-1.5" title={`Person · ${label}`}>
        <span
          aria-hidden="true"
          className="flex h-4 w-4 shrink-0 items-center justify-center rounded-full bg-primary/15"
        >
          <UserCircle2 className="h-3 w-3 text-primary" />
        </span>
        <span className="max-w-[140px] truncate text-foreground/85">{label}</span>
      </span>
    )
  }
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5" title="Recorded without an actor">
      <Cpu className="h-3.5 w-3.5 shrink-0 text-muted-foreground-soft" aria-hidden="true" />
      <span className="text-muted-foreground-soft">system</span>
    </span>
  )
}

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
      <dt className="w-[70px] shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 truncate text-foreground/85">{children}</dd>
    </div>
  )
}


// Inline alias so we don't have to import AlertDialogCancel everywhere — saves a line.
/** The crew or agent a slot reaches — a link when its slug is known, so
 *  "who can read this?" is one click from the answer (README §5). */
function UsedByTarget({ href, children }: { href?: string; children: React.ReactNode }) {
  const cls = "ml-auto inline-flex min-w-0 items-center gap-1.5"
  if (!href) return <span className={cls}>{children}</span>
  return <Link href={href} className={cn(cls, "hover:underline")}>{children}</Link>
}

function Cancel({ children }: { children: React.ReactNode }) {
  return <AlertDialogCancel>{children}</AlertDialogCancel>
}
