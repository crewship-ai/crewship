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

import { adminFetch } from "@/lib/admin-api"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
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
  /** Optional: a server older than the budget setting does not send it, and the
   *  card must not crash on that — it renders the built-in default instead. */
  judge_timeout_ms?: ConfigField<number>
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

/**
 * StepLabel numbers the three things that have to happen in order. The card used
 * to lead with the engine switch, which invited turning Keeper on before there
 * was a judge for it to use — and Keeper is fail-closed, so that is the one
 * ordering that produces an outage.
 */
function StepLabel({ n, children }: { n: number; children: React.ReactNode }) {
  return (
    <span className="flex items-center gap-2">
      <span className="grid place-items-center h-4 w-4 rounded-full bg-muted/60 border border-border/60 text-[9px] font-semibold text-muted-foreground shrink-0">
        {n}
      </span>
      <span>{children}</span>
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
  // Candidate addresses from the server — its own loopback, and the address the
  // browser connected FROM. The second one is the answer to "my Ollama runs on my
  // Mac, how would I know what to type": the daemon can see it, the operator
  // cannot.
  const [suggestions, setSuggestions] = useState<{ url: string; label: string }[]>([])
  const [modelsError, setModelsError] = useState<string | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<JudgeTestResult | null>(null)
  const [connecting, setConnecting] = useState(false)
  const [connectResult, setConnectResult] = useState<{ ok: boolean; detail: string } | null>(null)

  // Engine, endpoint and model share ONE draft on purpose, even though a switch
  // would normally commit on the spot. Keeper is fail-closed: enabling it
  // without a judge is refused by the server, so a lone switch on a fresh
  // instance could only fail. One Save sends all three, which is the flow the
  // endpoint was built for — turn it on and say what decides, in one write.
  // Optional chaining on every field, not just the newest one: a response that
  // is missing a field — an older server, a proxy that mangled it, a partial
  // error body — should render an unconfigured card, not throw and take the whole
  // admin page down with it. A settings card is the wrong place to be brittle,
  // because it is where somebody goes when something is already wrong.
  const form = useDirtyForm({
    enabled: cfg?.enabled?.value ?? false,
    endpoint: cfg?.judge_endpoint_url?.value ?? "",
    model: cfg?.judge_model?.value ?? "",
    // Seconds in the field, milliseconds on the wire: nobody types 20000.
    timeoutSec: String(Math.round((cfg?.judge_timeout_ms?.value ?? 20000) / 1000)),
  })

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!workspaceId) return
    try {
      const res = await adminFetch("/api/v1/admin/keeper/config", workspaceId,
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

  // Suggestions on mount, not only after a failed dial: the moment an empty
  // endpoint field is least useful is before anything has been typed, and that is
  // exactly when "your own machine is at 192.168.1.20" is worth the most.
  useEffect(() => {
    if (!workspaceId) return
    const controller = new AbortController()
    void (async () => {
      try {
        const res = await adminFetch("/api/v1/admin/keeper/judge/models", workspaceId,
          { signal: controller.signal },
        )
        if (!res.ok) return
        const body = (await res.json()) as { suggestions?: { url: string; label: string }[] }
        if (!controller.signal.aborted && body.suggestions) setSuggestions(body.suggestions)
      } catch {
        // A missing suggestion list is a missing convenience, not an error.
      }
    })()
    return () => controller.abort()
  }, [workspaceId])

  function handleSave() {
    if (!workspaceId) return
    void form.submit(async (draft) => {
      const res = await adminFetch("/api/v1/admin/keeper/config", workspaceId,
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
            judge_timeout_ms: Math.round(Number(draft.timeoutSec) * 1000),
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
        const res = await adminFetch(`/api/v1/admin/keeper/judge/models?endpoint=${encodeURIComponent(endpoint)}`, workspaceId,
          { signal: controller.signal },
        )
        if (!res.ok) {
          setModels([])
          // 403 means the caller cannot manage — silence beats a scary banner on
          // a field they cannot use anyway. Everything else is worth saying, and
          // 429 especially: the probe budget is instance-wide, so an empty list
          // with no explanation on the screen that just tripped it is the least
          // helpful thing this field can do.
          setModelsError(res.status === 403 ? null : await errorFrom(res, `Could not list models (HTTP ${res.status}).`))
          return
        }
        const body = (await res.json()) as {
          models?: string[]; error?: string; suggestions?: { url: string; label: string }[]
        }
        if (body.suggestions) setSuggestions(body.suggestions)
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

  // Connect is the cheap half of the check, asked for explicitly: one
  // GET /api/tags, no model load, no verdict. It answers "is anything there"
  // and hands back what that server has — which is the question you have before
  // you can sensibly choose a model. The debounced discovery above still runs,
  // but a background effect is not an answer: pressing a button and being told
  // yes or no is.
  async function handleConnect() {
    if (!workspaceId || connecting) return
    const endpoint = form.draft.endpoint.trim()
    if (endpoint === "") return
    setConnecting(true)
    setConnectResult(null)
    try {
      const res = await adminFetch(`/api/v1/admin/keeper/judge/models?endpoint=${encodeURIComponent(endpoint)}`, workspaceId,
      )
      if (!res.ok) {
        setConnectResult({ ok: false, detail: await errorFrom(res, `The check could not run (HTTP ${res.status})`) })
        return
      }
      const body = (await res.json()) as {
        models?: string[]; error?: string; suggestions?: { url: string; label: string }[]
      }
      if (body.suggestions) setSuggestions(body.suggestions)
      if (body.error) {
        setModels([])
        setModelsError(body.error)
        setConnectResult({ ok: false, detail: body.error })
        return
      }
      const found = body.models ?? []
      setModels(found)
      setModelsError(null)
      setConnectResult({
        ok: true,
        detail: found.length === 0
          ? "Connected — but that server has no models pulled yet. Run `ollama pull qwen2.5:7b` there."
          : `Connected · ${found.length} model${found.length === 1 ? "" : "s"} available`,
      })
      // Nothing chosen yet and exactly one candidate: choosing it for them is
      // the difference between a setup and a form.
      if (found.length === 1 && form.draft.model.trim() === "") {
        form.set("model", found[0])
      }
    } catch (e) {
      setConnectResult({ ok: false, detail: e instanceof Error ? e.message : "Could not reach that address" })
    } finally {
      setConnecting(false)
    }
  }

  // Test the DRAFT, not the saved row: finding a working combination before
  // committing it is the whole point of a test button.
  async function handleTest() {
    if (!workspaceId || testing) return
    setTesting(true)
    setTestResult(null)
    try {
      const res = await adminFetch("/api/v1/admin/keeper/judge/test", workspaceId,
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
      const res = await adminFetch("/api/v1/admin/keeper/config", workspaceId,
        { method: "DELETE" },
      )
      if (!res.ok) {
        toast.error(await errorFrom(res, `Failed to reset (HTTP ${res.status})`))
        return
      }
      const next = (await res.json()) as KeeperConfigResponse
      setCfg(next)
      setConnectResult(null)
      setTestResult(null)
      // The draft is rebased by the baseline effect once cfg lands, but only
      // while the form is clean — reset is a discard, so drop the draft too.
      form.reset()
      // Name the consequence: the override commonly IS what turned Keeper on, so
      // "reset" and "switch Keeper off" are frequently the same action.
      toast.success(
        next.enabled.value
          ? "Judge configuration reset — the server's own settings are back in force."
          : "Judge configuration reset — the server config has Keeper OFF, so the engine is now off.",
      )
    } catch (e) {
      // Without this the failure was an unhandled rejection and the operator saw
      // nothing at all — the worst outcome for a button whose whole job is to put
      // the instance back to a known state.
      toast.error(e instanceof Error ? e.message : "Could not reset the judge configuration")
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

  // The budget is a number input, which the browser lets you EMPTY and lets you
  // exceed min/max programmatically. Number("") is NaN and JSON.stringify writes
  // NaN as null, so an emptied field would send judge_timeout_ms: null and the
  // server would read it as "clear the override" — silently, on a save the
  // operator thought was about something else.
  const timeoutSec = Number(form.draft.timeoutSec)
  const timeoutInvalid =
    !Number.isFinite(timeoutSec) || !Number.isInteger(timeoutSec) || timeoutSec < 1 || timeoutSec > 120

  /**
   * A budget the last test says would actually hold, or null when the current one
   * is already comfortable.
   *
   * Doubling the measurement rather than adding a margin: the check is one warm
   * call and the first request after an idle period pays for a cold model load,
   * which is not a small difference on a 7B model. Rounded up to 5s so the number
   * in the box reads like a decision rather than a reading.
   */
  const measuredMS = testResult?.stages.find((st) => st.name === "verdict")?.latency_ms ?? 0
  const currentBudgetSec = Number(form.draft.timeoutSec) || 0
  const suggestedBudgetSec = (() => {
    if (measuredMS <= 0) return null
    const want = Math.min(120, Math.max(10, Math.ceil((measuredMS * 2) / 5000) * 5))
    // Only offer it when it would change something AND the current budget is
    // genuinely tight — a suggestion that lowers a deliberately generous budget
    // would be worse than none.
    if (want <= currentBudgetSec) return null
    return want
  })()

  return (
    <SettingsCard
      title="Credential access judge"
      description="Three steps, in order: point it at a model server, pick a model it actually has, then turn it on. Instance-wide; a workspace governance model overrides it per request. Changes apply to the next credential request — no restart."
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
      {/* Step 1. The endpoint and its own Connect button, because "is anything
          there" is a question you ask BEFORE choosing a model — and it is the
          cheap half of the check (one GET /api/tags, no model load). */}
      <SettingsRow
        label={<StepLabel n={1}>Model server</StepLabel>}
        description={
          <WithProvenance source={cfg.judge_endpoint_url?.source ?? "default"}>
            {/* Two sentences, not eight. "this machine" was the wrong words in a
                browser — the dial happens from the SERVER — and that one fact is
                worth saying every time; the rest (LAN address, OLLAMA_HOST) is
                troubleshooting, and it belongs where the trouble appears, which
                is the Connect error. */}
            Dialled <strong className="text-foreground/80">by the Crewship server</strong>, not by your
            browser — so <code className="px-1 rounded bg-muted/60 border border-border/60 font-mono">localhost</code> means
            the server&apos;s own Ollama. Press Connect to see what it has.
          </WithProvenance>
        }
      >
        <span className="flex flex-col items-end gap-1.5">
          <span className="flex items-center gap-1.5">
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
            {canEdit && (
              <Button
                variant="outline"
                size="sm"
                className="h-8 px-2.5 text-xs shrink-0"
                onClick={() => { void handleConnect() }}
                disabled={connecting || form.draft.endpoint.trim() === ""}
                data-testid="keeper-judge-connect"
              >
                {connecting ? "Connecting…" : "Connect"}
              </Button>
            )}
          </span>
          {/* Where an Ollama might be, as one-click fills. The second entry is the
              address the browser connected FROM — i.e. this laptop — which is the
              thing an operator running Ollama locally cannot look up but the
              daemon can see. Nothing is dialled until Connect. */}
          {canEdit && suggestions.length > 0 && (
            <span className="flex flex-col items-end gap-1" data-testid="keeper-judge-suggestions">
              <span className="text-[10px] text-muted-foreground/70">or try</span>
              <span className="flex flex-wrap justify-end gap-1 max-w-[21rem]">
                {suggestions.map((sg) => (
                  <button
                    key={sg.url}
                    type="button"
                    aria-pressed={sg.url === form.draft.endpoint.trim()}
                    title={sg.label}
                    onClick={() => { form.set("endpoint", sg.url); setConnectResult(null) }}
                    className={cn(
                      "h-[19px] rounded border px-1.5 font-mono text-[10px] transition-colors",
                      sg.url === form.draft.endpoint.trim()
                        ? "border-primary/50 bg-primary/[0.12] text-primary/90"
                        : "border-border/60 bg-muted/30 text-muted-foreground hover:border-border hover:text-foreground",
                    )}
                  >
                    {sg.url.replace(/^https?:\/\//, "")}
                  </button>
                ))}
              </span>
            </span>
          )}
          {connectResult && (
            <span
              className={cn(
                "text-[10px] max-w-[21rem] text-right leading-snug",
                connectResult.ok ? "text-success" : "text-destructive",
              )}
              data-testid="keeper-judge-connect-result"
            >
              {connectResult.detail}
            </span>
          )}
        </span>
      </SettingsRow>

      {/* Step 2. The models that endpoint reported. A picker, not a text field:
          a typo here is a fail-closed DENY on every credential request, and
          nobody should be typing a model tag from memory. */}
      <SettingsRow
        label={<StepLabel n={2}>Model</StepLabel>}
        description={
          <WithProvenance source={cfg.judge_model?.source ?? "default"}>
            {models.length > 0
              ? "What that server has pulled."
              : "Press Connect to list what that server has."}
          </WithProvenance>
        }
      >
        <span className="flex flex-col items-end gap-1.5">
          {/* A dropdown once we know what the server has — the conventional
              control for "one of these", and it scales: this endpoint returned
              ten models, which as chips was a wall and as a list is a list. The
              free-text field stays for the case the dropdown cannot serve, which
              is a tag you are about to pull. */}
          {models.length > 0 ? (
            <Select
              value={models.includes(form.draft.model.trim()) ? form.draft.model.trim() : undefined}
              onValueChange={(v) => form.set("model", v)}
              disabled={!canEdit}
            >
              <SelectTrigger
                className="h-8 w-[240px] text-xs font-mono"
                aria-label="Judge model"
                data-testid="keeper-judge-model-select"
              >
                <SelectValue placeholder="Pick a model" />
              </SelectTrigger>
              <SelectContent>
                {models.map((m) => (
                  <SelectItem key={m} value={m} className="text-xs font-mono">{m}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : (
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
          )}
          {/* A model that is not on the endpoint is the single most common real
              failure, and it is silent until a credential request denies. Say it
              at the field, while it is still one click from being right — and keep
              the typed value visible, because clearing it would hide the mistake
              rather than name it. */}
          {models.length > 0 && form.draft.model.trim() !== "" && !models.includes(form.draft.model.trim()) && (
            <span className="text-[11px] text-warn max-w-[21rem] text-right leading-snug" data-testid="keeper-judge-model-missing">
              <span className="font-mono">{form.draft.model.trim()}</span> is not on that server — pull it there, or pick one from the list.
            </span>
          )}
          {models.length === 0 && modelsError && (
            <span className="text-[10px] text-muted-foreground/70 max-w-[21rem] text-right leading-snug">
              {modelsError}
            </span>
          )}
        </span>
      </SettingsRow>

      {/* The budget belongs to the model, so it sits under it. A judge slower than
          this DENIES every credential request — the failure that showed three
          green ticks on dev1 and refused everything, because the budget was a 5s
          constant and a 7B model needs ~12s. */}
      <SettingsRow
        label="Time budget"
        description={
          <WithProvenance source={cfg.judge_timeout_ms?.source ?? "default"}>
            How long one credential decision may take. Keeper is fail-closed, so a judge that
            answers slower than this denies the request. Bigger model → bigger budget; Test
            measures against this number.
          </WithProvenance>
        }
      >
        <span className="flex items-center gap-1.5">
          <Input
            type="number"
            min={1}
            max={120}
            value={form.draft.timeoutSec}
            onChange={(e) => form.set("timeoutSec", e.target.value)}
            disabled={!canEdit}
            className="h-8 w-[80px] text-xs font-mono"
            aria-label="Judge time budget in seconds"
            data-testid="keeper-judge-timeout"
          />
          <span className="text-[11px] text-muted-foreground">seconds</span>
        </span>
      </SettingsRow>
      {timeoutInvalid && (
        <div role="status" className="px-4 py-2 text-[11px] text-destructive border-b border-border/40">
          The time budget must be a whole number of seconds between 1 and 120.
        </div>
      )}

      {/* Step 3. Prove it, then turn it on. Test is here rather than in the card
          header because it belongs to this step: it is the thing you do before
          flipping the switch, and it is the only check that makes the model
          actually return a verdict. */}
      <SettingsRow
        label={<StepLabel n={3}>Turn it on</StepLabel>}
        description={
          <WithProvenance source={cfg.enabled?.source ?? "default"}>
            With Keeper on, SECRET credentials are withheld from agents and must be requested — so
            test the judge first. Applies to runs started after the change. A local model costs
            nothing per decision.
          </WithProvenance>
        }
        border={false}
      >
        <span className="flex items-center gap-2">
          {canEdit && (
            <Button
              variant="soft"
              size="sm"
              className="h-8 px-2.5 text-xs"
              onClick={() => { void handleTest() }}
              disabled={testing}
              data-testid="keeper-judge-test"
            >
              {testing ? "Testing…" : "Test"}
            </Button>
          )}
          <Switch
            checked={form.draft.enabled}
            onCheckedChange={(checked) => form.set("enabled", checked)}
            disabled={!canEdit}
            aria-label="Toggle the Keeper engine"
            data-testid="keeper-judge-enabled"
          />
        </span>
      </SettingsRow>

      {/* Provider/wire is not a step — it is a fact about what the instance judge
          speaks, and it moves out of the flow into a footnote. */}
      <div className="px-4 py-2 border-t border-border/40 text-[10px] text-muted-foreground/70">
        Speaks the native Ollama API (<span className="font-mono" data-testid="keeper-judge-wire">
          {cfg.judge_provider.value || "—"}{cfg.judge_wire.value ? ` / ${cfg.judge_wire.value}` : ""}
        </span>). An OpenAI-compatible or Anthropic judge is configured per workspace as the
        governance model below, which carries its endpoint and key in the vault.
      </div>

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
          {/* The check just measured the exact number this setting wants. Making
              the operator read a latency out of a sentence and type a bigger one
              into a box above is a setting we can simply not have: the budget is
              a consequence of the model they picked, and we know what that model
              does on this hardware. */}
          {suggestedBudgetSec !== null && (
            <button
              type="button"
              onClick={() => form.set("timeoutSec", String(suggestedBudgetSec))}
              className="text-[11px] underline decoration-dotted text-foreground/80 hover:text-foreground"
              data-testid="keeper-judge-apply-budget"
            >
              Set the budget to {suggestedBudgetSec}s and save
            </button>
          )}
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
          canSave={!failClosed && !timeoutInvalid}
          onSave={handleSave}
          onCancel={form.reset}
        />
      )}
    </SettingsCard>
  )
}
