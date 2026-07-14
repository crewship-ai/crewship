"use client"

// Issue #1001 M0 — Keeper watchdog governance control panel.
//
// Rendered at the top of the admin Keeper tab. Surfaces the per-workspace
// watchdog governance settings backed by internal/api/keeper_governance.go:
//
//   GET /api/v1/admin/keeper/governance  → { configured, enabled,
//        security_contact_user_id, deny_notify_min_risk }        (ADMIN+)
//   PUT /api/v1/admin/keeper/governance  ← { enabled,
//        security_contact_user_id, deny_notify_min_risk }        (OWNER/ADMIN)
//
// "No row" semantics: the behavioral watchdog is opt-in and default OFF per
// workspace — configured=false means it has never been enabled here, so the
// switch shows off. The server engine flag (serverEnabled) is shown only as
// context; it governs the credential-access gatekeeper, not this switch.
//
// The security contact must be an OWNER/ADMIN workspace member (the
// backend rejects anything else with a 400), so the picker is filtered
// to those roles from GET /workspaces/{id}/members. Empty contact =
// legacy fanout to everyone with the MANAGER role.

import React, { useCallback, useEffect, useMemo, useState } from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Checkbox } from "@/components/ui/checkbox"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { useAbilities } from "@/hooks/use-abilities"
import { useCredentials } from "@/components/features/mcp/hooks/use-credentials"
import { apiFetch } from "@/lib/api-fetch"

interface GovernanceResponse {
  configured: boolean
  enabled: boolean
  security_contact_user_id: string
  deny_notify_min_risk: number
  watch_spec?: string
  watch_presets?: string[]
  // Governance-model override (backed by keeper_governance.go). All optional:
  // an empty provider means "use the server default model". When provider is
  // non-empty the server requires gov_model_id; credential is always optional.
  gov_model_provider?: string
  gov_model_id?: string
  gov_model_credential_id?: string
}

// GOV_MODEL_PROVIDERS mirrors the provider values accepted by
// keeper_governance.go / the CLI: "" (server default), "ollama", "anthropic",
// "openai_compat". Radix Select forbids value="" on items, so the "server
// default" option uses a sentinel that maps back to the empty wire value.
const GOV_PROVIDER_DEFAULT = "__server_default__"
const GOV_CREDENTIAL_NONE = "__none__"

const GOV_MODEL_PROVIDERS: { value: string; label: string; modelHint?: string }[] = [
  { value: "", label: "Server default" },
  { value: "ollama", label: "Ollama (local)", modelHint: "qwen2.5:3b-instruct" },
  { value: "anthropic", label: "Anthropic", modelHint: "claude-haiku-4-5" },
  { value: "openai_compat", label: "OpenAI-compatible", modelHint: "gpt-4o-mini" },
]

// Credential types usable as a governance-model credential: an API key
// (anthropic / openai_compat) or an endpoint URL (a remote ollama / compat host).
const GOV_CREDENTIAL_TYPES = new Set(["API_KEY", "ENDPOINT_URL"])

// WATCH_PRESETS mirrors internal/keeper/governance/presets.go — the five stable
// preset keys. The Go source is the authority for the wording actually injected
// into the evaluator prompts; these captions are UI summaries. Keep the key set
// in sync by hand (five stable keys; changing them is a product decision).
// Mirrors governance.MaxWatchSpecLen (the server + CLI cap on the free-form spec).
const WATCH_SPEC_MAX_LEN = 4096

const WATCH_PRESETS: { key: string; label: string; caption: string }[] = [
  { key: "credentials", label: "Credential access", caption: "Disproportionate or bulk secret access, unjustified high-security reads." },
  { key: "egress", label: "Network egress", caption: "Exfiltration-shaped outbound: non-allowlisted hosts, piping secrets out." },
  { key: "memory", label: "Memory tampering", caption: "Overwriting facts, mass deletes, or planting misleading memory entries." },
  { key: "destructive", label: "Destructive ops", caption: "rm -rf, DROP/TRUNCATE without WHERE, force-push, wholesale overwrites." },
  { key: "secret_files", label: "Sensitive files", caption: "Reads of ~/.ssh, id_rsa, .env, cloud credential files, private keys." },
]

interface WorkspaceMember {
  id: string
  user_id: string
  role: string
  user?: {
    id: string
    email: string
    full_name: string | null
    avatar_url: string | null
  } | null
}

