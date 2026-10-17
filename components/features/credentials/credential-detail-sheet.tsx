"use client"

/**
 * A secret's page (Harbor 2, /credentials?id=…): the header with its state,
 * scope, tier and classification, then tabs — Overview · Access · Value
 * history · Activity · Settings (`?tab=`). A provider login gets Overview ·
 * Pays for · Activity · Settings.
 *
 * Every sub-resource loads once on open; the audit and the rotations stay
 * gated on permission so a MEMBER never turns a 403 into "no history".
 * Reveal stays an explicit, reasoned and audited ceremony (reveal-dialog);
 * nothing on this page holds or renders a stored value.
 */

import { credentialTagClassName } from "@/lib/credentials/tag-accent"
import * as React from "react"
import Link from "next/link"
import {
  Activity,
  AlertTriangle,
  Bot,
  CheckCircle2,
  ChevronLeft,
  Clock,
  Cpu,
  Eye,
  EyeOff,
  FlaskConical,
  Hash,
  Info,
  KeyRound,
  Layers,
  Lock,
  PackageX,
  RefreshCw,
  ShieldCheck,
  TerminalSquare,
  UserCircle2,
  Users,
  XCircle,
} from "lucide-react"
import { toast } from "@/lib/toast"
import { Spinner } from "@/components/ui/spinner"

import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { StatusPill } from "@/components/ui/status-pill"
import { TabBar } from "@/components/ui/tab-bar"
import { toastSwitchError } from "@/components/ui/page-save-bar"
import { formatDate, formatRelativeTime } from "@/lib/time"
import { DetailCard, Pill } from "@/components/ui/detail"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { getBrand } from "@/lib/credential-providers/registry"
import { credentialTypeLabel } from "@/lib/credentials/item-types"
import { daysUntilExpiry, deriveSecretState } from "@/lib/credentials/facets"
import { tierMeta, tierOf, type CredentialTierLevel } from "@/lib/credentials/tiers"
import type { SecretTab } from "@/lib/credentials/vault-url-state"
import type { CredentialCrewRef, CredentialToolGap } from "@/hooks/use-credential-readiness"
import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { hasLogin, paysForLabel, type ProviderLogin } from "@/lib/credentials/provider-logins"
import { RevealDialog } from "./reveal-dialog"
import { GiveToAgentsDialog } from "./give-to-agents-dialog"
import { PaysForPanel } from "./pays-for-panel"
import { ProviderLoginActions, ProviderLoginCards } from "./provider-login-detail"
import { CredentialTierBadge } from "./credential-tier-badge"
import { BrandTile } from "./secrets-table"
import { SecretSettings } from "./secret-settings"
import { SecretValueHistory } from "./secret-value-history"
import { YourAccess } from "@/components/features/access/your-access"
import { useAccessMe } from "@/hooks/use-access-me"

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
  /** The seat this row is, when it pays for a model — PRD provider-logins
   *  §10.1. Absent on a secret and on a server that predates the tab. */
  login?: ProviderLogin | null
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
  envVarName: string
  /** "explicit" (an agent_credentials row) or "crew" (inherited via the crew). */
  grantSource: string
  expiresAt?: string
  expired: boolean
}

interface CredentialDependents {
  routines: { slug: string; name: string; type: string; resolution: "would_resolve" | "another_credential" | "unavailable" }[]
  recorded_use: "not_attributed"
  visibility_limited: boolean
  dynamic_uses_untracked: boolean
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
  /** Replace value: "rotate" (with a grace period) when allowed, else "patch". */
  onRotate: (cred: CredentialSummary, mode?: "rotate" | "patch") => void
  /** Returns to the list. Renders the back link when supplied. */
  onBack?: () => void
  /** The back link's word — the space the page belongs to. */
  backLabel?: string
  /** The open tab (`?tab=`); null or unknown is Overview. */
  tab?: SecretTab | null
  onTabChange?: (tab: SecretTab) => void
  /** Tags in use across the workspace, for the Settings tab. */
  knownTags?: string[]
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
  /** Provider logins only: opens the wizard's sign-in step for this seat's
   *  provider and mode. Without it the Re-login action is not offered. */
  onRelogin?: (cred: CredentialSummary) => void
}

