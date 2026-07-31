"use client"

import { useCallback, useEffect, useState } from "react"
import { RefreshCw } from "lucide-react"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import { adminFetch } from "@/lib/admin-api"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { SettingsCard, SettingsRow, SettingsEmpty } from "@/components/features/settings/shared"
import { useCredentials } from "@/components/features/mcp/hooks/use-credentials"
import { cn } from "@/lib/utils"

/**
 * Which model judges what, whether it can actually run — and, now, what it
 * should be.
 *
 * Replaces the old Settings → Auxiliary Models card, which was a
 * configuration echo: it printed llm.AuxiliaryModels and labelled every row
 * "explicit" regardless of whether that provider could be built. Three
 * things were wrong with it and all three are why this exists:
 *
 *  1. It lived in workspace Settings, next to per-workspace cards, while
 *     the config it showed is process-wide. Creating a workspace did not
 *     change it. It belongs in Admin, beside the keeper governance panel
 *     that actually edits a judge.
 *
 *  2. It listed a `keeper` slot that nothing in the codebase consumes,
 *     while the real credential-access judge — built from cfg.Keeper on a
 *     separate path — was absent. So the row an operator read as "the
 *     keeper" named a model the keeper never used.
 *
 *  3. It could not report a problem. The backend now says whether each
 *     judge's provider is buildable and why not, and this renders that.
 *
 * It was read-only for a while, on the argument that a second editable
 * surface for the same value is how inconsistencies start. What that
 * actually left was a card that told an operator five evaluators were
 * pinned to a paid model and offered no way to change one — the per-token
 * spend of the Keeper stack, visible and untouchable. The rows are editable
 * here now, and the precedence is explicit rather than implied: the
 * per-workspace governance model still wins per request, this is the
 * instance layer under it, and each field shows whether it is overridden
 * here or inherited from the server's configuration.
 */

interface Subsystem {
  id: string
  label: string
  provider: string
  model: string
  timeout_ms?: number
  source: string
  healthy: boolean
  detail?: string
  /** Absent = not probed (paid provider). true/false = the model server did
   *  or did not answer just now. */
  reachable?: boolean
  reach_detail?: string
}

/** One {value, source} field of the aux config — see internal/api/admin_keeper_aux.go. */
interface AuxField<T> {
  value: T
  source: string
  editable: boolean
}

interface AuxSlot {
  slot: string
  label: string
  applies_at: string
  provider: AuxField<string>
  model: AuxField<string>
  timeout_ms: AuxField<number>
  /** Which stored key this slot spends (#1554). Optional: a server from before
   *  the field existed sends nothing, and the row then renders without a picker
   *  rather than claiming the key is unset. `editable: false` means the server
   *  has no way to VERIFY a chosen credential, so it must not offer one. */
  credential_id?: AuxField<string>
  overridden: boolean
}

interface AuxConfig {
  slots: AuxSlot[]
  providers: string[]
  judge_provider: string
  judge_model: string
  any_overridden: boolean
}

/**
 * A judge is usable only if it is BOTH configured-and-buildable and, where we
 * can check, actually answering. Keeping the two apart matters: an Ollama
 * provider constructs without ever dialling, so a box with no model server
 * running reported a perfectly healthy judge.
 */
function isUsable(r: Subsystem): boolean {
  return r.healthy && r.reachable !== false
}

/**
 * The Reviews evaluator a config slot configures, or null when it configures
 * something that cannot be asked a question on demand.
 *
 * The card is keyed by aux slot (`curator`, `negative`) because that is what the
 * config endpoint returns; the run route is keyed by evaluator (`skill-review`,
 * `negative-learning`). `run_summary` writes a verdict at the end of a run and
 * `fallback` is not an evaluator at all — neither has anything to run, so
 * neither gets a button.
 */
function reviewSlotFor(slot: string): string | null {
  switch (slot) {
    case "curator": return "skill-review"
    case "behavior": return "behavior"
    case "memory_health": return "memory-health"
    case "negative": return "negative-learning"
    default: return null
  }
}

/** The Select's stand-in for "no credential" — Radix has no empty-string value. */
const AUX_CREDENTIAL_NONE = "__none__"