interface FormState {
  enabled: boolean
  contact: string // "" = everyone with MANAGER role
  risk: string    // kept as string so the number input can be edited freely
  watchSpec: string       // free-form NL rules
  watchPresets: string[]  // enabled preset keys
  govProvider: string     // "" | ollama | anthropic | openai_compat
  govModelId: string      // required when govProvider != ""
  govCredentialId: string // optional; "" = none
}

// sameSet compares two preset-key arrays order-independently (the wire order is
// not meaningful) so dirty-tracking doesn't flag a reordering as a change.
function sameSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false
  const s = new Set(a)
  return b.every((k) => s.has(k))
}

// Radix Select forbids value="" on items, so the "everyone" option uses a
// sentinel that maps to the backend's empty security_contact_user_id.
const MANAGER_FANOUT = "__managers__"

export interface KeeperGovernancePanelProps {
  workspaceId: string | null | undefined
  /** Server-level keeper engine flag (GET /system/keeper) — shown as context
   *  only; the per-workspace watchdog toggle is independent (opt-in). */
  serverEnabled: boolean
}

export const KeeperGovernancePanel = React.memo(function KeeperGovernancePanel({
  workspaceId,
  serverEnabled,
}: KeeperGovernancePanelProps) {
  // Mirrors AgentLearningToggle: derive edit rights from CASL. The PUT is
  // roleManage (OWNER/ADMIN) server-side; only those roles get "manage" on
  // Workspace, so this lines up exactly. Server stays authoritative — the
  // greyed-out UI is a UX hint, not a security boundary.
  const { abilities } = useAbilities()
  const canEdit = useMemo(() => abilities.can("manage", "Workspace"), [abilities])

  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [configured, setConfigured] = useState(false)
  const [admins, setAdmins] = useState<WorkspaceMember[]>([])
  const emptyForm: FormState = {
    enabled: false, contact: "", risk: "7", watchSpec: "", watchPresets: [],
    govProvider: "", govModelId: "", govCredentialId: "",
  }
  const [form, setForm] = useState<FormState>(emptyForm)
  const [baseline, setBaseline] = useState<FormState>(emptyForm)

  // Governance-model credential picker. Reuses the MCP credentials hook; we
  // only surface API_KEY / ENDPOINT_URL creds (the two usable as a model cred).
  const { credentials } = useCredentials(workspaceId ?? undefined)
  const govCredentials = useMemo(
    () => credentials.filter((c) => GOV_CREDENTIAL_TYPES.has(c.type)),
    [credentials],
  )

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!workspaceId) {
      setLoading(false)
      return
    }
    setLoading(true)
    setErr(null)
    try {
      const [govRes, membersRes] = await Promise.all([
        apiFetch(
          `/api/v1/admin/keeper/governance?workspace_id=${encodeURIComponent(workspaceId)}`,
          { signal },
        ),
        apiFetch(
          `/api/v1/workspaces/${workspaceId}/members?workspace_id=${encodeURIComponent(workspaceId)}`,
          { signal },
        ),
      ])
      if (signal?.aborted) return
      if (!govRes.ok) {
        setErr(`Failed to load governance settings (HTTP ${govRes.status})`)
        return
      }
      const gov = (await govRes.json()) as GovernanceResponse
      if (signal?.aborted) return

      // A members failure only degrades the picker; governance still renders.
      if (membersRes.ok) {
        const members = (await membersRes.json()) as WorkspaceMember[]
        if (signal?.aborted) return
        setAdmins(
          (Array.isArray(members) ? members : []).filter(
            (m) => m.role === "OWNER" || m.role === "ADMIN",
          ),
        )
      } else {
        setAdmins([])
      }

      setConfigured(gov.configured)
      const next: FormState = {
        // Opt-in, default OFF: an unconfigured workspace shows the switch off
        // (gov.enabled is false server-side until explicitly enabled).
        enabled: gov.enabled,
        contact: gov.security_contact_user_id ?? "",
        risk: String(gov.deny_notify_min_risk ?? 7),
        watchSpec: gov.watch_spec ?? "",
        watchPresets: gov.watch_presets ?? [],
        govProvider: gov.gov_model_provider ?? "",
        govModelId: gov.gov_model_id ?? "",
        govCredentialId: gov.gov_model_credential_id ?? "",
      }
      setForm(next)
      setBaseline(next)
    } catch (e) {
      // Aborts are expected when workspaceId changes mid-flight.
      if (e instanceof DOMException && e.name === "AbortError") return
      setErr(e instanceof Error ? e.message : "Failed to load governance settings")
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [workspaceId, serverEnabled])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const dirty =
    form.enabled !== baseline.enabled ||
    form.contact !== baseline.contact ||
    form.risk !== baseline.risk ||
    form.watchSpec !== baseline.watchSpec ||
    !sameSet(form.watchPresets, baseline.watchPresets) ||
    form.govProvider !== baseline.govProvider ||
    form.govModelId !== baseline.govModelId ||
    form.govCredentialId !== baseline.govCredentialId

  // A non-empty provider REQUIRES a model id (the server 400s otherwise); block
  // save and surface the requirement client-side.
  const govModelMissing = form.govProvider !== "" && form.govModelId.trim() === ""

  const save = useCallback(async () => {
    if (!workspaceId) return
    const riskNum = Number(form.risk)
    if (!Number.isInteger(riskNum) || riskNum < 1 || riskNum > 10) {
      toast.error("Risk threshold must be a whole number between 1 and 10")
      return
    }
    if (form.govProvider !== "" && form.govModelId.trim() === "") {
      toast.error("A model id is required when a governance-model provider is set")
      return
    }
    setSaving(true)
    try {
      const res = await apiFetch(
        `/api/v1/admin/keeper/governance?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            enabled: form.enabled,
            security_contact_user_id: form.contact,
            deny_notify_min_risk: riskNum,
            watch_spec: form.watchSpec,
            watch_presets: form.watchPresets,
            gov_model_provider: form.govProvider,
            // Trim to "" when the provider is server-default so we never send a
            // stale model id alongside an empty provider.
            gov_model_id: form.govProvider === "" ? "" : form.govModelId.trim(),
            // Same guard for the credential: the server rejects a credential
            // with no provider (400), so drop a stale credential when the
            // provider is reset to server-default.
            gov_model_credential_id:
              form.govProvider === "" ? "" : form.govCredentialId,
          }),
        },
      )
      if (!res.ok) {
        let msg = `HTTP ${res.status}`
        try {
          const e = (await res.json()) as { error?: string; detail?: string }
          msg = e.error ?? e.detail ?? msg
        } catch {
          /* keep the status fallback */
        }
        toast.error(`Failed to save governance: ${msg}`)
        return
      }
      const body = (await res.json()) as GovernanceResponse
      setConfigured(body.configured)
      const next: FormState = {
        enabled: body.enabled,
        contact: body.security_contact_user_id ?? "",
        risk: String(body.deny_notify_min_risk ?? riskNum),
        watchSpec: body.watch_spec ?? "",
        watchPresets: body.watch_presets ?? [],
        govProvider: body.gov_model_provider ?? "",
        govModelId: body.gov_model_id ?? "",
        govCredentialId: body.gov_model_credential_id ?? "",
      }
      setForm(next)
      setBaseline(next)
      toast.success(body.enabled ? "Watchdog enabled" : "Watchdog governance saved")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save governance")
    } finally {
      setSaving(false)
    }
  }, [workspaceId, form])

  if (!workspaceId) return null

  if (loading) {
    return <Skeleton className="h-[180px] rounded-xl" data-testid="keeper-governance-loading" />
  }

  if (err) {
    return (
      <SettingsCard
        title="Watchdog governance"
        description="Workspace-level watchdog controls"
      >
        <div className="px-4 py-3 flex items-center justify-between gap-3">
          <span className="text-[11px] text-destructive/90">{err}</span>
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => { void load() }}
          >
            Retry
          </Button>
        </div>
      </SettingsCard>
    )
  }

  // Keep the current contact selectable even if that member was demoted or
  // removed since it was saved — otherwise the Select renders blank and a
  // save would silently rewrite the contact.
  const contactInList =
    form.contact === "" || admins.some((m) => m.user_id === form.contact)

  return (
    <SettingsCard
      title="Watchdog governance"
      description="Who the behavioral watchdog reports to, and when. Credential-access enforcement stays server-configured."
      actions={
        canEdit ? (
          <Button
            variant="soft"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => { void save() }}
            disabled={saving || !dirty || govModelMissing}
            data-testid="keeper-governance-save"
          >
            {saving ? "Saving…" : "Save"}
          </Button>
        ) : undefined
      }
    >
      <SettingsRow
        label="Watchdog enabled"
        description={
          configured
            ? `Behavioral monitoring for this workspace. Server engine is ${serverEnabled ? "on" : "off"}.`
            : `Off by default (opt-in) — enable to start behavioral monitoring for this workspace. Server engine is ${serverEnabled ? "on" : "off"}.`
        }
      >
        <Switch
          checked={form.enabled}
          onCheckedChange={(checked) => setForm((f) => ({ ...f, enabled: checked }))}
          disabled={!canEdit || saving}
          data-testid="keeper-governance-switch"
          aria-label="Toggle watchdog enabled"
        />
      </SettingsRow>

      <SettingsRow
        label="Security contact"
        description="Findings target this person's inbox in realtime."
      >
        <Select
          value={form.contact === "" ? MANAGER_FANOUT : form.contact}
          onValueChange={(v) =>
            setForm((f) => ({ ...f, contact: v === MANAGER_FANOUT ? "" : v }))
          }
          disabled={!canEdit || saving}
        >
          <SelectTrigger
            className="h-8 text-xs w-[220px]"
            aria-label="Security contact"
            data-testid="keeper-governance-contact"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={MANAGER_FANOUT} className="text-xs">
              Everyone with MANAGER role
            </SelectItem>
            {admins.map((m) => (
              <SelectItem key={m.user_id} value={m.user_id} className="text-xs">
                {m.user?.full_name || m.user?.email || m.user_id}
              </SelectItem>
            ))}
            {!contactInList && (
              <SelectItem value={form.contact} className="text-xs">
                {form.contact} (no longer OWNER/ADMIN)
              </SelectItem>
            )}
          </SelectContent>
        </Select>
      </SettingsRow>

      <SettingsRow
        label="Notify on DENY at risk ≥"
        description="ESCALATE decisions always notify; this additionally surfaces high-risk DENYs."
      >
        <Input
          type="number"
          min={1}
          max={10}
          step={1}
          inputMode="numeric"
          value={form.risk}
          onChange={(e) => setForm((f) => ({ ...f, risk: e.target.value }))}
          disabled={!canEdit || saving}
          className="h-8 w-16 text-xs text-right tabular-nums"
          aria-label="DENY notification risk threshold (1-10)"
          data-testid="keeper-governance-risk"
        />
      </SettingsRow>

      {/* Governance model — which model the credential-access gatekeeper uses.
          Empty provider = the server default; a non-empty provider requires a
          model id (enforced client-side to match keeper_governance.go). */}
      <SettingsRow
        label="Governance model provider"
        description="Model backing the credential-access gatekeeper. Leave on server default unless you need a workspace override."
      >
        <Select
          value={form.govProvider === "" ? GOV_PROVIDER_DEFAULT : form.govProvider}
          onValueChange={(v) =>
            setForm((f) => ({ ...f, govProvider: v === GOV_PROVIDER_DEFAULT ? "" : v }))
          }
          disabled={!canEdit || saving}
        >
          <SelectTrigger
            className="h-8 text-xs w-[220px]"
            aria-label="Governance model provider"
            data-testid="keeper-gov-provider"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {GOV_MODEL_PROVIDERS.map((p) => (
              <SelectItem
                key={p.value || GOV_PROVIDER_DEFAULT}
                value={p.value === "" ? GOV_PROVIDER_DEFAULT : p.value}
                className="text-xs"
              >
                {p.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </SettingsRow>

      {form.govProvider !== "" && (
        <>
          <SettingsRow
            label="Governance model id"
            description="Required when a provider is set."
          >
            <span className="flex flex-col items-end gap-1">
              <Input
                type="text"
                value={form.govModelId}
                onChange={(e) => setForm((f) => ({ ...f, govModelId: e.target.value }))}
                disabled={!canEdit || saving}
                placeholder={
                  GOV_MODEL_PROVIDERS.find((p) => p.value === form.govProvider)?.modelHint
                }
                className="h-8 w-[220px] text-xs"
                aria-label="Governance model id"
                aria-required="true"
                aria-invalid={govModelMissing}
                data-testid="keeper-gov-model-id"
              />
              {govModelMissing && (
                <span className="text-[11px] text-destructive/90" data-testid="keeper-gov-model-required">
                  A model id is required for this provider.
                </span>
              )}
            </span>
          </SettingsRow>

          <SettingsRow
            label="Governance model credential"
            description="Optional. API key or endpoint URL the provider authenticates with."
          >
            <Select
              value={form.govCredentialId === "" ? GOV_CREDENTIAL_NONE : form.govCredentialId}
              onValueChange={(v) =>
                setForm((f) => ({ ...f, govCredentialId: v === GOV_CREDENTIAL_NONE ? "" : v }))
              }
              disabled={!canEdit || saving}
            >
              <SelectTrigger
                className="h-8 text-xs w-[220px]"
                aria-label="Governance model credential"
                data-testid="keeper-gov-credential"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={GOV_CREDENTIAL_NONE} className="text-xs">
                  — none —
                </SelectItem>
                {govCredentials.map((c) => (
                  <SelectItem key={c.id} value={c.id} className="text-xs">
                    {c.name} ({c.type})
                  </SelectItem>
                ))}
                {/* Keep a saved-but-now-unlisted credential selectable so the
                    Select never renders blank and silently drops it on save. */}
                {form.govCredentialId !== "" &&
                  !govCredentials.some((c) => c.id === form.govCredentialId) && (
                    <SelectItem value={form.govCredentialId} className="text-xs">
                      {form.govCredentialId} (unavailable)
                    </SelectItem>
                  )}
              </SelectContent>
            </Select>
          </SettingsRow>
        </>
      )}

      {/* Watch presets — curated rules the operator toggles on. Full-width block
          rather than a SettingsRow because the multi-select doesn't fit the
          right-aligned control slot. */}
      <div className="px-4 py-2.5 border-b border-border/40">
        <div className="text-xs text-foreground">Watch presets</div>
        <div className="text-[11px] text-muted-foreground/80 mt-0.5 leading-snug">
          Curated rules the watchdog flags against, added to its built-in checks.
        </div>
        <div className="mt-2 grid gap-2">
          {WATCH_PRESETS.map((p) => {
            const on = form.watchPresets.includes(p.key)
            return (
              <label
                key={p.key}
                className="flex items-start gap-2 cursor-pointer"
                htmlFor={`keeper-watch-preset-${p.key}`}
              >
                <Checkbox
                  id={`keeper-watch-preset-${p.key}`}
                  checked={on}
                  onCheckedChange={(checked) =>
                    setForm((f) => ({
                      ...f,
                      watchPresets:
                        checked === true
                          ? [...f.watchPresets.filter((k) => k !== p.key), p.key]
                          : f.watchPresets.filter((k) => k !== p.key),
                    }))
                  }
                  disabled={!canEdit || saving}
                  className="mt-0.5"
                  data-testid={`keeper-watch-preset-${p.key}`}
                />
                <span className="min-w-0">
                  <span className="text-xs text-foreground">{p.label}</span>
                  <span className="block text-[11px] text-muted-foreground/80 leading-snug">
                    {p.caption}
                  </span>
                </span>
              </label>
            )
          })}
        </div>
      </div>

      {/* Free-form rules — natural language, injected as authoritative policy. */}
      <div className="px-4 py-2.5">
        <div className="text-xs text-foreground">Custom watch rules</div>
        <div className="text-[11px] text-muted-foreground/80 mt-0.5 leading-snug">
          One rule per line, in plain language. Injected into the evaluator prompts.
        </div>
        <Textarea
          value={form.watchSpec}
          onChange={(e) => setForm((f) => ({ ...f, watchSpec: e.target.value }))}
          disabled={!canEdit || saving}
          rows={4}
          // Mirror the server/CLI cap (governance.MaxWatchSpecLen) client-side so
          // an over-long paste is refused before the round-trip, not lost to a 400.
          maxLength={WATCH_SPEC_MAX_LEN}
          placeholder={"flag any read of ~/.ssh or id_rsa\nflag credential access outside 08:00–18:00"}
          className="mt-2 text-xs font-mono"
          aria-label="Custom watch rules"
          data-testid="keeper-watch-spec"
        />
      </div>
    </SettingsCard>
  )
})
