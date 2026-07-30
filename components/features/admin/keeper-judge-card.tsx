"use client"

// The instance-level Keeper judge — the card that answers "what decides whether
// an agent gets a credential, and can I change it?"
//
// Until the runtime-config slice (crewship#1530) the answer to the second half
// was no. `keeper.enabled`, the endpoint and the model were boot-time env, so
// this page could only DIAGNOSE a dead judge: it rendered "Not running —
// disabled by configuration (keeper.enabled = false)" next to no control, and
// told the operator to edit .env.local and restart the server. For a
// self-hosted product whose pitch is "runs fully local", the local case was the
// one that could not be configured from the product.
//
// Backed by internal/api/admin_keeper_config.go:
//
//   GET    /api/v1/admin/keeper/config   → per-field { value, source, editable }
//   PUT    /api/v1/admin/keeper/config   ← partial update (ADMIN+/OWNER)
//   DELETE /api/v1/admin/keeper/config   → drop every instance override
//
// Every field either carries an instance override or inherits the KEEPER_*
// value the server booted with, and the card says which — that distinction is
// the whole reason provenance is on the wire. "Disabled" and "somebody turned
// this off here" look identical without it.

import { useCallback, useEffect, useState } from "react"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { SaveFooter } from "@/components/ui/save-footer"
import { useDirtyForm } from "@/hooks/use-dirty-form"
import { useAbilities } from "@/hooks/use-abilities"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { cn } from "@/lib/utils"

/** Where an effective value came from. Mirrors keepercfg.Source. */
type ConfigSource = "instance" | "env" | "default"

interface ConfigField<T> {
  value: T
  source: ConfigSource
  editable: boolean
}

interface KeeperConfigResponse {
  enabled: ConfigField<boolean>
  judge_provider: ConfigField<string>
  judge_endpoint_url: ConfigField<string>
  judge_wire: ConfigField<string>
  judge_model: ConfigField<string>
  overridden: boolean
  updated_at?: string
  updated_by?: string
  /** False when the effective endpoint or model is missing — the state in which
   *  enabling Keeper would deny every credential request. */
  judge_configured: boolean
}

/**
 * ProvenanceChip says whether a value is this instance's own or inherited, so
 * "Reset to inherited" has a visible referent. Deliberately quiet: it is a
 * caption, not a status.
 */
function ProvenanceChip({ source }: { source: ConfigSource }) {
  const label =
    source === "instance" ? "instance override"
      : source === "env" ? "from server config"
        : "built-in default"
  return (
    <span
      className={cn(
        "inline-flex items-center h-[15px] px-1.5 rounded text-[9px] font-medium uppercase tracking-wide border",
        source === "instance"
          ? "text-primary/90 border-primary/30 bg-primary/[0.08]"
          : "text-muted-foreground border-border/60 bg-muted/30",
      )}
    >
      {label}
    </span>
  )
}

/** Row description with the provenance chip on its own line underneath. */
function WithProvenance({ source, children }: { source: ConfigSource; children: React.ReactNode }) {
  return (
    <span className="block">
      <span className="block">{children}</span>
      <span className="mt-1 inline-flex"><ProvenanceChip source={source} /></span>
    </span>
  )
}

async function errorFrom(res: Response, fallback: string): Promise<string> {
  try {
    const body = (await res.json()) as { error?: string; detail?: string }
    return body.error ?? body.detail ?? fallback
  } catch {
    return fallback
  }
}

