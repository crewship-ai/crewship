"use client"

import { useCallback, useEffect, useState } from "react"
import { RefreshCw, ChevronsUpDown, Check, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
} from "@/components/ui/command"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { SettingsCard, SettingsRow, SettingsEmpty } from "@/components/features/settings/shared"
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
      const res = await apiFetch(`/api/v1/admin/keeper/aux?workspace_id=${encodeURIComponent(workspaceId ?? "")}`)
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
      const res = await apiFetch(
        `/api/v1/admin/keeper/aux/${encodeURIComponent(slot)}?workspace_id=${encodeURIComponent(workspaceId ?? "")}`, {
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
      const res = await apiFetch(
        `/api/v1/admin/keeper/aux/use-judge?workspace_id=${encodeURIComponent(workspaceId ?? "")}`,
        { method: "POST" })
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
      const res = await apiFetch(
        `/api/v1/admin/keeper/aux?workspace_id=${encodeURIComponent(workspaceId ?? "")}`,
        { method: "DELETE" })
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
      const res = await apiFetch(
        `/api/v1/admin/keeper/aux/${encodeURIComponent(slot)}/probe?workspace_id=${encodeURIComponent(workspaceId ?? "")}`,
        { method: "POST" })
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

  // The five background evaluators are collapsed by default. On a default
  // instance they are five identical rows of "anthropic / claude-haiku-4-5", and
  // a wall of the same technical detail five times is not information — it is
  // noise that hides the one row that matters, the credential judge. Expanded
  // only when an operator asks, or when they are NOT all the same (in which case
  // the difference is the point).
  const [showEach, setShowEach] = useState(false)

  const unusable = (rows ?? []).filter((r) => !isUsable(r)).length
  const auxBySlot = new Map((aux?.slots ?? []).map((s) => [s.slot, s]))
  // Slots the status endpoint does not report (today: `fallback`) still belong
  // in the list — an evaluator that is configurable but invisible is a knob an
  // operator cannot find.
  const extraSlots = (aux?.slots ?? []).filter((s) => !(rows ?? []).some((r) => r.id === s.slot))

  // The credential judge is the row an operator opens this card for: it is the
  // one in the path of every credential request. Everything else runs on a
  // schedule and can be a summary until asked about.
  const judgeRow = (rows ?? []).find((r) => r.id === "access_gatekeeper")
  const evaluatorRows = (rows ?? []).filter((r) => r.id !== "access_gatekeeper")
  const allSlots = aux?.slots ?? []
  const uniformModel =
    allSlots.length > 0 && allSlots.every((sl) => sl.model.value === allSlots[0].model.value)
      ? `${allSlots[0].provider.value} / ${allSlots[0].model.value}`
      : null
  // A tier that costs money says so. "ollama" is the local judge and bills
  // nothing; anything else is per-token, which is the fact an operator is
  // actually deciding about.
  const evaluatorsAreFree = allSlots.length > 0 && allSlots.every((sl) => sl.provider.value === "ollama")
  const expanded = showEach || uniformModel === null
  const orderedRows = judgeRow
    ? (expanded ? [judgeRow, ...evaluatorRows] : [judgeRow])
    : (expanded ? evaluatorRows : [])

  return (
    <SettingsCard
      title="Which model decides"
      description="The judge that answers credential requests, and the models behind the background checks. Applies to the whole instance — a workspace can override it under Judge for this workspace."
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
      ) : rows.length === 0 && extraSlots.length === 0 ? (
        <SettingsEmpty>No judge models are wired into this build.</SettingsEmpty>
      ) : (
        <>
          {/* "fail closed" is jargon that reads as a state rather than a
              consequence. What an operator needs is what will HAPPEN, in words
              they can act on. */}
          {unusable > 0 && (
            <div role="status" className="px-4 py-2 text-[11px] text-destructive border-b border-border/40 bg-destructive/[0.05]">
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
                  workspaceId={workspaceId}
                  busy={busy}
                  onSave={saveSlot}
                  onProbe={probeSlot}
                  probing={probing === r.id}
                  probeResult={probeResults[r.id]}
                />
              ) : (
                <span className="text-[11px] text-muted-foreground font-mono tabular-nums text-right">
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
                  <span className="font-mono text-[11px] text-muted-foreground">
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
                workspaceId={workspaceId}
                busy={busy}
                onSave={saveSlot}
                onProbe={probeSlot}
                probing={probing === s.slot}
                probeResult={probeResults[s.slot]}
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
  slot, providers, workspaceId, busy, onSave, onProbe, probing, probeResult,
}: {
  slot: AuxSlot
  providers: string[]
  workspaceId: string | null
  busy: boolean
  onSave: (slot: string, patch: Record<string, unknown>) => Promise<void>
  onProbe: (slot: string) => Promise<void>
  probing: boolean
  probeResult?: { ok: boolean; detail: string }
}) {
  const [pickerOpen, setPickerOpen] = useState(false)

  return (
    <div className="flex flex-col items-end gap-1">
      <div className="flex items-center gap-1.5">
        <Select
          value={slot.provider.value || undefined}
          disabled={busy}
          onValueChange={(next) => {
            if (next === slot.provider.value) return
            // A provider alone cannot resolve (the builder needs both, and the
            // server refuses it), so changing provider opens the model picker
            // rather than saving a half-configured slot.
            void onSave(slot.slot, { provider: next, model: slot.model.value }).then(() => setPickerOpen(true))
          }}
        >
          <SelectTrigger size="sm" className="h-7 w-[7.5rem] text-[11px]" aria-label={`${slot.label} provider`}>
            <SelectValue placeholder="provider" />
          </SelectTrigger>
          <SelectContent>
            {providers.map((p) => (
              <SelectItem key={p} value={p} className="text-[11px]">{p}</SelectItem>
            ))}
          </SelectContent>
        </Select>

        <button
          type="button"
          disabled={busy}
          onClick={() => setPickerOpen(true)}
          aria-label={`${slot.label} model: ${slot.model.value || "not set"}`}
          className={cn(
            "flex h-7 min-w-[10rem] items-center gap-1.5 rounded-lg border border-border bg-background px-2.5",
            "text-left text-[11px] font-mono text-foreground outline-none transition-[border-color]",
            "hover:border-foreground/25 focus:border-primary disabled:opacity-60",
          )}
        >
          <span className="min-w-0 flex-1 truncate">{slot.model.value || "choose a model"}</span>
          <ChevronsUpDown className="size-3 shrink-0 text-muted-foreground" />
        </button>
      </div>

      <div className="flex items-center gap-2 text-[10px] text-muted-foreground">
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
            "max-w-[22rem] text-right text-[10px] leading-snug",
            probeResult.ok ? "text-success" : "text-destructive",
          )}
        >
          {probeResult.detail}
        </span>
      )}

      <ModelPicker
        open={pickerOpen}
        onOpenChange={setPickerOpen}
        provider={slot.provider.value}
        workspaceId={workspaceId}
        current={slot.model.value}
        onPick={(id) => void onSave(slot.slot, { provider: slot.provider.value, model: id })}
      />
    </div>
  )
}

