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

import { useCallback, useEffect, useRef, useState } from "react"
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

/** One step of the three-stage check (internal/api/admin_keeper_judge.go). */
interface JudgeStage {
  name: string
  label: string
  ok: boolean
  skipped?: boolean
  detail: string
  latency_ms?: number
}

interface JudgeTestResult {
  ok: boolean
  endpoint: string
  model?: string
  stages: JudgeStage[]
  models?: string[]
  decision?: string
}

/**
 * StageRow renders one stage. Three states, not two: a skipped stage is not a
 * failed one, and telling them apart is what stops an operator chasing the wrong
 * problem — "no verdict" reads as a broken model until you notice the endpoint
 * never answered.
 */
function StageRow({ stage }: { stage: JudgeStage }) {
  const mark = stage.ok ? "✓" : stage.skipped ? "–" : "✗"
  const tone = stage.ok
    ? "text-success"
    : stage.skipped
      ? "text-muted-foreground/60"
      : "text-destructive"
  return (
    <div className="flex items-start gap-2 text-[11px]">
      <span className={cn("font-mono shrink-0 w-3", tone)} aria-hidden="true">{mark}</span>
      <span className="text-foreground/80 shrink-0 w-[9.5rem]">{stage.label}</span>
      <span className={cn("min-w-0 flex-1", stage.ok ? "text-muted-foreground" : tone)}>
        {stage.detail}
        {stage.latency_ms ? <span className="text-muted-foreground/60"> · {stage.latency_ms}ms</span> : null}
      </span>
    </div>
  )
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
  // Discovery + verification state. Kept out of the draft: they are questions
  // about an address, not values to save.
  const [models, setModels] = useState<string[]>([])
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<JudgeTestResult | null>(null)

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

  // Ask the endpoint what it serves, so the model is something you pick rather
  // than something you type from memory (and typo into a fail-closed DENY).
  // Debounced against the draft, because it fires while somebody is typing a URL.
  const discoverTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const draftEndpoint = form.draft.endpoint
  useEffect(() => {
    if (!workspaceId) return
    const endpoint = draftEndpoint.trim()
    if (endpoint === "") {
      setModels([])
      setModelsError(null)
      return
    }
    if (discoverTimer.current) clearTimeout(discoverTimer.current)
    const controller = new AbortController()
    discoverTimer.current = setTimeout(async () => {
      try {
        const res = await apiFetch(
          `/api/v1/admin/keeper/judge/models?workspace_id=${encodeURIComponent(workspaceId)}&endpoint=${encodeURIComponent(endpoint)}`,
          { signal: controller.signal },
        )
        if (!res.ok) {
          // A 403 here means the caller cannot manage; silence beats a scary
          // banner on a field they cannot use anyway.
          setModels([])
          setModelsError(null)
          return
        }
        const body = (await res.json()) as { models?: string[]; error?: string }
        setModels(body.models ?? [])
        setModelsError(body.error ?? null)
      } catch (e) {
        if (e instanceof DOMException && e.name === "AbortError") return
        setModels([])
        setModelsError(null)
      }
    }, 600)
    return () => {
      controller.abort()
      if (discoverTimer.current) clearTimeout(discoverTimer.current)
    }
  }, [workspaceId, draftEndpoint])

  // Test the DRAFT, not the saved row: finding a working combination before
  // committing it is the whole point of a test button.
  async function handleTest() {
    if (!workspaceId || testing) return
    setTesting(true)
    setTestResult(null)
    try {
      const res = await apiFetch(
        `/api/v1/admin/keeper/judge/test?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            judge_endpoint_url: form.draft.endpoint.trim(),
            judge_model: form.draft.model.trim(),
          }),
        },
      )
      if (!res.ok) {
        toast.error(await errorFrom(res, `The check could not run (HTTP ${res.status})`))
        return
      }
      const body = (await res.json()) as JudgeTestResult
      setTestResult(body)
      // The test round trip already listed the endpoint's models; adopt them so
      // a failed stage 2 can offer the right answer immediately.
      if (body.models?.length) {
        setModels(body.models)
        setModelsError(null)
      }
      if (body.ok) {
        toast.success("The judge works — this endpoint and model return real verdicts.")
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "The check could not run")
    } finally {
      setTesting(false)
    }
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
        canEdit ? (
          <>
            <Button
              variant="soft"
              size="sm"
              className="h-7 px-2.5 text-xs"
              onClick={() => { void handleTest() }}
              disabled={testing}
              data-testid="keeper-judge-test"
            >
              {testing ? "Testing…" : "Test"}
            </Button>
            {cfg.overridden && (
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
            )}
          </>
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
        <span className="flex flex-col items-end gap-1.5">
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
          {/* Discovered from the endpoint, one click to use. A model name is
              something to pick, not something to type from memory — a typo here
              is a fail-closed DENY on every credential request. */}
          {models.length > 0 && (
            <span className="flex flex-wrap justify-end gap-1 max-w-[19rem]" data-testid="keeper-judge-models">
              {models.map((m) => (
                <button
                  key={m}
                  type="button"
                  onClick={() => form.set("model", m)}
                  disabled={!canEdit}
                  className={cn(
                    "px-1.5 h-[18px] rounded border text-[10px] font-mono transition-colors",
                    m === form.draft.model
                      ? "border-primary/40 bg-primary/[0.08] text-primary/90"
                      : "border-border/60 bg-muted/30 text-muted-foreground hover:text-foreground",
                  )}
                >
                  {m}
                </button>
              ))}
            </span>
          )}
          {models.length === 0 && modelsError && (
            <span className="text-[10px] text-muted-foreground/70 max-w-[19rem] text-right leading-snug">
              {modelsError}
            </span>
          )}
        </span>
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

      {testResult && (
        <div
          className={cn(
            "px-4 py-3 border-t space-y-1.5",
            testResult.ok
              ? "border-success/25 bg-success/[0.04]"
              : "border-destructive/25 bg-destructive/[0.04]",
          )}
          data-testid="keeper-judge-test-result"
        >
          <div className={cn("text-[11px] font-medium", testResult.ok ? "text-success" : "text-destructive")}>
            {testResult.ok
              ? `This judge works — it returned a real verdict (${testResult.decision}).`
              : "This judge is not usable yet."}
          </div>
          {testResult.stages.map((st) => (
            <StageRow key={st.name} stage={st} />
          ))}
        </div>
      )}

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
