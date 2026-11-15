"use client"

import { useCallback, useEffect, useState } from "react"
import { toast } from "@/lib/toast"
import { Check, ChevronsUpDown, Loader2 } from "lucide-react"

import {
  CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList,
} from "@/components/ui/command"
import { MODEL_META } from "@/components/features/crews/model-library-picker"
import { isKnownModel, isValidModelId } from "@/components/features/crews/create-agent/llm-models"
import type { LLMProvider } from "@/lib/entities"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { cn } from "@/lib/utils"

import { ConfigRow } from "./config-field"

// =============================================================================
// Model, chosen from what the provider actually serves.
//
// This was a free-text field, which meant switching Provider to OpenAI left
// `claude-haiku-4-5` sitting in it — a combination that cannot run, offered by
// the UI as if it could, and only failing at the next run. Typing a model name
// from memory is not a feature.
//
// GET /api/v1/models?provider=X already existed and already does the right
// thing: it lists live from the workspace's credential for that provider and
// falls back to a curated set when there is no key or the provider won't list
// (internal/api/models.go). The picker just had to ask.
//
// It opens as a centred dialog rather than a dropdown because the useful part
// of a model is the sentence next to it, not the id.
//
// The list is a suggestion, not a gate. Providers ship models faster than the
// catalog is refreshed, so the dialog also takes a custom model id typed by
// hand; the server checks only its shape (#5376). A custom id stays in place
// until the user picks something else.
// =============================================================================

interface ModelInfo {
  id: string
  display_name?: string
  provider: string
}

export interface ConfigModelProps {
  draftMode?: boolean
  label: string
  hint?: string
  workspaceId: string
  /** Uppercase provider — the list is scoped to it. */
  provider: string
  value: string
  onSave: (next: string) => Promise<void> | void
}