/**
 * Only an API_KEY can back an evaluator. The endpoint a hosted evaluator dials
 * is ours (api.anthropic.com / api.openai.com), so an ENDPOINT_URL credential
 * has nothing to do here — offering one would give the operator a row that saves
 * cleanly and then authenticates with a URL. The judge's own picker is wider
 * (it accepts ENDPOINT_URL, because its endpoint IS configurable); this is the
 * narrower half of the same question.
 */
const AUX_CREDENTIAL_TYPE = "API_KEY"

/** Provenance, in the words an operator needs to decide whether Reset does anything. */
function sourceNote(source: string): string {
  switch (source) {
    case "instance": return "set here"
    case "env": return "from server config"
    case "default": return "shipped default"
    default: return ""
  }
}

export function JudgeModelsCard({ workspaceId }: { workspaceId: string | null }) {
  const [rows, setRows] = useState<Subsystem[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  // The editable half. Absent (older server, or a failed read) degrades this
  // card to exactly what it was before: status only, nothing to press.
  const [aux, setAux] = useState<AuxConfig | null>(null)
  const [busy, setBusy] = useState(false)
  // Per-slot probe results. The card's default is still "not probed" — rendering
  // a status page must not call a paid API — but "is this evaluator actually
  // usable" was a question with no answer until a sweep ran and failed, which is
  // the worst moment to find out.
  const [probing, setProbing] = useState<string | null>(null)
  const [probeResults, setProbeResults] = useState<Record<string, { ok: boolean; detail: string }>>({})
  // Per-slot manual runs (#1555). "Test" says whether the model answers; this
  // says what the check actually finds. Kept separate from probing so a slot
  // can be tested and run without the two results overwriting each other.
  const [running, setRunning] = useState<string | null>(null)
  const [runResults, setRunResults] = useState<Record<string, { ok: boolean; detail: string }>>({})
  // Which stored keys a slot may be pointed at (#1554). Same hook the governance
  // model's picker uses, so the two surfaces cannot disagree about what the
  // workspace holds — the list is filtered narrower here (see AUX_CREDENTIAL_TYPE).
  const { credentials } = useCredentials(workspaceId ?? undefined)
  // The hook assigns whatever the endpoint returned without checking its shape,
  // so a non-array body would take this whole card down — and this card's job is
  // to REPORT breakage, not to become it.
  const keyCredentials = Array.isArray(credentials)
    ? credentials.filter((c) => c.type === AUX_CREDENTIAL_TYPE)
    : []

  const load = useCallback(async () => {
    if (!workspaceId) return
    try {
      const res = await apiFetch(`/api/v1/system/aux-status?workspace_id=${encodeURIComponent(workspaceId)}`)
      if (!res.ok) throw new Error(String(res.status))
      const data = await res.json()
      setRows(Array.isArray(data?.subsystems) ? data.subsystems : [])
      setError(null)
    } catch {
      // Not an empty list: "no judges configured" would read as a clean
      // system, which is the opposite of what a failed read means.
      setError("Couldn't load judge status.")
    }
  }, [workspaceId])

  const loadAux = useCallback(async () => {
    // The evaluator config is instance-scoped, but the route is behind
    // RequireWorkspace like every other admin route — it 400s without
    // workspace_id. Waiting for one is the difference between an editable card
    // and the read-only fallback, which is what this looked like for a while:
    // five evaluators visibly configured and none of them touchable.
    if (!workspaceId) return
    try {
      const res = await adminFetch("/api/v1/admin/keeper/aux", workspaceId)
      if (!res.ok) throw new Error(String(res.status))
      const data = await res.json()
      setAux(Array.isArray(data?.slots) ? (data as AuxConfig) : null)
    } catch {
      // Deliberately silent: the status half of the card still works, and an
      // error banner for a missing edit surface would bury the health rows
      // that are the reason an operator opened this.
      setAux(null)
    }
  }, [workspaceId])

  useEffect(() => { load() }, [load])
  useEffect(() => { loadAux() }, [loadAux])

  /** PUT one slot, then re-read both halves so health follows the new model. */
  const saveSlot = useCallback(async (slot: string, patch: Record<string, unknown>) => {
    setBusy(true)
    try {
      const res = await adminFetch(`/api/v1/admin/keeper/aux/${encodeURIComponent(slot)}`, workspaceId, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(patch),
      })
      const body = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(body?.error || `HTTP ${res.status}`)
      setAux(body as AuxConfig)
      await load()
      toast.success("Evaluator model saved.")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not save")
    } finally {
      setBusy(false)
    }
  }, [load, workspaceId])

  const switchToLocalJudge = useCallback(async () => {
    setBusy(true)
    try {
      const res = await adminFetch("/api/v1/admin/keeper/aux/use-judge", workspaceId, { method: "POST" })
      const body = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(body?.error || `HTTP ${res.status}`)
      setAux(body as AuxConfig)
      await load()
      // Names the trade rather than just confirming: local models are smaller
      // than the hosted defaults, so this buys cost with bluntness.
      toast.success("Every evaluator now runs on the local judge — no per-token cost, blunter findings.")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not switch the evaluators")
    } finally {
      setBusy(false)
    }
  }, [load, workspaceId])

  const resetAll = useCallback(async () => {
    setBusy(true)
    try {
      const res = await adminFetch("/api/v1/admin/keeper/aux", workspaceId, { method: "DELETE" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const body = await res.json().catch(() => null)
      if (body) setAux(body as AuxConfig)
      await load()
      // The consequence, not just the action: resetting off the local judge
      // puts the sweeps back on a paid model.
      toast.success("Evaluator overrides cleared — the server's configured models are back in force.")
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not reset")
    } finally {
      setBusy(false)
    }
  }, [load, workspaceId])

  const probeSlot = useCallback(async (slot: string) => {
    setProbing(slot)
    try {
      const res = await adminFetch(`/api/v1/admin/keeper/aux/${encodeURIComponent(slot)}/probe`, workspaceId, { method: "POST" })
      const body = await res.json().catch(() => ({}))
      if (!res.ok) {
        setProbeResults((p) => ({ ...p, [slot]: { ok: false, detail: body?.error || `HTTP ${res.status}` } }))
        return
      }
      // The last stage carries the most specific answer — a verdict that arrived
      // too slowly is a different failure from one that never arrived, and the
      // budget stage is the one that says which.
      const stages = (body?.stages ?? []) as { ok: boolean; detail: string }[]
      const worst = stages.find((st) => !st.ok) ?? stages[stages.length - 1]
      setProbeResults((p) => ({
        ...p,
        [slot]: { ok: Boolean(body?.ok), detail: worst?.detail ?? "no answer" },
      }))
    } catch (e) {
      setProbeResults((p) => ({
        ...p,
        [slot]: { ok: false, detail: e instanceof Error ? e.message : "Network error" },
      }))
    } finally {
      setProbing(null)
    }
  }, [workspaceId])

  /**
   * Run the evaluator this row configures, now.
   *
   * Sent with no subject: the server picks one from the workspace (the stalest
   * skill, the crew's memory, the last recorded failure) so a manual check is
   * one press rather than a form. What comes back is a real verdict, recorded
   * in the Keeper audit log like any scheduled one.
   */
  const runSlot = useCallback(async (slot: string) => {
    const evaluator = reviewSlotFor(slot)
    if (!evaluator) return
    setRunning(slot)
    try {
      const res = await adminFetch(`/api/v1/admin/keeper/review/${evaluator}/run`, workspaceId, { method: "POST" })
      const body = await res.json().catch(() => ({}))
      if (!res.ok) {
        // The server's own words. Its refusals name the thing that is missing
        // ("no skill is assigned to an agent in this workspace"), which a
        // generic "run failed" would throw away.
        setRunResults((p) => ({ ...p, [slot]: { ok: false, detail: body?.error || `HTTP ${res.status}` } }))
        return
      }
      const decision = String(body?.decision ?? "")
      const reason = String(body?.reason ?? "")
      setRunResults((p) => ({
        ...p,
        [slot]: { ok: decision === "ALLOW", detail: reason ? `${decision} — ${reason}` : decision || "no verdict" },
      }))
    } catch (e) {
      setRunResults((p) => ({
        ...p,
        [slot]: { ok: false, detail: e instanceof Error ? e.message : "Network error" },
      }))
    } finally {
      setRunning(null)
    }
  }, [workspaceId])

  // The five background evaluators are collapsed by default. On a default
  // instance they are five identical rows of "anthropic / claude-haiku-4-5", and
  // a wall of the same technical detail five times is not information — it is
  // noise that hides the one row that matters, the credential judge. Expanded
  // only when an operator asks, or when they are NOT all the same (in which case
  // the difference is the point).
  const [showEach, setShowEach] = useState(false)

  const unusable = (rows ?? []).filter((r) => r.id !== "access_gatekeeper" && !isUsable(r)).length
  const auxBySlot = new Map((aux?.slots ?? []).map((s) => [s.slot, s]))
  // Slots the status endpoint does not report (today: `fallback`) still belong
  // in the list — an evaluator that is configurable but invisible is a knob an
  // operator cannot find.
  const extraSlots = (aux?.slots ?? []).filter((s) => !(rows ?? []).some((r) => r.id === s.slot))

  // The credential judge is the row an operator opens this card for: it is the
  // one in the path of every credential request. Everything else runs on a
  // schedule and can be a summary until asked about.
  // The credential judge is NOT listed here. It has its own card directly above,
  // with its own Test, and the page's status strip already reports whether it is
  // answering — three copies of one fact, of which this was the least actionable
  // (it could be looked at and not changed).
  const evaluatorRows = (rows ?? []).filter((r) => r.id !== "access_gatekeeper")
  const allSlots = aux?.slots ?? []
  // Provider AND model. Keying on the model alone printed
  // "<first slot's provider> / <model>" for a set that agreed on the model and
  // disagreed on the provider — a summary asserting something it had not checked,
  // and the provider is the half that decides whether the row costs money.
  const uniformModel =
    allSlots.length > 0 &&
    allSlots.every(
      (sl) => sl.model.value === allSlots[0].model.value && sl.provider.value === allSlots[0].provider.value,
    )
      ? `${allSlots[0].provider.value} / ${allSlots[0].model.value}`
      : null
  // A tier that costs money says so. "ollama" is the local judge and bills
  // nothing; anything else is per-token, which is the fact an operator is
  // actually deciding about.
  const evaluatorsAreFree = allSlots.length > 0 && allSlots.every((sl) => sl.provider.value === "ollama")
  const expanded = showEach || uniformModel === null
  const orderedRows = expanded ? evaluatorRows : []

  return (
    <SettingsCard
      title="Background checks"
      description="The scheduled reviews: skills, the tool-call watchdog, memory audits, failure lessons and run summaries. They run on a timer, not in the credential path — nothing waits on them, and the credential judge above is unaffected by anything here."
      actions={
        <>
          {aux && aux.judge_model && (
            <Button
              variant="ghost" size="sm" className="h-7 px-2.5 gap-1.5 text-xs"
              disabled={busy} onClick={() => void switchToLocalJudge()}
            >
              Use local judge for all
            </Button>
          )}
          {aux?.any_overridden && (
            <Button
              variant="ghost" size="sm" className="h-7 px-2.5 gap-1.5 text-xs"
              disabled={busy} onClick={() => void resetAll()}
            >
              Reset all
            </Button>
          )}
          <Button variant="ghost" size="sm" className="h-7 px-2.5 gap-1.5 text-xs" onClick={() => { void load(); void loadAux() }}>
            <RefreshCw className="size-3" />Refresh
          </Button>
        </>
      }
    >
      {error ? (
        <SettingsEmpty>
          <div className="space-y-2">
            <div className="text-destructive">{error}</div>
            <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={() => { setError(null); load() }}>
              Retry
            </Button>
          </div>
        </SettingsEmpty>
      ) : rows === null ? (
        <div className="px-4 py-3 space-y-2">
          <Skeleton className="h-7 w-full" />
          <Skeleton className="h-7 w-full" />
        </div>
      ) : evaluatorRows.length === 0 && extraSlots.length === 0 ? (
        <SettingsEmpty>No background checks are wired into this build.</SettingsEmpty>
      ) : (
        <>
          {/* "fail closed" is jargon that reads as a state rather than a
              consequence. What an operator needs is what will HAPPEN, in words
              they can act on. */}
          {unusable > 0 && (
            <div role="status" className="px-4 py-2 text-xs text-destructive border-b border-border/40 bg-destructive/[0.05]">
              {unusable === 1 ? "One check cannot run" : `${unusable} checks cannot run`} right now. Anything that
              needs {unusable === 1 ? "it" : "them"} is refused rather than allowed — Keeper never guesses.
            </div>
          )}
          {orderedRows.map((r) => (
            <SettingsRow
              key={r.id}
              label={
                <span className="flex items-center gap-2">
                  <span
                    aria-hidden="true"
                    className={`size-1.5 rounded-full shrink-0 ${
                      !isUsable(r) ? "bg-destructive" : r.reachable === undefined ? "bg-muted-foreground/50" : "bg-success"
                    }`}
                  />
                  <span>{r.label}</span>
                </span>
              }
              description={
                // Verbatim from the server. A dot that does not say what is
                // wrong — or why it is grey — cannot be acted on.
                r.detail || r.reach_detail ? (
                  <span
                    role="status"
                    className={`block max-w-[28rem] whitespace-normal break-words ${
                      isUsable(r) ? "text-muted-foreground/80" : "text-destructive/90"
                    }`}
                  >
                    {r.detail
                      ? `Not running — ${r.detail}`
                      : r.reachable === false
                        ? `Not answering — ${r.reach_detail}`
                        : r.reach_detail}
                  </span>
                ) : undefined
              }
            >
              {auxBySlot.has(r.id) ? (
                <SlotEditor
                  slot={auxBySlot.get(r.id)!}
                  providers={aux?.providers ?? []}
                  credentials={keyCredentials}
                  workspaceId={workspaceId}
                  busy={busy}
                  onSave={saveSlot}
                  onProbe={probeSlot}
                  probing={probing === r.id}
                  probeResult={probeResults[r.id]}
                  onRun={runSlot}
                  runningNow={running === r.id}
                  runResult={runResults[r.id]}
                />
              ) : (
                <span className="text-xs text-muted-foreground font-mono tabular-nums text-right">
                  {r.provider || "—"}
                  {r.model ? ` / ${r.model}` : ""}
                  {r.timeout_ms ? ` · ${Math.round(r.timeout_ms / 1000)}s` : ""}
                </span>
              )}
            </SettingsRow>
          ))}
          {/* One row instead of five. On a default instance the five background
              checks are the SAME model with different timeouts, and printing
              "anthropic / claude-haiku-4-5" five times buries the credential judge
              above it. The detail is one click away and stays open once asked
              for; when they are NOT all the same, the card expands on its own,
              because then the difference is the information. */}
          {allSlots.length > 0 && (
            <SettingsRow
              label="Background checks"
              description={
                <span className="block max-w-[30rem] leading-snug text-muted-foreground/80">
                  Skill reviews, the tool-call watchdog, memory audits and run summaries. They run on a
                  schedule, not in the credential path — nothing waits on them.{" "}
                  {evaluatorsAreFree
                    ? "Running on your local model, so they cost nothing."
                    : "These call a paid model, so they cost money per run."}
                </span>
              }
            >
              <span className="flex items-center gap-2">
                {uniformModel && !expanded && (
                  <span className="font-mono text-xs text-muted-foreground">
                    all {allSlots.length} on {uniformModel}
                  </span>
                )}
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 px-2.5 text-xs"
                  onClick={() => setShowEach((v) => !v)}
                  data-testid="keeper-aux-toggle"
                >
                  {expanded ? "Hide detail" : "Change"}
                </Button>
              </span>
            </SettingsRow>
          )}

          {expanded && extraSlots.map((s) => (
            <SettingsRow key={s.slot} label={s.label}>
              <SlotEditor
                slot={s}
                providers={aux?.providers ?? []}
                credentials={keyCredentials}
                workspaceId={workspaceId}
                busy={busy}
                onSave={saveSlot}
                onProbe={probeSlot}
                probing={probing === s.slot}
                probeResult={probeResults[s.slot]}
                onRun={runSlot}
                runningNow={running === s.slot}
                runResult={runResults[s.slot]}
              />
            </SettingsRow>
          ))}
        </>
      )}
    </SettingsCard>
  )
}

/**
 * One slot's provider + model, editable in place.
 *
 * Provider is a select over what the server says it can BUILD — narrower than
 * the model catalogue, which offers Gemini ids this build has no provider for.
 * Model is a picker rather than a text field for the same reason it is one on
 * the crew canvas: typing a model id from memory is not a feature, and a typo
 * here degrades an evaluator silently.
 */
function SlotEditor({
  slot, providers, credentials, workspaceId, busy, onSave, onProbe, probing, probeResult,
  onRun, runningNow, runResult,
}: {
  slot: AuxSlot
  providers: string[]
  credentials: { id: string; name: string }[]
  workspaceId: string | null
  busy: boolean
  onSave: (slot: string, patch: Record<string, unknown>) => Promise<void>
  onProbe: (slot: string) => Promise<void>
  probing: boolean
  probeResult?: { ok: boolean; detail: string }
  onRun: (slot: string) => Promise<void>
  runningNow: boolean
  runResult?: { ok: boolean; detail: string }
}) {
  const evaluator = reviewSlotFor(slot.slot)
  // The models this slot's provider can serve. Loaded with the row rather than
  // behind a dialog: a control that has to be OPENED before it can tell you what
  // it contains is a control you have to interrogate, and the whole complaint
  // about this page was having to interrogate it.
  const [catalogue, setCatalogue] = useState<string[]>([])
  const [catalogueError, setCatalogueError] = useState<string | null>(null)
  const provider = slot.provider.value
  useEffect(() => {
    if (!workspaceId || !provider) { setCatalogue([]); return }
    const controller = new AbortController()
    void (async () => {
      try {
        const res = provider === "ollama"
          ? await adminFetch("/api/v1/admin/keeper/judge/models", workspaceId, { signal: controller.signal })
          : await apiFetch(
              `/api/v1/models?provider=${encodeURIComponent(provider.toUpperCase())}&workspace_id=${encodeURIComponent(workspaceId)}`,
              { signal: controller.signal },
            )
        const body = await res.json().catch(() => ({}))
        if (controller.signal.aborted) return
        if (!res.ok || body?.error) {
          setCatalogue([])
          setCatalogueError(body?.error ?? body?.detail ?? null)
          return
        }
        setCatalogueError(null)
        setCatalogue(
          provider === "ollama"
            ? ((body?.models ?? []) as string[])
            : ((body?.models ?? []) as { id: string }[]).map((m) => m.id),
        )
      } catch (e) {
        if (e instanceof DOMException && e.name === "AbortError") return
        setCatalogue([])
      }
    })()
    return () => controller.abort()
  }, [provider, workspaceId])

  const pinnedKey = slot.credential_id?.value ?? ""
  const showKeyPicker = Boolean(slot.credential_id?.editable) && provider !== "ollama"

  return (
    <div className="flex flex-col items-end gap-1">
      <div className="flex items-center gap-1.5">
        <Select
          value={slot.provider.value || undefined}
          disabled={busy}
          onValueChange={(next) => {
            if (next === slot.provider.value) return
            // A provider alone cannot resolve — the builder needs both, and the
            // server refuses it — so the current model rides along and the
            // dropdown beside this reloads with what the new provider offers.
            void onSave(slot.slot, { provider: next, model: slot.model.value })
          }}
        >
          <SelectTrigger size="sm" className="h-7 w-[7.5rem] text-xs" aria-label={`${slot.label} provider`}>
            <SelectValue placeholder="provider" />
          </SelectTrigger>
          <SelectContent>
            {providers.map((p) => (
              <SelectItem key={p} value={p} className="text-xs">{p}</SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={catalogue.includes(slot.model.value) ? slot.model.value : undefined}
          disabled={busy || catalogue.length === 0}
          onValueChange={(next) => {
            if (next === slot.model.value) return
            void onSave(slot.slot, { provider: slot.provider.value, model: next })
          }}
        >
          <SelectTrigger
            size="sm"
            className="h-7 min-w-[11rem] text-xs font-mono"
            aria-label={`${slot.label} model`}
            data-testid={`keeper-aux-model-${slot.slot}`}
          >
            <SelectValue placeholder={catalogue.length === 0 ? "loading…" : "choose a model"} />
          </SelectTrigger>
          <SelectContent>
            {catalogue.map((m) => (
              <SelectItem key={m} value={m} className="text-xs font-mono">{m}</SelectItem>
            ))}
          </SelectContent>
        </Select>

        {/* Which stored key this slot spends. Absent for a local ("ollama")
            evaluator, which dials the instance judge endpoint and needs none —
            a picker there would be a control wired to nothing. Absent too when
            the server cannot VERIFY a chosen credential (editable: false) or
            predates the field: offering a choice that is never checked is how a
            key from the wrong workspace gets bound. */}
        {showKeyPicker && (
          <Select
            value={pinnedKey === "" ? AUX_CREDENTIAL_NONE : pinnedKey}
            disabled={busy}
            onValueChange={(next) => {
              const id = next === AUX_CREDENTIAL_NONE ? "" : next
              if (id === pinnedKey) return
              void onSave(slot.slot, { credential_id: id })
            }}
          >
            <SelectTrigger
              size="sm"
              className="h-7 w-[9.5rem] text-[11px]"
              aria-label={`${slot.label} key`}
              data-testid={`keeper-aux-credential-${slot.slot}`}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={AUX_CREDENTIAL_NONE} className="text-[11px]">
                server&apos;s own key
              </SelectItem>
              {credentials.map((c) => (
                <SelectItem key={c.id} value={c.id} className="text-[11px]">{c.name}</SelectItem>
              ))}
              {/* A pinned key that is no longer listed — revoked, or from a
                  workspace this view cannot see — stays selectable. Rendering
                  the picker blank would let the next save silently clear it, and
                  this is precisely the row that stopped working. */}
              {pinnedKey !== "" && !credentials.some((c) => c.id === pinnedKey) && (
                <SelectItem value={pinnedKey} className="text-[11px]">
                  {pinnedKey} (unavailable)
                </SelectItem>
              )}
            </SelectContent>
          </Select>
        )}
      </div>

      {/* The configured model is not one the provider offers. Silent until a
          sweep fails, so it is said here — and the value is spelled out, because
          the dropdown cannot show a selection that is not in its list. */}
      {slot.model.value !== "" && catalogue.length > 0 && !catalogue.includes(slot.model.value) && (
        <span className="max-w-[22rem] text-right text-xs leading-snug text-warn">
          <span className="font-mono">{slot.model.value}</span> is not offered by {slot.provider.value} — pick one from the list.
        </span>
      )}
      {catalogueError && (
        <span className="max-w-[22rem] text-right text-xs leading-snug text-muted-foreground">
          {catalogueError}
        </span>
      )}

      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        {/* One real evaluation, on request. Explicitly a button rather than
            something the page does on load: probing five hosted evaluators to
            render a status card would bill the operator for looking. */}
        <button
          type="button"
          disabled={busy || probing || !slot.model.value}
          onClick={() => void onProbe(slot.slot)}
          className="underline decoration-dotted hover:text-foreground disabled:opacity-50"
          data-testid={`keeper-aux-probe-${slot.slot}`}
        >
          {probing ? "testing…" : "test"}
        </button>
        {/* Test asks whether the model answers. Run asks the question the
            evaluator exists to ask — and until this button there was no way to
            ask it except by waiting for the nightly sweep, which is why the
            behaviour watchdog had never run outside its own tests. */}
        {evaluator && (
          <button
            type="button"
            disabled={busy || runningNow || !slot.model.value}
            onClick={() => void onRun(slot.slot)}
            className="underline decoration-dotted hover:text-foreground disabled:opacity-50"
            data-testid={`keeper-review-run-${slot.slot}`}
            title="Run this check now against your workspace — the verdict is recorded in the Keeper log"
          >
            {runningNow ? "running…" : "run now"}
          </button>
        )}
        <span>{sourceNote(slot.model.source)}</span>
        {slot.timeout_ms.value > 0 && <span className="tabular-nums">{Math.round(slot.timeout_ms.value / 1000)}s</span>}
        {/* Named per row: an operator who changes this one and sees no change in
            behaviour would otherwise conclude the save silently failed. */}
        {slot.applies_at === "restart" && <span className="text-warn">needs restart</span>}
        {slot.overridden && (
          <button
            type="button"
            disabled={busy}
            className="underline decoration-dotted hover:text-foreground"
            onClick={() => void onSave(slot.slot, { provider: "", model: "", timeout_ms: 0 })}
          >
            reset
          </button>
        )}
      </div>

      {probeResult && (
        <span
          role="status"
          className={cn(
            "max-w-[22rem] text-right text-xs leading-snug",
            probeResult.ok ? "text-success" : "text-destructive",
          )}
        >
          {probeResult.detail}
        </span>
      )}

      {/* The verdict, verbatim. An ESCALATE is not an error — it is the check
          working — so a non-ALLOW result is flagged as attention, not failure. */}
      {runResult && (
        <span
          role="status"
          className={cn(
            "max-w-[22rem] text-right text-[10px] leading-snug",
            runResult.ok ? "text-success" : "text-warn",
          )}
        >
          {runResult.detail}
        </span>
      )}

    </div>
  )
}