export function CredentialDetailSheet({
  workspaceId, credential, open, onOpenChange, onRefresh, onRotate, onBack, backLabel = "Secrets",
  tab = null, onTabChange, knownTags = [],
  toolGaps = [], crewsById = {}, onRelogin,
}: CredentialDetailSheetProps) {
  const [audit, setAudit] = React.useState<AuditEvent[]>([])
  const [auditLoading, setAuditLoading] = React.useState(false)
  const [auditError, setAuditError] = React.useState(false)
  const [auditExpanded, setAuditExpanded] = React.useState(false)
  const [tierSaving, setTierSaving] = React.useState(false)
  const [renewing, setRenewing] = React.useState(false)
  const [testing, setTesting] = React.useState(false)
  const [testResult, setTestResult] = React.useState<{ valid: boolean; supported?: boolean; error?: string } | null>(null)
  // Older servers may omit `supported`; a positive `valid` without proof of a
  // probe must never render as a green verification result.
  const testUnknown = Boolean(testResult && (testResult.supported === false || (testResult.valid && testResult.supported !== true)))
  const testPassed = Boolean(testResult?.valid && !testUnknown)
  const [fieldsRead, setFieldsRead] = React.useState<{ credentialId: string; rows: CredentialFieldRow[] } | null>(null)
  const fields = fieldsRead && fieldsRead.credentialId === credential?.id ? fieldsRead.rows : []
  const [fieldsLoading, setFieldsLoading] = React.useState(false)
  const [fieldsError, setFieldsError] = React.useState(false)
  const [dependentsRead, setDependentsRead] = React.useState<{
    credentialId: string
    status: "loading" | "ready" | "error"
    data?: CredentialDependents
  } | null>(null)
  const [bindings, setBindings] = React.useState<BindingRow[]>([])
  const [assignments, setAssignments] = React.useState<AssignmentRow[]>([])
  const [accessLoading, setAccessLoading] = React.useState(false)
  const [accessError, setAccessError] = React.useState(false)
  const [accessCoverage, setAccessCoverage] = React.useState({ checked: 0, total: 0, unavailable: 0 })
  const [accessReload, setAccessReload] = React.useState(0)
  const [revealOpen, setRevealOpen] = React.useState(false)
  // The classification a PUT .../sensitivity last returned. Starts unset and
  // takes precedence over the value the list/get payload carried, so the pill
  // and the SEALED gate follow a change made in this sheet without a refetch.
  const [sensitivity, setSensitivity] = React.useState<string | null>(null)
  const [sensitivitySaving, setSensitivitySaving] = React.useState(false)
  const [sensitivityError, setSensitivityError] = React.useState<string | null>(null)
  // The `login` a Refresh-now returned. Wins over the list payload so the
  // status pill and the expiry follow the refresh without a page reload.
  const [loginOverride, setLoginOverride] = React.useState<ProviderLogin | null>(null)
  const [giveOpen, setGiveOpen] = React.useState(false)

  // The server answers each action using its own scope, role and capability
  // gates. Until that answer arrives (or if it fails), actions stay hidden.
  const { access: effectiveAccess, loading: effectiveAccessLoading, error: effectiveAccessError, refresh: refreshEffectiveAccess } = useAccessMe(open && credential
    ? `/api/v1/credentials/${encodeURIComponent(credential.id)}/access/me?workspace_id=${encodeURIComponent(workspaceId)}`
    : undefined)
  const canAct = (key: string) => {
    const state = effectiveAccess?.actions[key]?.state
    return state === "allowed" || state === "conditional"
  }
  const canUpdate = canAct("edit")
  const canRotate = canAct("rotate")
  // A manager who may edit but not rotate still replaces the value — at once, without grace.
  const canReplace = canRotate || canUpdate
  const canDelete = canAct("delete")
  const canLowerSensitivity = canAct("lower_sensitivity")
  const canBind = canAct("manage_bindings")

  const effectiveSensitivity = sensitivity ?? credential?.sensitivity ?? null

  const canReveal = canAct("reveal") && effectiveSensitivity !== "SEALED"

  /**
   * Which of the four gates is shut, in the order they bind.
   *
   * Only meaningful for a reader who is otherwise allowed to act — below the
   * MANAGER floor there is nothing to explain, because reveal is not a thing
   * that tier does. SEALED is checked first because it is the one no
   * configuration can open: the answer there is rotation, not a setting.
   */
  const revealBlockedReason = ({
    sealed: "SEALED values cannot be revealed. Obtain a replacement from the provider to change this secret.",
    workspace_switch_off: "Reveal is switched off for this workspace. An owner can turn it on under Settings → Access & secrets.",
    missing_capability: "Revealing a value needs the credentials:reveal capability, which no role grants on its own.",
    below_role_floor: "Revealing a value requires at least a manager role.",
    outside_crew_scope: "This credential is outside your crew scope.",
    non_interactive_auth: "Revealing a value requires an interactive session.",
  } as Record<string, string>)[effectiveAccess?.actions.reveal?.reason ?? ""] ?? "Reveal access could not be determined."

  React.useEffect(() => {
    if (!open || !credential) {
      setAudit([])
      setAuditExpanded(false)
      setTestResult(null)
      setFieldsRead(null)
      setBindings([])
      setAssignments([])
      setSensitivity(null)
      setSensitivityError(null)
      setRevealOpen(false)
      setGiveOpen(false)
    }
    // A refresh's answer belongs to the seat it refreshed, not the next one
    // opened in the rail.
    setLoginOverride(null)
  }, [open, credential])

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
      setAuditError(false)
      apiFetch(`/api/v1/credentials/${cid}/audit?workspace_id=${ws}&limit=${AUDIT_FETCH_LIMIT}`)
	        .then((r) => { if (!r.ok) throw new Error(); return r.json() })
        .then((data: AuditEvent[]) => { if (!Array.isArray(data)) throw new Error(); if (!cancelled) setAudit(data) })
	        .catch(() => !cancelled && setAuditError(true))
        .finally(() => !cancelled && setAuditLoading(false))
    }

    setFieldsRead(null)
    setFieldsLoading(true)
    setFieldsError(false)
    apiFetch(`/api/v1/credentials/${cid}/fields?workspace_id=${ws}`)
	      .then((r) => { if (!r.ok) throw new Error(); return r.json() })
      .then((data: CredentialFieldRow[]) => { if (!Array.isArray(data)) throw new Error(); if (!cancelled) setFieldsRead({ credentialId: cid, rows: data }) })
	      .catch(() => { if (!cancelled) { setFieldsRead(null); setFieldsError(true) } })
      .finally(() => !cancelled && setFieldsLoading(false))

    setDependentsRead({ credentialId: cid, status: "loading" })
    apiFetch(`/api/v1/credentials/${cid}/dependents?workspace_id=${ws}`)
      .then((r) => { if (!r.ok) throw new Error(); return r.json() })
      .then((body: CredentialDependents) => {
        if (!Array.isArray(body?.routines)) throw new Error()
        if (!cancelled) setDependentsRead({ credentialId: cid, status: "ready", data: body })
      })
      .catch(() => { if (!cancelled) setDependentsRead({ credentialId: cid, status: "error" }) })

    setAccessLoading(true); setAccessError(false); setBindings([]); setAssignments([])
    apiFetch(`/api/v1/credentials/bindings?workspace_id=${ws}&credential_id=${cid}`)
      .then(async (r) => { if (!r.ok) throw new Error(); return r.json() })
      .then(async (body: { bindings?: BindingRow[] }) => {
        const slots = Array.isArray(body?.bindings) ? body.bindings : []
        if (!cancelled) setBindings(slots)
        return loadAssignments(workspaceId, credential, slots)
      })
      .then((result) => { if (!cancelled) { setAssignments(result.rows); setAccessCoverage(result) } })
      .catch(() => { if (!cancelled) setAccessError(true) })
      .finally(() => { if (!cancelled) setAccessLoading(false) })

    return () => {
      cancelled = true
    }
  }, [open, credential, workspaceId, canRotate, canUpdate, accessReload])

  if (!credential) return null

  // Keep a previous credential's routine names out of the next sheet even
  // for the render before its effect starts a new request.
  const dependentsLoading = dependentsRead?.credentialId !== credential.id || dependentsRead.status === "loading"
  const dependentsError = dependentsRead?.credentialId === credential.id && dependentsRead.status === "error"
  const dependents = dependentsRead?.credentialId === credential.id && dependentsRead.status === "ready" ? dependentsRead.data : null

  // The seat view. `loginOverride` is what the last Refresh-now returned.
  const loginRow = loginOverride ? { ...credential, login: loginOverride } : credential
  const seat = hasLogin(loginRow) ? loginRow : null
  const accountId = fields.find((f) => f.key === "account_id" && !f.is_secret)?.value ?? null

  const brand = getBrand(credential.provider)
  const tierLevel: CredentialTierLevel | null = tierOf(credential)

  // The server ships agent ids and names as two parallel arrays (splitAgentRefs),
  // so a binding that names an agent by id can still be shown by name.
  const agentNameById = new Map<string, string>(
    (credential.agent_ids ?? []).map((id, i) => [id, credential.agent_names[i] ?? id]),
  )

  const shownAudit = auditExpanded ? audit : audit.slice(0, AUDIT_PREVIEW)

  const missingTools = Array.from(new Set(toolGaps.map((g) => g.tool).filter(Boolean)))
  // Keep the summary aligned with the provider's refreshed login metadata.
  const expiryDays = daysUntilExpiry(seat ? { ...credential, token_expires_at: seat.login.expires_at } : credential)

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
      setTestResult({ valid: data.valid, supported: data.supported, error: data.error })
    } catch {
      setTestResult({ valid: false, error: "Network error" })
    } finally {
      setTesting(false)
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
      void refreshEffectiveAccess()
    } catch {
      setSensitivityError("Network error")
    } finally {
      setSensitivitySaving(false)
    }
  }

  const isSecret = !seat
  const replaceValue = () => onRotate(credential, canRotate ? "rotate" : "patch")
  const tabs: { id: SecretTab; label: string }[] = [
    { id: "overview", label: "Overview" },
    { id: "access", label: seat ? "Pays for" : "Access" },
    ...(isSecret ? [{ id: "history" as const, label: "Value history" }] : []),
    ...(canUpdate ? [{ id: "activity" as const, label: "Activity" }] : []),
    { id: "settings", label: "Settings" },
  ]
  const activeTab: SecretTab = tabs.some((t) => t.id === tab) ? (tab as SecretTab) : "overview"
  const showTab = (t: SecretTab) => onTabChange?.(t)
  const state = deriveSecretState(seat ? { ...credential, token_expires_at: seat.login.expires_at ?? credential.token_expires_at } : credential)
  const classification = effectiveSensitivity ? effectiveSensitivity.toUpperCase() : null
  const crewNames = (credential.crew_ids ?? []).map((id) => crewsById[id]?.name ?? id)

  const setTier = async (level: number) => {
    if (tierSaving || level === tierLevel) return
    setTierSaving(true)
    try {
      const res = await apiFetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ security_level: level }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        toastSwitchError("Keeper tier", typeof data?.error === "string" ? data.error : `HTTP ${res.status}`, () => void setTier(level))
        return
      }
      onRefresh()
    } catch {
      toastSwitchError("Keeper tier", "Network error", () => void setTier(level))
    } finally {
      setTierSaving(false)
    }
  }

  const renew = async () => {
    setRenewing(true)
    try {
      const res = await apiFetch(`/api/v1/credentials/${credential.id}/refresh?workspace_id=${workspaceId}`, { method: "POST" })
      if (!res.ok) { toast.error("Couldn't renew it now.", { description: "Replace the value instead, or try again later." }); return }
      toast.success("Renewed")
      onRefresh()
    } catch {
      toast.error("Couldn't renew it now.")
    } finally {
      setRenewing(false)
    }
  }

  const testBadge = testResult && (
    <span
      className={cn(
        "inline-flex items-center gap-1 text-label",
        testUnknown ? "text-muted-foreground" : testPassed ? "text-success" : "text-destructive",
      )}
    >
      {testUnknown ? <Info className="size-3" /> : testPassed ? <CheckCircle2 className="size-3" /> : <XCircle className="size-3" />}
      {testUnknown ? "Not checked" : testPassed ? "Valid" : testResult.error || "Invalid"}
    </span>
  )

  const fieldsBlock = (fieldsLoading || fieldsError || fields.length > 0) ? (
    <div className="mt-3 border-t border-border pt-3" aria-label="Fields">
      {fieldsLoading ? (
        <p role="status" className="text-label text-muted-foreground">Checking fields…</p>
      ) : fieldsError ? (
        <p role="alert" className="text-label text-warn">Fields could not be checked. No values are shown until this credential&apos;s fields can be checked.</p>
      ) : (
        <dl className="grid gap-x-6 gap-y-1.5 sm:grid-cols-2">
          {fields.map((f) => (
            <div key={f.key} className="flex items-baseline gap-2 text-label">
              <dt className="shrink-0 font-mono text-foreground/90">{f.key}</dt>
              <dd className="min-w-0 flex-1 truncate text-right">
                {f.is_secret ? <Badge variant="outline" className="border-warn/40 px-1 text-micro text-warn">secret</Badge> : <span className="font-mono text-muted-foreground">{f.value}</span>}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  ) : null

  // ── Overview ───────────────────────────────────────────────────────────
  const overview = (
    <div className="flex flex-col gap-4">
      {isSecret && state.key === "rejected" && (
        <div role="alert" className="flex flex-wrap items-center gap-3 rounded-card border border-destructive/30 bg-chip-danger-bg px-4 py-3">
          <AlertTriangle className="size-4 shrink-0 text-destructive" aria-hidden />
          <div className="min-w-0 flex-1">
            <b className="text-control font-semibold text-foreground">Rejected</b>
            <p className="font-mono text-label text-foreground/85">{credential.last_error || "The service refused this value, or it has expired."}</p>
          </div>
          {canReplace && <Button size="sm" onClick={() => replaceValue()}><RefreshCw className="size-3.5" aria-hidden /> Replace value</Button>}
        </div>
      )}
      {isSecret && state.key === "expiring" && expiryDays !== null && (
        <div role="status" className="flex flex-wrap items-center gap-3 rounded-card border border-warn/30 bg-chip-warn-bg px-4 py-3">
          <Clock className="size-4 shrink-0 text-warn" aria-hidden />
          <div className="min-w-0 flex-1">
            <b className="text-control font-semibold text-foreground">Expires {expiryDays === 0 ? "today" : `in ${expiryDays} day${expiryDays === 1 ? "" : "s"}`}</b>
            <p className="text-label text-foreground/85">
              {credential.type === "OAUTH2" ? "It renews itself; if renewal fails you get an Inbox item." : "Agents that use it stop when it expires."}
            </p>
          </div>
          {credential.type === "OAUTH2" && canUpdate ? (
            <Button size="sm" variant="outline" onClick={renew} disabled={renewing}>{renewing ? <Spinner className="size-3" /> : <RefreshCw className="size-3.5" aria-hidden />} Renew now</Button>
          ) : canReplace ? (
            <Button size="sm" onClick={() => replaceValue()}><RefreshCw className="size-3.5" aria-hidden /> Replace</Button>
          ) : null}
        </div>
      )}

      {seat && (seat.login.refresh.status === "needs_relogin" || state.key === "rejected") && (
        <div role="alert" className="flex flex-wrap items-center gap-3 rounded-card border border-destructive/30 bg-chip-danger-bg px-4 py-3">
          <AlertTriangle className="size-4 shrink-0 text-destructive" aria-hidden />
          <div className="min-w-0 flex-1">
            <b className="text-control font-semibold text-foreground">Signed out</b>
            <p className="text-label text-foreground/85">
              {seat.login.refresh.error ? `${seat.login.refresh.error}. ` : ""}Agents that pay with it fall back to their crew&apos;s or the workspace account until someone signs in again.
            </p>
          </div>
          {canUpdate && onRelogin && <Button size="sm" onClick={() => onRelogin(credential)}>Sign in again</Button>}
        </div>
      )}
      {seat && (
        <ProviderLoginCards
          workspaceId={workspaceId}
          credential={seat}
          accountId={accountId}
          canUpdate={canUpdate}
          onLoginChange={(next) => {
            setLoginOverride(next)
            onRefresh()
          }}
        />
      )}
      {seat && fieldsBlock && <DetailCard title="Fields" icon={KeyRound}>{fieldsBlock}</DetailCard>}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        {isSecret && (
          <DetailCard
            title="Value"
            icon={KeyRound}
            subtitle="encrypted at rest"
            action={credential.testable && canUpdate ? (
              <span className="inline-flex items-center gap-2">
                {testBadge}
                <Button size="sm" variant="ghost" onClick={handleTest} disabled={testing}>
                  {testing ? <Spinner className="size-3" /> : <FlaskConical className="size-3.5" aria-hidden />}
                  Test
                </Button>
              </span>
            ) : undefined}
          >
            <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-background px-3 py-2">
              <span className="font-mono text-label text-muted-foreground">
                {credential.username ? `${credential.username} · ` : ""}••••••••••••••••
              </span>
              <span className="flex-1" />
              {canReveal && (
                <Button size="sm" variant="outline" onClick={() => setRevealOpen(true)}>
                  <Eye className="size-3.5" aria-hidden /> Reveal…
                </Button>
              )}
            </div>
            {canReveal ? (
              <p className="mt-1.5 text-label text-muted-foreground">
                Revealing needs a written reason and is recorded in the audit log before the value is returned.
              </p>
            ) : canUpdate ? (
              <p className="mt-1.5 flex items-start gap-1.5 text-label text-muted-foreground">
                <EyeOff className="mt-0.5 size-3 shrink-0" aria-hidden />
                <span>{revealBlockedReason}{effectiveSensitivity !== "SEALED" && <a className="ml-1 text-primary underline" href="/settings?tab=access-secrets">Review access &amp; secrets settings</a>}</span>
              </p>
            ) : null}
            <p className="mt-1.5 flex items-center gap-1.5 text-label text-muted-foreground" data-testid="connection-verification">
              <FlaskConical className="size-3 shrink-0" aria-hidden />
              {testResult
                ? testUnknown ? "Connection was not checked by this test." : testPassed ? "Connection check passed just now." : "Connection check did not pass. See the test result."
                : credential.testable && credential.last_checked_at && credential.status === "ACTIVE" && !credential.last_error
                  ? `Last connection check passed ${formatRelativeTime(credential.last_checked_at)}. This does not guarantee the next request will succeed.`
                  : "Connection not verified."}
            </p>
            <dl className="mt-3 grid gap-x-6 gap-y-1.5 text-label sm:grid-cols-2">
              <Fact label="Agents get it as">
                {bindings.length > 0 ? (
                  <span className="font-mono">{Array.from(new Set(bindings.map((b) => b.slot))).join(", ")}</span>
                ) : (
                  <span className="text-muted-foreground">No variable set yet</span>
                )}
              </Fact>
              <Fact label="Last used">{credential.last_used_at ? formatRelativeTime(credential.last_used_at) : "never"}</Fact>
              <Fact label="Expires">{credential.token_expires_at ? formatDate(credential.token_expires_at) : "never"}</Fact>
              <Fact label="Added">{formatDate(credential.created_at)}</Fact>
              {credential.last_error && state.key !== "rejected" && (
                <Fact label="Last error"><span className="font-mono text-destructive">{credential.last_error}</span></Fact>
              )}
              {credential.last_used_ips.length > 0 && <Fact label="Last IPs"><span className="font-mono">{credential.last_used_ips.join(", ")}</span></Fact>}
            </dl>
            {fieldsBlock}
          </DetailCard>
        )}

        <DetailCard
          title={seat ? "Pays for" : "Used by"}
          icon={Users}
          subtitle={seat ? paysForLabel(seat.login.pays_for) : credential.agent_names.length > 0 ? String(credential.agent_names.length) : undefined}
          action={<button type="button" className="text-label text-primary hover:underline" onClick={() => showTab("access")}>{seat ? "Manage" : "Who gets it ›"}</button>}
        >
          {credential.agent_names.length > 0 ? (
            <ul className="flex flex-col gap-1.5" aria-label="Assigned agents">
              {credential.agent_names.map((name, i) => (
                <li key={`${name}-${i}`} className="flex items-center gap-2 text-control">
                  <AgentAvatar seed={credential.agent_ids?.[i] ?? name} className="size-5 shrink-0" alt="" />
                  <span className="truncate">{name}</span>
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-label text-muted-foreground">No agent has it yet.</p>
          )}
          <div className="mt-3 border-t border-border pt-3" aria-label="Routine dependencies">
            <p className="text-label font-medium">Routine references</p>
            {dependentsLoading && <p role="status" className="text-label text-muted-foreground">Checking routine definitions…</p>}
            {dependentsError && <p role="alert" className="text-label text-warn">Routine dependencies could not be checked. The list may be incomplete.</p>}
            {!dependentsLoading && !dependentsError && dependents?.routines.length === 0 && (
              <p className="text-label text-muted-foreground">No visible routine declares this credential type. Dynamic agent and script use is not tracked here.</p>
            )}
            {!dependentsLoading && !dependentsError && dependents && dependents.routines.length > 0 && (
              <ul className="mt-1 flex flex-col gap-1 text-label">
                {dependents.routines.map((item) => (
                  <li key={item.slug} className="flex items-center justify-between gap-2">
                    <Link href={`/routines?slug=${encodeURIComponent(item.slug)}`} className="truncate text-primary hover:underline">{item.name || item.slug}</Link>
                    <span className="shrink-0 text-muted-foreground">{item.resolution === "would_resolve" ? "Would select this credential" : item.resolution === "another_credential" ? "Selects another credential" : "No active match"}</span>
                  </li>
                ))}
              </ul>
            )}
            {dependents?.visibility_limited && <p className="text-label text-muted-foreground">Hidden routines are excluded from your view.</p>}
            <p className="mt-1 text-micro text-muted-foreground">Configured references and current selection are estimates, not recorded run use.</p>
          </div>
          {credential.mcp_used && <p className="mt-3 text-label text-muted-foreground">Also referenced by one or more MCP server integrations.</p>}
        </DetailCard>
      </div>

      {toolGaps.length > 0 && (
        <DetailCard title="Tool availability" icon={PackageX} tone="warn">
          <p className="flex items-center gap-2 text-control text-warn">
            <PackageX className="size-3.5 shrink-0" aria-hidden />
            {missingTools.length > 0 ? `Needs ${missingTools.join(", ")}` : "A tool is missing"}
          </p>
          <ul className="mt-2 flex flex-col gap-1">
            {toolGaps.map((gap) => (
              <li key={`${gap.crewId}-${gap.tool}`} className="text-label text-muted-foreground">
                <span className="text-foreground/80">{gap.crewName}</span>
                {gap.featureId && <>{" — add "}<code className="font-mono text-foreground/70">{gap.featureId}</code>{" and rebuild"}</>}
              </li>
            ))}
          </ul>
        </DetailCard>
      )}
    </div>
  )

  // ── Access (secret) / Pays for (seat) ───────────────────────────────────
  const access = (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 lg:grid-cols-2">
        <DetailCard title="Who can see it" icon={ShieldCheck}>
          <p className="text-body">
            {credential.scope === "CREW"
              ? <>Selected crews{crewNames.length > 0 ? `: ${crewNames.join(", ")}` : ""}.</>
              : "The whole workspace."}
            {canUpdate && <button type="button" className="ml-1 text-primary hover:underline" onClick={() => showTab("settings")}>Change in Settings</button>}
          </p>
          <p className="mt-2 text-label text-muted-foreground">
            {seat ? "Only workspace owners and admins can view and manage provider accounts." : "Owners and admins manage secrets; managers can edit them. Reading a value needs reveal permission and workspace policy."}
          </p>
          <div className="mt-3">
            <YourAccess
              provided={{ access: effectiveAccess, loading: effectiveAccessLoading, error: effectiveAccessError }}
              actions={[{ key: "read", label: "Read" }, { key: "edit", label: "Edit" }, { key: "manage_bindings", label: "Manage assignments" }, { key: "reveal", label: "Reveal" }]}
            />
          </div>
        </DetailCard>

        <DetailCard
          title={seat ? "Pays for" : "Who gets it at run time"}
          icon={Users}
          action={!seat && canBind ? <Button size="sm" variant="outline" onClick={() => setGiveOpen(true)}>Give to agents</Button> : undefined}
          footer={seat
            ? "Assignments decide which agents can use this account. Automatic account switching is not available yet."
            : "“See” and “get” are different: a crew can use a secret without anyone in it being able to reveal it."}
        >
          {accessLoading && <p role="status" className="mb-2 text-label text-muted-foreground">Checking visible assignments…</p>}
          {accessError ? <p role="alert" className="mb-2 text-label text-warn">Access could not be fully loaded. An empty list does not mean nobody has access.</p> : accessCoverage.unavailable > 0 ? <p role="alert" className="mb-2 text-label text-warn">Some assignments could not be loaded. This list may be incomplete.</p> : null}
          {bindings.length > 0 && (
            <ul className="mb-3 flex flex-col gap-1.5" aria-label="Delivery bindings">
              {bindings.map((b) => {
                const crew = b.crew_id ? crewsById[b.crew_id] : undefined
                const agentName = b.agent_id ? agentNameById.get(b.agent_id) : undefined
                return (
                  <li key={b.id} className="flex items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-label">
                    <Badge variant="outline" className="px-1 text-micro">{b.scope}</Badge>
                    <span className="font-mono">{b.slot}</span>
                    {b.crew_id && (
                      <span className="ml-auto inline-flex min-w-0 items-center gap-1.5">
                        <CrewIcon icon={crew?.icon ?? ""} color={crew?.color ?? undefined} size="sm" className="!size-4 !rounded shrink-0" />
                        <span className="truncate">{crew?.name ?? b.crew_id}</span>
                      </span>
                    )}
                    {!b.crew_id && b.agent_id && (
                      <span className="ml-auto inline-flex min-w-0 items-center gap-1.5">
                        <AgentAvatar seed={b.agent_id} className="size-4 shrink-0" alt="" />
                        <span className="truncate">{agentName ?? b.agent_id}</span>
                      </span>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
          {assignments.length > 0 ? (
            <ul className="flex flex-col gap-1.5" aria-label="Agent grants">
              {assignments.map((a) => (
                <li key={`${a.agentName}:${a.envVarName}:${a.grantSource}`} className="flex items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-label">
                  <AgentAvatar seed={a.agentName} className="size-4 shrink-0" alt="" />
                  <span className="truncate">{a.agentName}</span>
                  <span className="truncate font-mono text-micro text-muted-foreground">{a.envVarName}</span>
                  <Badge variant="outline" className="ml-auto shrink-0 px-1 text-micro"
                    title={a.grantSource === "crew" ? "Inherited from the crew — unlink the crew to take it away" : a.grantSource === "explicit" ? "Granted to this agent directly" : a.grantSource === "binding" ? "Resolved from a delivery binding" : "Grant source was not identified by this view"}>
                    {a.grantSource === "crew" ? "crew grant" : a.grantSource === "explicit" ? "explicit" : a.grantSource === "binding" ? "delivery binding" : "source unconfirmed"}
                  </Badge>
                  {a.expiresAt && <Badge variant="outline" className={cn("shrink-0 px-1 text-micro", a.expired ? "text-destructive" : "text-warn")}>{a.expired ? "lease expired" : "leased"}</Badge>}
                </li>
              ))}
            </ul>
          ) : credential.agent_names.length > 0 ? (
            <ul className="flex flex-col gap-1.5" aria-label="Agents holding it">
              {credential.agent_names.map((name, i) => (
                <li key={`${name}-${i}`} className="flex items-center gap-2 rounded-md border border-border bg-background px-3 py-2 text-label">
                  <AgentAvatar seed={credential.agent_ids?.[i] ?? name} className="size-4 shrink-0" alt="" />
                  {name}
                </li>
              ))}
            </ul>
          ) : bindings.length === 0 && !accessLoading && !accessError ? (
            <p className="text-label text-muted-foreground">
              No matching grants were reported by the checked agents.{isSecret && <> Give it with <code className="font-mono">crewship credential bind</code>.</>}
            </p>
          ) : null}
          {!accessLoading && !accessError && (
            <p data-testid="credential-access-summary" className="mt-2 text-label text-muted-foreground">Checked {accessCoverage.checked} of {accessCoverage.total} candidate agents visible to you.{accessCoverage.unavailable > 0 ? ` ${accessCoverage.unavailable} lookups unavailable.` : ""}</p>
          )}
        </DetailCard>
      </div>

      {canUpdate && (
        <DetailCard title="Protection" icon={ShieldCheck}>
          <div className="flex flex-col divide-y divide-border">
            {isSecret && (
              <div className="flex flex-wrap items-center gap-3 pb-3">
                <div className="min-w-0 flex-1">
                  <b className="text-control font-medium">Keeper tier</b>
                  <p className="text-label text-muted-foreground">{tierLevel ? tierMeta(tierLevel).consequence : "This server did not report a tier."}</p>
                </div>
                <div role="radiogroup" aria-label="Keeper tier" className="inline-flex rounded-md border border-border bg-muted/40 p-0.5">
                  {[1, 2, 3, 4].map((n) => (
                    <button key={n} type="button" role="radio" aria-checked={tierLevel === n} disabled={tierSaving} onClick={() => setTier(n)}
                      className={cn("kit-tap h-7 rounded px-2.5 font-mono text-label", tierLevel === n ? "bg-card font-semibold text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
                      L{n}
                    </button>
                  ))}
                </div>
              </div>
            )}
            <div className={cn("flex flex-wrap items-center gap-3", isSecret && "pt-3")}>
              <div className="min-w-0 flex-1">
                <b className="text-control font-medium">Classification</b>
                <p className="text-label text-muted-foreground">
                  {effectiveSensitivity === null
                    ? "The current classification is not reported by the credentials API — picking one sets it."
                    : effectiveSensitivity === "SEALED"
                      ? "SEALED values cannot be revealed. Obtain a replacement from the provider to change this secret."
                      : "Raise it at any time; lowering it is an audited, admin-only action."}
                </p>
                {sensitivityError && <span className="mt-1 inline-flex items-center gap-1 text-label text-destructive"><XCircle className="size-3" />{sensitivityError}</span>}
              </div>
              <div role="radiogroup" aria-label="Classification" className="inline-flex rounded-md border border-border bg-muted/40 p-0.5">
                {SENSITIVITY_LEVELS.map((level) => {
                  const current = effectiveSensitivity
                  const lowering = current !== null && SENSITIVITY_LEVELS.indexOf(level) < SENSITIVITY_LEVELS.indexOf(current as (typeof SENSITIVITY_LEVELS)[number])
                  const blocked = lowering && !canLowerSensitivity
                  return (
                    <button key={level} type="button" role="radio" aria-checked={level === current} aria-pressed={level === current}
                      disabled={sensitivitySaving || blocked || level === current} onClick={() => setClassification(level)}
                      title={blocked ? "Lowering a classification is a workspace-admin action" : undefined}
                      className={cn("kit-tap h-7 rounded px-2.5 text-label disabled:opacity-60", level === current ? "bg-card font-semibold text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
                      {level}
                    </button>
                  )
                })}
              </div>
            </div>
          </div>
        </DetailCard>
      )}

      {!(canUpdate && canRotate && canDelete) && (
        <DetailCard title="Permissions">
          <p className="text-label text-muted-foreground">
            {!canUpdate && !canRotate
              ? "You don't have permission to modify this credential."
              : !canUpdate
                ? "You can replace this secret value. Editing its details requires a workspace manager."
                : "Deleting this credential requires a workspace admin."}
          </p>
        </DetailCard>
      )}
    </div>
  )

  // ── Activity ───────────────────────────────────────────────────────────
  const activity = (
    <DetailCard
      title="Activity"
      icon={Activity}
      subtitle={audit.length === 0 ? undefined : auditExpanded || audit.length <= AUDIT_PREVIEW ? `${audit.length}${audit.length === AUDIT_FETCH_LIMIT ? "+" : ""}` : `${AUDIT_PREVIEW} of ${audit.length}${audit.length === AUDIT_FETCH_LIMIT ? "+" : ""}`}
      action={audit.length > AUDIT_PREVIEW ? (
        <button type="button" onClick={() => setAuditExpanded((v) => !v)} className="text-label text-primary hover:underline">
          {auditExpanded ? "Show less" : `Show all ${audit.length}`}
        </button>
      ) : undefined}
    >
      {auditLoading ? (
        <p role="status" className="text-label text-muted-foreground">Loading…</p>
      ) : auditError ? (
        <p role="alert" className="text-label text-warn">Activity could not be checked. Previously loaded events may be stale.</p>
      ) : audit.length === 0 ? (
        <p className="text-label text-muted-foreground">Nothing has happened to this credential yet.</p>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {shownAudit.map((e) => (
            <li key={e.id} className="flex flex-wrap items-center gap-2 rounded-md px-1.5 py-1.5 text-label hover:bg-muted/60">
              <AuditActor event={e} crewsById={crewsById} />
              <Badge variant="outline" className="px-1.5 font-mono text-micro">{e.event_type}</Badge>
              {e.ip_address && <span className="font-mono text-micro text-muted-foreground-soft">{e.ip_address}</span>}
              <span className="ml-auto text-micro text-muted-foreground-soft">{formatRelativeTime(e.occurred_at)}</span>
            </li>
          ))}
        </ul>
      )}
    </DetailCard>
  )

  const body =
    activeTab === "access" && seat ? (
      <PaysForPanel workspaceId={workspaceId} credential={seat} canBind={canBind} crewsById={crewsById} onChanged={onRefresh} />
    )
      : activeTab === "access" ? access
      : activeTab === "history" ? (
        <SecretValueHistory workspaceId={workspaceId} credentialId={credential.id} canRotate={canRotate} onReplace={() => replaceValue()} revision={credential.updated_at ? Date.parse(credential.updated_at) : 0} />
      )
      : activeTab === "activity" ? activity
      : activeTab === "settings" ? (
        <SecretSettings
          workspaceId={workspaceId}
          credential={credential}
          isLogin={Boolean(seat)}
          canUpdate={canUpdate}
          canDelete={canDelete}
          crewsById={crewsById}
          knownTags={knownTags}
          fields={fields}
          onRefresh={onRefresh}
          onDeleted={() => { onRefresh(); onOpenChange(false) }}
          onSignInAgain={seat && canUpdate && onRelogin ? () => onRelogin(credential) : undefined}
          dependents={dependentsError ? "error" : dependents ?? null}
        />
      )
      : overview

  return (
    <>
      <div className="flex h-full flex-col" data-slot="secret-page">
        <div className="min-h-0 flex-1 overflow-y-auto">
          <div className="mx-auto flex w-full max-w-6xl flex-col gap-4 p-4 md:p-6">
            {onBack && (
              <button type="button" onClick={onBack} className="kit-tap inline-flex w-fit items-center gap-1 text-label text-muted-foreground hover:text-foreground">
                <ChevronLeft className="size-3.5" aria-hidden /> {backLabel}
              </button>
            )}
            <header className="flex flex-wrap items-start gap-4">
              <BrandTile provider={credential.provider} size={52} />
              <div className="min-w-0 flex-1">
                <h1 className="truncate text-title font-semibold tracking-tight text-foreground">{credential.name}</h1>
                <p className="mt-0.5 text-body text-muted-foreground">
                  {credentialTypeLabel(credential.type)} · {brand.label}
                  {credential.account_label ? ` · ${credential.account_label}` : ""} · added {formatDate(credential.created_at)}
                </p>
                <div className="mt-2 flex flex-wrap items-center gap-1.5">
                  <StatusPill tone={state.tone} label={state.label} />
                  <span className="inline-flex items-center gap-1 rounded-md border border-border px-1.5 py-0.5 text-label text-muted-foreground">
                    <Layers className="size-3" aria-hidden /> {credential.scope === "CREW" ? "Selected crews" : "Workspace"}
                  </span>
                  {isSecret && <CredentialTierBadge level={tierLevel} serverLabel={credential.security_level_label} />}
                  {classification && (
                    <span className="inline-flex items-center gap-1 rounded-md border border-border px-1.5 py-0.5 text-label text-muted-foreground">
                      <Lock className="size-3" aria-hidden /> {classification[0] + classification.slice(1).toLowerCase()}
                    </span>
                  )}
                  {(credential.tags ?? []).map((t) => (
                    <span key={t} className={cn("inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-label", credentialTagClassName(t))}>
                      <Hash className="size-3" aria-hidden />{t}
                    </span>
                  ))}
                  {brand.cli && <Pill tone="blue"><TerminalSquare className="size-3" />CLI</Pill>}
                </div>
              </div>
              <div className="flex flex-wrap items-center gap-1.5" data-slot="secret-actions">
                {seat ? (
                  <ProviderLoginActions
                    credential={seat}
                    testable={Boolean(credential.testable)}
                    testing={testing}
                    canUpdate={canUpdate}
                    canBind={canBind}
                    onPayFor={() => showTab("access")}
                    onTest={handleTest}
                    onRelogin={onRelogin ? () => onRelogin(credential) : undefined}
                  />
                ) : (
                  <>
                    {canReplace && (
                      <Button size="sm" variant="ghost" onClick={() => replaceValue()}>
                        <RefreshCw className="size-3.5" aria-hidden /> Replace value
                      </Button>
                    )}
                    <Button size="sm" onClick={() => (canBind ? setGiveOpen(true) : showTab("access"))}>
                      <Bot className="size-3.5" aria-hidden /> Give to agents
                    </Button>
                  </>
                )}
              </div>
            </header>
            {seat && testBadge}

            <TabBar value={activeTab} onValueChange={(v) => showTab(v as SecretTab)} ariaLabel="Secret sections" className="overflow-x-auto">
              {tabs.map((t) => <TabBar.Item key={t.id} value={t.id}>{t.label}</TabBar.Item>)}
            </TabBar>
            <div role="tabpanel" aria-label={tabs.find((t) => t.id === activeTab)?.label}>{body}</div>
          </div>
        </div>
      </div>

      {!seat && canBind && (
        <GiveToAgentsDialog
          workspaceId={workspaceId}
          credential={credential}
          open={giveOpen}
          onOpenChange={setGiveOpen}
          crewsById={crewsById}
          onGiven={() => {
            // The Access tab shows the new rows; the list's "Used by" follows the refresh.
            setAccessReload((n) => n + 1)
            onRefresh()
          }}
        />
      )}

      <RevealDialog
        workspaceId={workspaceId}
        credentialId={credential.id}
        credentialName={credential.name}
        sensitivity={effectiveSensitivity}
        open={revealOpen}
        onOpenChange={setRevealOpen}
        onRotateInstead={canReplace ? () => {
          setRevealOpen(false)
          replaceValue()
        } : undefined}
      />
    </>
  )
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline gap-2">
      <dt className="w-28 shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 truncate text-foreground/85">{children}</dd>
    </div>
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
  bindings: BindingRow[],
): Promise<{ rows: AssignmentRow[]; checked: number; total: number; unavailable: number }> {
  const res = await apiFetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`)
  if (!res.ok) throw new Error("Agents unavailable")
  const agents = await res.json()
  if (!Array.isArray(agents)) throw new Error("Agents unavailable")

  const crewIds = new Set(credential.crew_ids ?? [])
  const names = new Set(credential.agent_names ?? [])
  const ids = new Set([...(credential.agent_ids ?? []), ...bindings.flatMap((b) => b.agent_id ? [b.agent_id] : [])])
  for (const b of bindings) if (b.crew_id) crewIds.add(b.crew_id)
  const workspaceBinding = bindings.some((b) => b.scope.toUpperCase() === "WORKSPACE")
  const allCandidates = (agents as { id?: string; name?: string; crew_id?: string | null }[])
    .filter((a) => typeof a?.id === "string")
    .filter((a) => workspaceBinding || ids.has(a.id!) || names.has(a.name ?? "") || (a.crew_id ? crewIds.has(a.crew_id) : false))
  const candidates = allCandidates.slice(0, MAX_ASSIGNMENT_LOOKUPS)
  let unavailable = 0

  const rows: AssignmentRow[] = []
  await Promise.all(
    candidates.map(async (agent) => {
      try {
        const r = await apiFetch(
          `/api/v1/agents/${encodeURIComponent(agent.id!)}/credentials` +
            `?workspace_id=${encodeURIComponent(workspaceId)}`,
        )
        if (!r.ok) { unavailable++; return }
        const list = await r.json()
        if (!Array.isArray(list)) { unavailable++; return }
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
            envVarName: row.env_var_name ?? "",
            grantSource: row.grant_source ?? "unknown",
            expiresAt: row.expires_at,
            expired: Boolean(row.expired),
          })
        }
      } catch {
        unavailable++
        // One unreachable agent must not blank the whole list.
      }
    }),
  )
  return { rows: rows.sort((a, b) => a.agentName.localeCompare(b.agentName)), checked: candidates.length - unavailable, total: allCandidates.length, unavailable }
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