/**
 * The model list for one provider.
 *
 * Two sources, because there are two kinds of provider here: a hosted one is
 * listed by GET /api/v1/models (live from the workspace credential, curated
 * fallback when there is no key) — the same endpoint the crew canvas picker
 * uses, so the two cannot drift into offering different Claude ids. A local
 * one is whatever the instance judge's endpoint actually serves, which only
 * the judge-models probe can answer.
 */
function ModelPicker({
  open, onOpenChange, provider, workspaceId, current, onPick,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  provider: string
  workspaceId: string | null
  current: string
  onPick: (id: string) => void
}) {
  const [models, setModels] = useState<string[] | null>(null)
  const [note, setNote] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    const run = async () => {
      setLoading(true)
      setError(null)
      try {
        if (provider === "ollama") {
          const res = await apiFetch("/api/v1/admin/keeper/judge/models", { signal: controller.signal })
          const body = await res.json()
          if (controller.signal.aborted) return
          if (body?.error) { setError(body.error); setModels([]); return }
          setModels(Array.isArray(body?.models) ? body.models : [])
          setNote(body?.endpoint ? `Served by ${body.endpoint}` : "")
        } else {
          const qs = new URLSearchParams({ provider: provider.toUpperCase() })
          if (workspaceId) qs.set("workspace_id", workspaceId)
          const res = await apiFetch(`/api/v1/models?${qs.toString()}`, { signal: controller.signal })
          if (!res.ok) throw new Error((await res.text()) || `HTTP ${res.status}`)
          const body = await res.json()
          if (controller.signal.aborted) return
          setModels((body?.models ?? []).map((m: { id: string }) => m.id))
          setNote(body?.source === "live"
            ? "Listed live from this workspace's credential."
            : "No usable credential for this provider — showing the known set.")
        }
      } catch (err) {
        if ((err as { name?: string })?.name === "AbortError") return
        // "No models" and "we could not ask" are different answers, and only
        // the second one tells the operator what to fix.
        setError(err instanceof Error ? err.message : "Could not list models")
        setModels([])
      } finally {
        if (!controller.signal.aborted) setLoading(false)
      }
    }
    void run()
    return () => controller.abort()
  }, [open, provider, workspaceId])

  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`${provider || "provider"} models`}
      description={note || "Models this provider can serve."}
    >
      <CommandInput placeholder="Search models…" />
      <CommandList>
        {loading && (
          <div className="flex items-center gap-2 px-4 py-6 text-xs text-muted-foreground">
            <Loader2 className="size-3.5 animate-spin" />
            Asking {provider || "the provider"}…
          </div>
        )}
        {!loading && error && <div className="px-4 py-6 text-xs text-destructive">{error}</div>}
        {!loading && !error && <CommandEmpty>No model matches.</CommandEmpty>}
        {!loading && !error && models && models.length > 0 && (
          <CommandGroup>
            {models.map((id) => (
              <CommandItem
                key={id}
                value={id}
                onSelect={() => { onOpenChange(false); if (id !== current) onPick(id) }}
              >
                <Check className={cn("size-3.5 shrink-0", id === current ? "opacity-100 text-primary" : "opacity-0")} />
                <span className="min-w-0 flex-1 truncate font-mono text-xs">{id}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}
      </CommandList>
    </CommandDialog>
  )
}
