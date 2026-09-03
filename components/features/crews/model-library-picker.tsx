"use client"

import { useMemo, useState } from "react"
import { Check, ChevronDown, Pencil, Sparkles } from "lucide-react"
import {
  CommandDialog,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
  CommandSeparator,
} from "@/components/ui/command"
import { CLI_ADAPTERS, getProviderLabel } from "@/lib/cli-adapters"
import { MODEL_CATALOG, adapterModels, providerModels, type CatalogModel } from "@/lib/model-catalog"
import { cn } from "@/lib/utils"

/**
 * Model metadata. Co-located with the picker (rather than in
 * lib/cli-adapters) because it's purely cosmetic — the runtime only
 * cares about the model ID string. Anything not in this map renders
 * with bare ID, which is fine for genuinely-custom values.
 */
export interface ModelMeta {
  description: string
  badge?: string
  legacy?: boolean
}

// Derived from the catalog's `category` and `role`, not typed per id: the
// per-id table this replaced still described gpt-4o and gemini-2.0-flash,
// neither of which any picker offered any more, and had nothing to say about
// half the ids they did.
const CATEGORY_META: Record<string, { description: string; badge?: string }> = {
  frontier: { description: "Current flagship tier · best for complex work" },
  reasoning: { description: "Reasoning model · slower, deeper", badge: "Reasoning" },
  fast: { description: "Fast and cheap · quick replies", badge: "Fast" },
  cheap: { description: "Cheapest tier · mechanical, well-specified work", badge: "Cheap" },
  legacy: { description: "Older generation · still served", badge: "Legacy" },
  local: { description: "Runs on your own endpoint · no API key", badge: "Local" },
}

function metaFor(m: CatalogModel): ModelMeta {
  const base = CATEGORY_META[m.category ?? ""] ?? { description: "" }
  if (m.role === "default") return { description: `${base.description} · default pick`, badge: "Default" }
  if (m.role === "top") return { description: base.description, badge: "Top" }
  return { ...base, legacy: m.category === "legacy" }
}

function buildModelMeta(): Record<string, ModelMeta> {
  const out: Record<string, ModelMeta> = {}
  for (const provider of Object.keys(MODEL_CATALOG.providers)) {
    for (const m of providerModels(provider)) out[m.id] = metaFor(m)
  }
  for (const key of Object.keys(MODEL_CATALOG.adapters)) {
    for (const m of adapterModels(key)) if (!out[m.id]) out[m.id] = metaFor(m)
  }
  return out
}

export const MODEL_META: Record<string, ModelMeta> = buildModelMeta()

interface ModelEntry {
  value: string
  label: string
  provider: string
  /** Canonical adapter that owns this model (used when a fresh choice
   *  needs a default adapter — current adapter wins if it can also run
   *  the model). */
  defaultAdapter: string
  badge?: string
  description?: string
  legacy?: boolean
}

/**
 * Adapters that can run a given provider's models. Anthropic models can
 * run via Claude Code OR OpenCode, OpenAI via Codex CLI OR OpenCode,
 * Google via Gemini CLI only.
 */
const PROVIDER_ADAPTERS: Record<string, string[]> = {
  ANTHROPIC: ["CLAUDE_CODE", "OPENCODE", "CURSOR_CLI", "FACTORY_DROID"],
  OPENAI: ["CODEX_CLI", "OPENCODE", "CURSOR_CLI", "FACTORY_DROID"],
  GOOGLE: ["GEMINI_CLI", "OPENCODE", "CURSOR_CLI", "FACTORY_DROID"],
  CURSOR: ["CURSOR_CLI"],
  FACTORY: ["FACTORY_DROID"],
}

function buildModelLibrary(): ModelEntry[] {
  const seen = new Set<string>()
  const out: ModelEntry[] = []
  for (const [adapterKey, cfg] of Object.entries(CLI_ADAPTERS)) {
    for (const m of cfg.models) {
      if (seen.has(m.value)) continue
      seen.add(m.value)
      const meta = MODEL_META[m.value]
      out.push({
        value: m.value,
        label: m.label,
        provider: cfg.provider,
        defaultAdapter: adapterKey,
        badge: meta?.badge,
        description: meta?.description,
        legacy: meta?.legacy,
      })
    }
  }
  return out
}

export interface ModelLibraryPickerProps {
  /** Current cli_adapter on the agent. */
  cliAdapter: string
  /** Current llm_model on the agent. */
  llmModel: string
  /** Called when the user picks a preset model. Adapter is auto-resolved
   *  to the current adapter if it can still run the new model, otherwise
   *  to the model's default adapter. */
  onPick: (next: { llm_model: string; cli_adapter: string; llm_provider: string }) => void
  /** Called when the user clicks "Custom model name…". The parent should
   *  open its own free-text input flow. */
  onCustom: () => void
}