export function ConfigModel({ label, hint, workspaceId, provider, value, onSave, draftMode = false }: ConfigModelProps) {
  const [custom, setCustom] = useState("")
  const [open, setOpen] = useState(false)
  const [models, setModels] = useState<ModelInfo[] | null>(null)
  const [source, setSource] = useState<string>("")
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const load = useCallback(async (signal: AbortSignal) => {
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/models?provider=${encodeURIComponent(provider)}&workspace_id=${encodeURIComponent(workspaceId)}`,
        { signal },
      )
      if (!res.ok) throw await toApiError(res, `HTTP ${res.status}`)
      const body = (await res.json()) as { models?: ModelInfo[]; source?: string }
      if (signal.aborted) return
      setModels(body.models ?? [])
      setSource(body.source ?? "")
    } catch (err) {
      if ((err as { name?: string })?.name === "AbortError") return
      // Surfaced in the dialog, not swallowed: "no models" and "we could not
      // ask" are different answers and the second one is actionable.
      setError(err instanceof Error ? err.message : "Could not list models")
      setModels([])
    } finally {
      if (!signal.aborted) setLoading(false)
    }
  }, [provider, workspaceId])

  // Fetch when the dialog opens, and again if the provider changed underneath.
  useEffect(() => {
    if (!open) return
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [open, load])

  async function pick(id: string) {
    setOpen(false)
    if (id === value) return
    try {
      await onSave(id)
      if (!draftMode) toast.success(`${label} saved`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Could not save")
    }
  }

  // Before the dialog has listed anything, the bundled catalog decides; after,
  // the provider's own list does.
  const listed = models && models.length > 0
    ? models.some((m) => m.id === value)
    : isKnownModel(provider as LLMProvider, value)
  const isCustom = !!value && !listed

  // Opening the dialog on a custom id puts that id in the custom field, so a
  // one-character fix does not mean retyping the whole id.
  function openDialog() {
    setCustom(isCustom ? value : "")
    setOpen(true)
  }

  const customId = custom.trim()
  const customInvalid = customId !== "" && !isValidModelId(customId)
  function applyCustom() {
    if (customId && !customInvalid) void pick(customId)
  }

  return (
    <>
      <ConfigRow label={label} hint={hint}>
        <button
          type="button"
          onClick={openDialog}
          aria-label={`${label}: ${value || "not set"}`}
          className={cn(
            "type-row flex h-7 w-full items-center gap-2 rounded-lg border border-border bg-background px-2.5",
            "text-left font-mono text-foreground outline-none transition-[border-color,box-shadow]",
            "hover:border-foreground/25 focus:border-primary",
            "focus:shadow-[0_0_0_3px_color-mix(in_oklch,var(--primary)_20%,transparent)]",
            isCustom && "border-primary/40",
          )}
        >
          <span className="min-w-0 flex-1 truncate">{value || "choose a model"}</span>
          <ChevronsUpDown className="h-3 w-3 shrink-0 text-muted-foreground-soft" />
        </button>
      </ConfigRow>

      {isCustom && (
        <p className="type-meta border-b border-border px-3 pb-2 text-muted-foreground">
          Custom model ID — not in this catalog. It is sent to the runner exactly as typed.
        </p>
      )}

      <CommandDialog
        open={open}
        onOpenChange={setOpen}
        title={`${provider.charAt(0)}${provider.slice(1).toLowerCase()} models`}
        description={
          source === "live"
            ? "Listed live from this workspace's credential."
            : source === "curated"
              ? "No usable credential for this provider — showing the known set."
              : "Models this provider can serve."
        }
      >
        <CommandInput placeholder="Search models…" />
        <CommandList>
          {loading && (
            <div className="type-row flex items-center gap-2 px-4 py-6 text-muted-foreground">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Asking {provider.toLowerCase()}…
            </div>
          )}
          {!loading && error && (
            <div className="type-row px-4 py-6 text-destructive">{error}</div>
          )}
          {!loading && !error && <CommandEmpty>No model matches.</CommandEmpty>}
          {!loading && !error && models && models.length > 0 && (
            <CommandGroup>
              {models.map((m) => {
                const meta = MODEL_META[m.id]
                return (
                  <CommandItem key={m.id} value={`${m.id} ${m.display_name ?? ""}`} onSelect={() => void pick(m.id)}>
                    <Check className={cn("h-3.5 w-3.5 shrink-0", m.id === value ? "opacity-100 text-primary" : "opacity-0")} />
                    <span className="min-w-0 flex-1">
                      <span className="type-row block truncate font-mono text-foreground">{m.id}</span>
                      {(meta?.description || m.display_name) && (
                        <span className="type-meta block truncate text-muted-foreground">
                          {meta?.description ?? m.display_name}
                        </span>
                      )}
                    </span>
                    {meta?.badge && (
                      <span className="type-meta shrink-0 rounded border border-border px-1.5 py-0.5 text-muted-foreground">
                        {meta.badge}
                      </span>
                    )}
                  </CommandItem>
                )
              })}
            </CommandGroup>
          )}
        </CommandList>
        <div className="border-t border-border p-3">
          <div className="flex items-end gap-2">
            <label htmlFor="custom-model-id" className="type-meta flex-1 text-muted-foreground">
              Custom model ID
              <input
                id="custom-model-id"
                value={custom}
                placeholder="e.g. claude-sonnet-5-5 or gpt-6.1-sol"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={customInvalid}
                aria-describedby="custom-model-id-hint"
                onChange={(event) => setCustom(event.target.value)}
                // cmdk listens for Enter on the dialog root to select the
                // highlighted row; this field's Enter must use the typed id.
                onKeyDown={(event) => {
                  if (event.key !== "Enter") return
                  event.preventDefault()
                  event.stopPropagation()
                  applyCustom()
                }}
                className={cn(
                  "mt-1 w-full rounded-lg border bg-background p-2 font-mono text-sm text-foreground",
                  customInvalid ? "border-destructive" : "border-border",
                )}
              />
            </label>
            <button
              type="button"
              className="px-2 py-2 text-sm text-primary disabled:opacity-40"
              disabled={!customId || customInvalid}
              onClick={applyCustom}
            >
              Use model
            </button>
          </div>
          <p id="custom-model-id-hint" className={cn("type-meta mt-1", customInvalid ? "text-destructive" : "text-muted-foreground")}>
            {customInvalid
              ? "Start with a letter or digit; use only letters, digits and - _ . : / @ + [ ] with no spaces."
              : "Any model ID the runner accepts, even if it is not listed above."}
          </p>
        </div>
      </CommandDialog>
    </>
  )
}
