"use client"

import * as React from "react"
import { motion } from "motion/react"
import {
  Activity,
  Settings as SettingsIcon,
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
  ListTree,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { formatDate, formatRelativeTime } from "@/lib/time"
import { getBrand } from "@/lib/credential-providers/registry"
import { Capability } from "@/lib/capabilities"
import { useAbilities } from "@/hooks/use-abilities"
import { cn } from "@/lib/utils"
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
   * in internal/api/credentials.go has no `sensitivity` field, even though the
   * column exists and PUT .../sensitivity writes it. So this is normally
   * undefined, and every consumer below has to treat "unknown" as its own
   * state rather than as STANDARD. The moment the field is added to the read
   * payload, the gating here starts working with no other change.
   */
  sensitivity?: string | null
}

interface AuditEvent {
  id: string
  event_type: string
  agent_id: string | null
  ip_address: string | null
  metadata: Record<string, unknown> | null
  occurred_at: string
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
  envVarName: string
  /** "explicit" (an agent_credentials row) or "crew" (inherited via the crew). */
  grantSource: string
  expiresAt?: string
  expired: boolean
}

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
}

export function CredentialDetailSheet({
  workspaceId, credential, open, onOpenChange, onRefresh, onRotate, onEdit,
}: CredentialDetailSheetProps) {
  const [tab, setTab] = React.useState<"overview" | "fields" | "used-by" | "audit" | "settings">("overview")
  const [audit, setAudit] = React.useState<AuditEvent[]>([])
  const [auditLoading, setAuditLoading] = React.useState(false)
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
  // The classification the server last told us about. GET /credentials does
  // not carry it (see CredentialSummary.sensitivity), so this starts unknown
  // and becomes authoritative the moment a PUT answers.
  const [sensitivity, setSensitivity] = React.useState<string | null>(null)
  const [sensitivitySaving, setSensitivitySaving] = React.useState(false)
  const [sensitivityError, setSensitivityError] = React.useState<string | null>(null)
  // Inline value rewrite — Vercel-parity manual rotation. Lives in the
  // Settings tab next to the full grace-overlap rotation flow.
  const [valueDraft, setValueDraft] = React.useState("")
  const [showValueDraft, setShowValueDraft] = React.useState(false)
  const [savingValue, setSavingValue] = React.useState(false)
  const [valueSaved, setValueSaved] = React.useState(false)
  const [valueError, setValueError] = React.useState<string | null>(null)

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

  React.useEffect(() => {
    if (!open || !credential) {
      setTab("overview")
      setAudit([])
      setRotations([])
      setTestResult(null)
      setFields([])
      setBindings([])
      setAssignments([])
      setSensitivity(null)
      setSensitivityError(null)
      setRevealEnabled(false)
      setRevealOpen(false)
      setValueDraft("")
      setShowValueDraft(false)
      setValueSaved(false)
      setValueError(null)
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

  React.useEffect(() => {
    if (!open || !credential) return
    if (tab === "audit" && canUpdate) {
      setAuditLoading(true)
      apiFetch(`/api/v1/credentials/${credential.id}/audit?workspace_id=${workspaceId}&limit=50`)
        .then((r) => r.ok ? r.json() : [])
        .then((data: AuditEvent[]) => setAudit(Array.isArray(data) ? data : []))
        .catch(() => setAudit([]))
        .finally(() => setAuditLoading(false))
    }
    if (tab === "fields") {
      setFieldsLoading(true)
      apiFetch(`/api/v1/credentials/${credential.id}/fields?workspace_id=${workspaceId}`)
        .then((r) => r.ok ? r.json() : [])
        .then((data: CredentialFieldRow[]) => setFields(Array.isArray(data) ? data : []))
        .catch(() => setFields([]))
        .finally(() => setFieldsLoading(false))
    }
    if (tab === "used-by") {
      apiFetch(`/api/v1/credentials/bindings?workspace_id=${workspaceId}&credential_id=${credential.id}`)
        .then((r) => r.ok ? r.json() : null)
        .then((body: { bindings?: BindingRow[] } | null) =>
          setBindings(Array.isArray(body?.bindings) ? body.bindings : []))
        .catch(() => setBindings([]))
      void loadAssignments(workspaceId, credential).then(setAssignments).catch(() => setAssignments([]))
    }
    if (tab === "settings" && canRotate) {
      // Rotation history is only rendered for users who can rotate —
      // skip the fetch entirely for everyone else.
      apiFetch(`/api/v1/credentials/${credential.id}/rotations?workspace_id=${workspaceId}`)
        .then((r) => r.ok ? r.json() : [])
        .then((data: RotationRow[]) => setRotations(Array.isArray(data) ? data : []))
        .catch(() => setRotations([]))
    }
  }, [tab, open, credential, workspaceId, canRotate, canUpdate])

  if (!credential) return null

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

  const handleDelete = async () => {
    const res = await apiFetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
      method: "DELETE",
    })
    if (res.ok) {
      onRefresh()
      onOpenChange(false)
    }
    setConfirmDelete(false)
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
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent side="right" className="sm:max-w-[520px] p-0 flex flex-col">
          <SheetHeader className="px-5 pt-4 pb-3 border-b border-white/10">
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <SheetTitle className="text-base font-mono truncate">{credential.name}</SheetTitle>
                  {getBrand(credential.provider).cli && (
                    <Badge
                      variant="outline"
                      className="text-[9px] px-1 font-mono shrink-0 border-info/50 text-info"
                      title="Crewship uses this credential to authenticate the agent's CLI inside the container"
                    >
                      CLI
                    </Badge>
                  )}
                  {effectiveSensitivity && (
                    <Badge
                      variant="outline"
                      className={cn(
                        "text-[9px] px-1 font-mono shrink-0",
                        effectiveSensitivity === "SEALED" && "border-destructive/50 text-destructive",
                        effectiveSensitivity === "RESTRICTED" && "border-warn/50 text-warn",
                      )}
                    >
                      {effectiveSensitivity}
                    </Badge>
                  )}
                </div>
                <SheetDescription className="text-xs truncate">
                  {credential.account_label || credential.description || credential.provider}
                </SheetDescription>
              </div>
              {onEdit && canUpdate && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => onEdit(credential)}
                  className="shrink-0"
                >
                  <Pencil className="h-3 w-3 mr-1.5" />
                  Edit
                </Button>
              )}
            </div>
            {credential.tags && credential.tags.length > 0 && (
              <div className="flex flex-wrap gap-1 pt-1">
                {credential.tags.map((t) => (
                  <Badge key={t} variant="outline" className="text-[10px] px-1 font-mono">{t}</Badge>
                ))}
              </div>
            )}
          </SheetHeader>

          <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)} className="flex-1 flex flex-col">
            <TabsList className="px-3 mt-2 justify-start bg-transparent border-b border-white/10 rounded-none h-9">
              <TabsTrigger value="overview" className="text-xs">Overview</TabsTrigger>
              <TabsTrigger value="fields" className="text-xs">
                <ListTree className="h-3 w-3 mr-1" />Fields
              </TabsTrigger>
              <TabsTrigger value="used-by" className="text-xs">
                Used by{credential._count_agent_credentials > 0 && (
                  <Badge variant="secondary" className="ml-1.5 h-4 text-[10px] px-1.5">{credential._count_agent_credentials}</Badge>
                )}
              </TabsTrigger>
              {/* GET /audit is MANAGER+ — it exposes the IPs behind admin
                  actions. Rendering the tab for MEMBER/VIEWER meant a 403
                  degrading to the empty state, i.e. telling them the credential
                  has no history when they are merely not allowed to read it. */}
              {canUpdate && (
                <TabsTrigger value="audit" className="text-xs"><Activity className="h-3 w-3 mr-1" />Audit</TabsTrigger>
              )}
              <TabsTrigger value="settings" className="text-xs"><SettingsIcon className="h-3 w-3 mr-1" />Settings</TabsTrigger>
            </TabsList>

            <div className="flex-1 overflow-y-auto p-4">
              <TabsContent value="overview" className="m-0 space-y-3">
                <Field label="Type">{credential.type.replace(/_/g, " ")}</Field>
                <Field label="Provider">{credential.provider}</Field>
                {credential.username && (
                  // USERPASS only — username is cleartext (it's an
                  // identifier, not a secret). The password lives in
                  // encrypted_value and is never returned by any
                  // read endpoint, so we don't even need to mask
                  // anything on this tab.
                  <Field label="Username">
                    <span className="font-mono">{credential.username}</span>
                  </Field>
                )}
                <Field label="Scope">{credential.scope}</Field>
                <Field label="Created">{formatDate(credential.created_at)}</Field>
                {credential.token_expires_at && (
                  <Field label="Expires">{formatDate(credential.token_expires_at)}</Field>
                )}
                <Field label="Last used">
                  {credential.last_used_at ? (
                    <span className="inline-flex items-center gap-1.5">
                      <Clock className="h-3 w-3 opacity-60" />
                      {formatRelativeTime(credential.last_used_at)}
                    </span>
                  ) : (
                    <span className="text-muted-foreground">never</span>
                  )}
                </Field>
                {credential.last_error && (
                  <div className="rounded-md border border-destructive/30 bg-destructive/[0.05] p-3">
                    <div className="flex items-center gap-1.5 text-xs text-destructive font-medium">
                      <AlertTriangle className="h-3.5 w-3.5" />
                      Last error
                    </div>
                    <p className="text-xs text-foreground/80 mt-1 font-mono">{credential.last_error}</p>
                  </div>
                )}

                {credential.last_used_ips.length > 0 && (
                  <div className="space-y-1.5">
                    <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                      Last 5 IPs
                    </div>
                    <ul className="space-y-1">
                      {credential.last_used_ips.map((ip) => (
                        <li key={ip} className="text-xs font-mono text-foreground/80 flex items-center gap-2">
                          <span className="h-1 w-1 rounded-full bg-success/60" />
                          {ip}
                        </li>
                      ))}
                    </ul>
                  </div>
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
                {(canRotate || canReveal) && (
                  <div className="pt-3 border-t border-white/10 space-y-2">
                    <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                      Value
                    </div>
                    <div className="rounded-md border border-white/10 bg-background px-3 py-2 font-mono text-xs text-muted-foreground">
                      ••••••••••••••••
                    </div>
                    {canRotate && (
                      <>
                        <Button size="sm" className="w-full justify-start" onClick={() => onRotate(credential)}>
                          <RefreshCw className="h-3.5 w-3.5 mr-1.5" />
                          Rotate and show the new value
                        </Button>
                        <p className="text-[10px] text-muted-foreground">
                          Mints a new value, shows it once, and lets the old one drain through the
                          grace window. Nothing existing is disclosed.
                        </p>
                      </>
                    )}
                    {canReveal && (
                      <>
                        <Button
                          size="sm"
                          variant="ghost"
                          className="w-full justify-start text-[11px] text-muted-foreground hover:text-foreground -ml-2"
                          onClick={() => setRevealOpen(true)}
                        >
                          <Eye className="h-3 w-3 mr-1.5" />
                          Reveal the existing value…
                        </Button>
                        <p className="text-[10px] text-muted-foreground">
                          Requires a written reason and is recorded in the tamper-evident journal
                          before the value is returned.
                        </p>
                      </>
                    )}
                  </div>
                )}

                {/* Test now is only meaningful where the server maintains an
                    upstream probe (credential.testable — see
                    probeSupportedProviders) and requires update permission.
                    Mirrors the BE gating in TestStored — hiding the button
                    avoids a click → 403 dead-end for read-only members.
                    Deliberately NOT gated on brand .cli like the badge above:
                    that flag marks the CLIs Crewship drives in the container,
                    which excluded GitHub/GitLab/Vercel despite real probes. */}
                {credential.testable && canUpdate && (
                <div className="pt-3 border-t border-white/10 flex gap-2">
                  <Button size="sm" variant="outline" onClick={handleTest} disabled={testing}>
                    {testing ? <Spinner className="h-3.5 w-3.5 mr-1.5" /> : <FlaskConical className="h-3.5 w-3.5 mr-1.5" />}
                    Test now
                  </Button>
                  {testResult && (
                    <span className={cn("text-xs inline-flex items-center gap-1.5", testResult.valid ? "text-success" : "text-destructive")}>
                      {testResult.valid ? <CheckCircle2 className="h-3.5 w-3.5" /> : <XCircle className="h-3.5 w-3.5" />}
                      {testResult.valid ? "Valid" : (testResult.error || "Invalid")}
                    </span>
                  )}
                </div>
                )}
              </TabsContent>

              {/*
                Fields. A secret part is listed by KEY and marked "secret" —
                there is no masked placeholder standing in for a value, because
                the server does not return one: credential_fields.go omits
                `encrypted_value` from the SELECT entirely, so there are no
                bytes here to render even by accident. Non-secret parts (region,
                account id) ARE shown, which is the entire reason they are
                stored in the clear.
              */}
              <TabsContent value="fields" className="m-0">
                {fieldsLoading ? (
                  <div className="text-center py-8"><Spinner className="inline h-4 w-4 text-muted-foreground" /></div>
                ) : fields.length === 0 ? (
                  <p className="text-xs text-muted-foreground py-6 text-center">
                    This credential is a single value — no extra fields.
                  </p>
                ) : (
                  <ul className="space-y-1.5">
                    {fields.map((f) => (
                      <li
                        key={f.key}
                        className="rounded-md border border-white/10 bg-background px-3 py-2 text-xs flex items-center gap-2"
                      >
                        <span className="font-mono text-foreground/90">{f.key}</span>
                        {f.is_secret ? (
                          <Badge variant="outline" className="ml-auto text-[9px] px-1 border-warn/40 text-warn">
                            secret
                          </Badge>
                        ) : (
                          <span className="ml-auto font-mono text-[11px] text-muted-foreground truncate max-w-[220px]">
                            {f.value}
                          </span>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
              </TabsContent>

              <TabsContent value="used-by" className="m-0 space-y-4">
                {/* Bindings — (scope, slot) → this credential. This is the
                    answer to "which env var will the container actually see",
                    which before P3 had no answer short of booting the agent. */}
                {bindings.length > 0 && (
                  <div className="space-y-1.5">
                    <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                      Slots
                    </div>
                    <ul className="space-y-1">
                      {bindings.map((b) => (
                        <li
                          key={b.id}
                          className="rounded-md border border-white/10 bg-background px-3 py-2 text-xs flex items-center gap-2"
                        >
                          <Badge variant="outline" className="text-[9px] px-1">{b.scope}</Badge>
                          <span className="font-mono">{b.slot}</span>
                          {(b.crew_id || b.agent_id) && (
                            <span className="ml-auto font-mono text-[10px] text-muted-foreground">
                              {b.crew_id ?? b.agent_id}
                            </span>
                          )}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                {assignments.length > 0 ? (
                  <div className="space-y-1.5">
                    <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                      Agents
                    </div>
                    <ul className="space-y-1.5">
                      {assignments.map((a) => (
                        <li
                          key={`${a.agentName}:${a.envVarName}:${a.grantSource}`}
                          className="rounded-md border border-white/10 bg-background px-3 py-2 text-sm flex items-center gap-2"
                        >
                          <Users className="h-3.5 w-3.5 text-muted-foreground" />
                          <span className="truncate">{a.agentName}</span>
                          {/* grant_source decides where the revoke lives: an
                              explicit grant has an assignment id and its own
                              DELETE; a crew-derived one has no row at all and
                              can only be taken away by unlinking the crew. */}
                          <Badge
                            variant="outline"
                            className={cn(
                              "ml-auto text-[9px] px-1",
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
                              className={cn("text-[9px] px-1", a.expired ? "border-destructive/40 text-destructive" : "border-warn/40 text-warn")}
                            >
                              {a.expired ? "lease expired" : "leased"}
                            </Badge>
                          )}
                        </li>
                      ))}
                    </ul>
                  </div>
                ) : credential.agent_names.length > 0 ? (
                  <ul className="space-y-1.5">
                    {credential.agent_names.map((name) => (
                      <li key={name} className="rounded-md border border-white/10 bg-background px-3 py-2 text-sm flex items-center gap-2">
                        <Users className="h-3.5 w-3.5 text-muted-foreground" />
                        {name}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="text-xs text-muted-foreground py-6 text-center">
                    Not yet used by any agent.
                  </p>
                )}
                {credential.mcp_used && (
                  <div className="mt-3 rounded-md border border-info/25 bg-info/[0.05] px-3 py-2 text-xs">
                    Also referenced by one or more MCP server integrations.
                  </div>
                )}
              </TabsContent>

              <TabsContent value="audit" className="m-0">
                {auditLoading ? (
                  <div className="text-center py-8"><Spinner className="inline h-4 w-4 text-muted-foreground" /></div>
                ) : audit.length === 0 ? (
                  <p className="text-xs text-muted-foreground py-6 text-center">No audit events yet.</p>
                ) : (
                  <ul className="space-y-2">
                    {audit.map((e, idx) => (
                      <motion.li
                        key={e.id}
                        initial={{ opacity: 0, y: 4 }}
                        animate={{ opacity: 1, y: 0 }}
                        transition={{ duration: 0.12, delay: idx * 0.015 }}
                        className="rounded-md border border-white/10 bg-background px-3 py-2 text-xs"
                      >
                        <div className="flex items-center justify-between gap-2">
                          <Badge variant="outline" className="text-[10px] px-1.5 font-mono">{e.event_type}</Badge>
                          <span className="text-muted-foreground">{formatRelativeTime(e.occurred_at)}</span>
                        </div>
                        {e.ip_address && (
                          <div className="text-[10px] text-muted-foreground font-mono mt-1">
                            from {e.ip_address}
                          </div>
                        )}
                      </motion.li>
                    ))}
                  </ul>
                )}
              </TabsContent>

              <TabsContent value="settings" className="m-0 space-y-4">
                {/* Only claim "no permission" when there is genuinely nothing
                    here — a MEMBER holding credential.rotate can act on this
                    credential, just not rewrite its value. */}
                {!canUpdate && !canRotate && (
                  <p className="text-xs text-muted-foreground">
                    You don&apos;t have permission to modify this credential.
                  </p>
                )}
                {!canUpdate && canRotate && (
                  <p className="text-xs text-muted-foreground">
                    You can rotate this credential. Replacing its value outright requires
                    a workspace manager.
                  </p>
                )}
                {canUpdate && !canRotate && (
                  <p className="text-xs text-muted-foreground">
                    Rotation with grace overlap and deletion require a workspace admin.
                  </p>
                )}

                {/* Classification. Raising is MANAGER+ and unaudited (it only
                    ever removes reach); lowering is OWNER/ADMIN and journaled
                    as a precondition, because it hands out a key that did not
                    exist a second earlier. */}
                {canUpdate && (
                  <div className="space-y-1.5">
                    <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                      Classification
                    </div>
                    <div className="flex flex-wrap gap-1.5">
                      {["STANDARD", "RESTRICTED", "SEALED"].map((level) => {
                        const current = effectiveSensitivity
                        const rank = ["STANDARD", "RESTRICTED", "SEALED"]
                        const lowering = current !== null && rank.indexOf(level) < rank.indexOf(current)
                        const blocked = lowering && !canLowerSensitivity
                        return (
                          <button
                            key={level}
                            type="button"
                            disabled={sensitivitySaving || blocked || level === current}
                            aria-pressed={level === current}
                            onClick={() => setClassification(level)}
                            title={blocked ? "Lowering a classification is a workspace-admin action" : undefined}
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
                    <p className="text-[10px] text-muted-foreground">
                      {effectiveSensitivity === null
                        ? "The current classification is not reported by the credentials API — picking one below sets it."
                        : effectiveSensitivity === "SEALED"
                          ? "SEALED can never be revealed, by any role. Break-glass is rotation, not disclosure."
                          : "Raise it at any time; lowering it is an audited, admin-only action."}
                    </p>
                    {sensitivityError && (
                      <span className="text-[11px] text-destructive inline-flex items-center gap-1">
                        <XCircle className="h-3 w-3" />
                        {sensitivityError}
                      </span>
                    )}
                  </div>
                )}

                {/* Inline value rewrite — quick manual rotation without
                    grace overlap. For users who just need to paste a
                    new key and move on (Vercel pattern). The real
                    rotation flow with overlap lives in onRotate. */}
                {canUpdate && (
                <div className="space-y-1.5">
                  <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                    Update value
                  </div>
                  <div className="relative">
                    <Input
                      type={showValueDraft ? "text" : "password"}
                      placeholder="Paste new secret value"
                      value={valueDraft}
                      onChange={(e) => {
                        setValueDraft(e.target.value)
                        setValueSaved(false)
                        // Clear stale error as soon as the user retries —
                        // a red message stuck under a freshly-typed input
                        // reads like "your current input is rejected".
                        setValueError(null)
                      }}
                      className="pr-10 font-mono text-xs"
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      className="absolute right-1.5 top-1/2 -translate-y-1/2"
                      onClick={() => setShowValueDraft((s) => !s)}
                      aria-label={showValueDraft ? "Hide value" : "Show value"}
                    >
                      {showValueDraft ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                    </Button>
                  </div>
                  <div className="flex items-center gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={!valueDraft.trim() || savingValue}
                      onClick={async () => {
                        setSavingValue(true)
                        setValueSaved(false)
                        setValueError(null)
                        try {
                          const res = await apiFetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
                            method: "PATCH",
                            headers: { "Content-Type": "application/json" },
                            body: JSON.stringify({ value: valueDraft }),
                          })
                          if (res.ok) {
                            // Success — clear draft so plaintext doesn't
                            // linger in DOM/state.
                            setValueDraft("")
                            setShowValueDraft(false)
                            setValueSaved(true)
                            onRefresh()
                          } else {
                            const data = await res.json().catch(() => ({}))
                            setValueError(typeof data.error === "string" ? data.error : `Request failed (${res.status})`)
                          }
                        } catch {
                          setValueError("Network error")
                        } finally {
                          setSavingValue(false)
                          // Defence-in-depth: even on failure, keep
                          // plaintext only as long as the input shows
                          // it. Drafts are gone once the user dismisses
                          // the error or closes the sheet (handled by
                          // the open-effect reset).
                        }
                      }}
                    >
                      {savingValue && <Spinner className="h-3 w-3 mr-1.5" />}
                      Save value
                    </Button>
                    {valueSaved && (
                      <span className="text-[11px] text-success inline-flex items-center gap-1">
                        <CheckCircle2 className="h-3 w-3" />
                        Saved
                      </span>
                    )}
                    {valueError && (
                      <span className="text-[11px] text-destructive inline-flex items-center gap-1">
                        <XCircle className="h-3 w-3" />
                        {valueError}
                      </span>
                    )}
                  </div>
                  <p className="text-[10px] text-muted-foreground">
                    Save replaces the value immediately. Use rotate-with-grace if agents are
                    currently running and need a 24h overlap.
                  </p>
                </div>
                )}

                {/* Rotation is gated on canRotate ALONE, deliberately outside
                    the canUpdate block above. The two permissions are not
                    nested: PATCH is MANAGER+, while rotate additionally accepts
                    any member holding credential.rotate
                    (requireRoleOrCapabilityOrForbid, #1028) — the grant that
                    lets an oncall MEMBER replace a leaked token without blanket
                    vault reach. Nesting this inside canUpdate hid the action
                    from precisely that tier. */}
                {canRotate && (
                  <div className="space-y-1.5">
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => onRotate(credential)}
                      className="text-[11px] text-muted-foreground hover:text-foreground -ml-2"
                    >
                      <RefreshCw className="h-3 w-3 mr-1.5" />
                      Rotate with grace overlap…
                    </Button>
                    <p className="text-[10px] text-muted-foreground">
                      Issues a new value and keeps the old one working for the grace window,
                      so agents mid-run don&apos;t break.
                    </p>
                  </div>
                )}

                {canRotate && rotations.length > 0 && (
                  <div className="space-y-1.5">
                    <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                      Rotation history
                    </div>
                    <ul className="space-y-1">
                      {rotations.slice(0, 5).map((r) => (
                        <li key={r.id} className="text-xs flex items-center gap-2 px-2 py-1 rounded border border-white/10 bg-background">
                          <Badge
                            variant="outline"
                            className={cn(
                              "text-[10px] px-1.5",
                              r.status === "ACTIVE" && "border-primary/40 text-primary",
                              r.status === "EXPIRED" && "border-success/30 text-success",
                              r.status === "CANCELLED" && "border-warn/30 text-warn",
                            )}
                          >
                            {r.status}
                          </Badge>
                          <span className="text-muted-foreground">{formatRelativeTime(r.rotated_at)}</span>
                          <span className="ml-auto text-[10px] text-muted-foreground font-mono">
                            {Math.round(r.grace_seconds / 3600)}h grace
                          </span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                {canDelete && (
                <div className="pt-3 border-t border-white/10">
                  <Button
                    size="sm"
                    variant="outline"
                    className="w-full justify-start text-destructive border-destructive/30 hover:bg-destructive/[0.05]"
                    onClick={() => setConfirmDelete(true)}
                  >
                    <Trash2 className="h-3.5 w-3.5 mr-1.5" />
                    Delete credential
                  </Button>
                </div>
                )}
              </TabsContent>
            </div>
          </Tabs>
        </SheetContent>
      </Sheet>

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
  const candidates = (agents as { id?: string; name?: string; crew_id?: string | null }[])
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

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[100px_1fr] gap-2 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-foreground/90 font-mono">{children}</span>
    </div>
  )
}

// Inline alias so we don't have to import AlertDialogCancel everywhere — saves a line.
function Cancel({ children }: { children: React.ReactNode }) {
  return <AlertDialogCancel>{children}</AlertDialogCancel>
}