export function KeeperJudgeCard({ workspaceId }: { workspaceId: string | null | undefined }) {
  // The PUT is roleManage (OWNER/ADMIN) server-side, which is exactly who gets
  // "manage" on Workspace from CASL. The server stays authoritative; a
  // read-only render is a UX hint, not the gate.
  const { abilities } = useAbilities()
  const canEdit = abilities.can("manage", "Workspace")

  const [cfg, setCfg] = useState<KeeperConfigResponse | null>(null)
  const [err, setErr] = useState<string | null>(null)
  const [resetting, setResetting] = useState(false)

  // Engine, endpoint and model share ONE draft on purpose, even though a switch
  // would normally commit on the spot. Keeper is fail-closed: enabling it
  // without a judge is refused by the server, so a lone switch on a fresh
  // instance could only fail. One Save sends all three, which is the flow the
  // endpoint was built for — turn it on and say what decides, in one write.
  const form = useDirtyForm({
    enabled: cfg?.enabled.value ?? false,
    endpoint: cfg?.judge_endpoint_url.value ?? "",
    model: cfg?.judge_model.value ?? "",
  })

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!workspaceId) return
    try {
      const res = await apiFetch(
        `/api/v1/admin/keeper/config?workspace_id=${encodeURIComponent(workspaceId)}`,
        { signal },
      )
      if (signal?.aborted) return
      if (!res.ok) {
        // 503 is the honest "this server has no settings store" case; anything
        // else is a read failure. Either way the card must not render an empty
        // form whose saves would vanish.
        setErr(await errorFrom(res, `Couldn't load the judge configuration (HTTP ${res.status}).`))
        return
      }
      setCfg((await res.json()) as KeeperConfigResponse)
      setErr(null)
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") return
      setErr("Couldn't load the judge configuration.")
    }
  }, [workspaceId])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  function handleSave() {
    if (!workspaceId) return
    void form.submit(async (draft) => {
      const res = await apiFetch(
        `/api/v1/admin/keeper/config?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          // Endpoint and model are sent trimmed, and an emptied field is a
          // deliberate "stop overriding this" — the server reads "" as a clear,
          // which is why they are always present rather than omitted when blank.
          body: JSON.stringify({
            enabled: draft.enabled,
            judge_endpoint_url: draft.endpoint.trim(),
            judge_model: draft.model.trim(),
          }),
        },
      )
      if (!res.ok) {
        // Thrown so the footer shows it and keeps the draft: the server's
        // message is the useful one here ("Keeper cannot be enabled without a
        // judge endpoint and model — it is fail-closed, …").
        throw new Error(await errorFrom(res, `Failed to save (HTTP ${res.status})`))
      }
      setCfg((await res.json()) as KeeperConfigResponse)
    })
  }

  async function handleReset() {
    if (!workspaceId || resetting) return
    setResetting(true)
    try {
      const res = await apiFetch(
        `/api/v1/admin/keeper/config?workspace_id=${encodeURIComponent(workspaceId)}`,
        { method: "DELETE" },
      )
      if (!res.ok) {
        toast.error(await errorFrom(res, `Failed to reset (HTTP ${res.status})`))
        return
      }
      const next = (await res.json()) as KeeperConfigResponse
      setCfg(next)
      // The draft is rebased by the baseline effect once cfg lands, but only
      // while the form is clean — reset is a discard, so drop the draft too.
      form.reset()
      toast.success("Judge configuration reset — the server's own settings are back in force.")
    } finally {
      setResetting(false)
    }
  }

  if (!workspaceId) return null

  if (err) {
    return (
      <SettingsCard title="Credential access judge" description="Which model decides credential access, instance-wide.">
        <div className="px-4 py-3 flex items-center justify-between gap-3">
          <span className="text-[11px] text-destructive/90">{err}</span>
          <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={() => { setErr(null); void load() }}>
            Retry
          </Button>
        </div>
      </SettingsCard>
    )
  }

  if (!cfg) {
    return <Skeleton className="h-[190px] rounded-xl" data-testid="keeper-judge-loading" />
  }

  const failClosed = form.draft.enabled && (form.draft.endpoint.trim() === "" || form.draft.model.trim() === "")

  return (
    <SettingsCard
      title="Credential access judge"
      description="Which model decides credential access, and whether Keeper runs at all. Instance-wide; a workspace governance model overrides it per request. Changes apply to the next credential request — no restart."
      actions={
        canEdit && cfg.overridden ? (
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => { void handleReset() }}
            disabled={resetting}
            data-testid="keeper-judge-reset"
          >
            {resetting ? "Resetting…" : "Reset to inherited"}
          </Button>
        ) : undefined
      }
    >
      <SettingsRow
        label="Keeper engine"
        description={
          <WithProvenance source={cfg.enabled.source}>
            With Keeper on, SECRET credentials are withheld from agents and must be requested.
            Applies to runs started after the change.
          </WithProvenance>
        }
      >
        <Switch
          checked={form.draft.enabled}
          onCheckedChange={(checked) => form.set("enabled", checked)}
          disabled={!canEdit}
          aria-label="Toggle the Keeper engine"
          data-testid="keeper-judge-enabled"
        />
      </SettingsRow>

      <SettingsRow
        label="Judge endpoint"
        description={
          <WithProvenance source={cfg.judge_endpoint_url.source}>
            Where the judge asks. Repoints the judge only — the episodic embedder and the chat
            summarizer keep the server&apos;s own URL. Clear the field to inherit it again.
          </WithProvenance>
        }
      >
        <Input
          type="text"
          value={form.draft.endpoint}
          onChange={(e) => form.set("endpoint", e.target.value)}
          disabled={!canEdit}
          placeholder="http://localhost:11434"
          className="h-8 w-[240px] text-xs font-mono"
          aria-label="Judge endpoint URL"
          data-testid="keeper-judge-endpoint"
        />
      </SettingsRow>

      <SettingsRow
        label="Judge model"
        description={
          <WithProvenance source={cfg.judge_model.source}>
            The model that returns the verdict. Clear the field to inherit the server&apos;s.
          </WithProvenance>
        }
      >
        <Input
          type="text"
          value={form.draft.model}
          onChange={(e) => form.set("model", e.target.value)}
          disabled={!canEdit}
          placeholder="qwen2.5:7b"
          className="h-8 w-[240px] text-xs font-mono"
          aria-label="Judge model"
          data-testid="keeper-judge-model"
        />
      </SettingsRow>

      <SettingsRow
        label="Wire format"
        description="The instance judge speaks the native Ollama API. An OpenAI-compatible or Anthropic judge is configured per workspace as the governance model below, which carries its endpoint and key in the vault."
        border={false}
      >
        <span className="text-[11px] text-muted-foreground font-mono" data-testid="keeper-judge-wire">
          {cfg.judge_provider.value || "—"}
          {cfg.judge_wire.value ? ` / ${cfg.judge_wire.value}` : ""}
        </span>
      </SettingsRow>

      {failClosed && (
        <div role="status" className="px-4 py-2 text-[11px] text-destructive border-t border-destructive/20 bg-destructive/[0.05]">
          Keeper is fail-closed: with the engine on and no endpoint or model, every credential
          request is denied. Fill both in before saving.
        </div>
      )}

      {canEdit && (
        <SaveFooter
          dirty={form.isDirty}
          status={form.status}
          error={form.error}
          canSave={!failClosed}
          onSave={handleSave}
          onCancel={form.reset}
        />
      )}
    </SettingsCard>
  )
}