export function ModelLibraryPicker({
  cliAdapter,
  llmModel,
  onPick,
  onCustom,
}: ModelLibraryPickerProps) {
  const [open, setOpen] = useState(false)
  const library = useMemo(() => buildModelLibrary(), [])

  const current = useMemo(() => library.find((m) => m.value === llmModel), [library, llmModel])
  const currentAdapterCfg = CLI_ADAPTERS[cliAdapter]
  const TriggerIcon =
    (current ? CLI_ADAPTERS[current.defaultAdapter]?.icon : currentAdapterCfg?.icon) ?? Sparkles

  // Group by provider for the cmdk list
  const grouped = useMemo(() => {
    const acc: Record<string, ModelEntry[]> = {}
    for (const m of library) {
      ;(acc[m.provider] ??= []).push(m)
    }
    // Stable provider order: Anthropic / OpenAI / Google / others alpha
    const order = ["ANTHROPIC", "OPENAI", "GOOGLE"]
    return Object.entries(acc).sort(
      ([a], [b]) => {
        const ai = order.indexOf(a); const bi = order.indexOf(b)
        if (ai !== -1 && bi !== -1) return ai - bi
        if (ai !== -1) return -1
        if (bi !== -1) return 1
        return a.localeCompare(b)
      },
    )
  }, [library])

  const handleSelect = (entry: ModelEntry) => {
    // Keep current adapter when it can still run the new model's provider.
    const compatible = PROVIDER_ADAPTERS[entry.provider] ?? [entry.defaultAdapter]
    const nextAdapter = compatible.includes(cliAdapter) ? cliAdapter : entry.defaultAdapter
    onPick({
      llm_model: entry.value,
      cli_adapter: nextAdapter,
      llm_provider: entry.provider,
    })
    setOpen(false)
  }

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className={cn(
          "w-full flex items-center gap-3 rounded-lg border bg-card hover:bg-white/[0.03]",
          "px-3 py-2.5 text-left transition-colors",
        )}
        aria-haspopup="dialog"
      >
        <TriggerIcon className="h-5 w-5 shrink-0 text-foreground" />
        <div className="flex-1 min-w-0">
          {current ? (
            <>
              <div className="flex items-center gap-2">
                <span className={cn("text-sm font-medium truncate", current.legacy && "text-muted-foreground")}>
                  {current.label}
                </span>
                {current.badge && <BadgeChip badge={current.badge} legacy={current.legacy} />}
              </div>
              <div className="text-[11px] text-muted-foreground truncate flex items-center gap-1.5">
                <span className="font-mono">{current.value}</span>
                {current.description && (
                  <>
                    <span className="opacity-50">·</span>
                    <span>{current.description}</span>
                  </>
                )}
              </div>
            </>
          ) : llmModel ? (
            <>
              <div className="flex items-center gap-2">
                <span className="font-mono text-sm">{llmModel}</span>
                <BadgeChip badge="Custom" legacy={false} />
              </div>
              <div className="text-[11px] text-muted-foreground">
                Not in preset list — pick from library or keep typing.
              </div>
            </>
          ) : (
            <span className="text-sm text-muted-foreground">Select a model…</span>
          )}
        </div>
        <ChevronDown className="h-4 w-4 text-muted-foreground shrink-0" />
      </button>

      <CommandDialog
        open={open}
        onOpenChange={setOpen}
        title="Model library"
        description="Search and pick a model"
      >
        <CommandInput placeholder="Search models — e.g. 'opus', 'reasoning', 'fast'…" />
        <CommandList className="max-h-[420px]">
          <CommandEmpty>No models match.</CommandEmpty>
          {grouped.map(([provider, models], gi) => (
            <div key={provider}>
              {gi > 0 && <CommandSeparator />}
              <CommandGroup heading={getProviderLabel(provider)}>
                {models.map((m) => {
                  const ItemIcon = CLI_ADAPTERS[m.defaultAdapter]?.icon ?? Sparkles
                  const isActive = m.value === llmModel
                  return (
                    <CommandItem
                      key={m.value}
                      value={`${m.label} ${m.value} ${m.badge ?? ""} ${m.description ?? ""}`}
                      onSelect={() => handleSelect(m)}
                      className="items-start gap-3 py-2"
                    >
                      <ItemIcon className={cn("h-4 w-4 shrink-0 mt-0.5", isActive ? "text-primary" : "text-muted-foreground")} />
                      <div className="flex flex-col gap-0.5 flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <span className={cn("text-sm", m.legacy && "text-muted-foreground")}>
                            {m.label}
                          </span>
                          {m.badge && <BadgeChip badge={m.badge} legacy={m.legacy} />}
                        </div>
                        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
                          <span className="font-mono">{m.value}</span>
                          {m.description && (
                            <>
                              <span className="opacity-50">·</span>
                              <span className="truncate">{m.description}</span>
                            </>
                          )}
                        </div>
                      </div>
                      {isActive && <Check className="h-4 w-4 text-primary shrink-0 mt-0.5" />}
                    </CommandItem>
                  )
                })}
              </CommandGroup>
            </div>
          ))}
          <CommandSeparator />
          <CommandGroup heading="Other">
            <CommandItem
              value="custom model name"
              onSelect={() => {
                onCustom()
                setOpen(false)
              }}
              className="gap-3"
            >
              <Pencil className="h-4 w-4 text-muted-foreground" />
              <span className="italic text-muted-foreground">Custom model name…</span>
            </CommandItem>
          </CommandGroup>
        </CommandList>
      </CommandDialog>
    </>
  )
}

function BadgeChip({ badge, legacy }: { badge: string; legacy?: boolean }) {
  return (
    <span
      className={cn(
        "rounded px-1.5 py-px text-[10px] font-medium shrink-0",
        legacy
          ? "bg-muted/40 text-muted-foreground"
          : badge === "Latest" || badge === "Default"
            ? "bg-blue-500/15 text-blue-300"
            : badge === "Reasoning"
              ? "bg-purple/15 text-purple"
              : badge === "Multimodal"
                ? "bg-purple/15 text-purple"
                : badge === "Long ctx"
                  ? "bg-success/15 text-success"
                  : badge === "Custom"
                    ? "bg-warn/15 text-warn"
                    : "bg-muted-foreground/15 text-muted-foreground",
      )}
    >
      {badge}
    </span>
  )
}

/**
 * Returns the list of CLI adapter keys that can run the given model's
 * provider. Used by agent-canvas to render the secondary "Adapter"
 * select only when there's a real choice.
 */
export function getCompatibleAdapters(provider: string): string[] {
  return PROVIDER_ADAPTERS[provider] ?? []
}
