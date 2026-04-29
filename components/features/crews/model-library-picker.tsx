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
import { cn } from "@/lib/utils"

/**
 * Model metadata. Co-located with the picker (rather than in
 * lib/cli-adapters) because it's purely cosmetic — the runtime only
 * cares about the model ID string. Anything not in this map renders
 * with bare ID, which is fine for genuinely-custom values.
 */
export const MODEL_META: Record<string, { description: string; badge?: string; legacy?: boolean }> = {
  // Anthropic — current
  "claude-opus-4-7":            { description: "Latest · most capable · 1M context", badge: "Latest" },
  "claude-sonnet-4-6":          { description: "Balanced speed and capability · default pick", badge: "Default" },
  "claude-haiku-4-5-20251001":  { description: "Fast and cheap · quick replies", badge: "Fast" },
  // Anthropic — legacy
  "claude-opus-4-20250514":     { description: "Older Opus 4 — superseded by 4.7", badge: "Legacy", legacy: true },
  "claude-sonnet-4-20250514":   { description: "Older Sonnet 4 — superseded by 4.6", badge: "Legacy", legacy: true },
  "claude-3-5-sonnet-20241022": { description: "Pre-4.x flagship", badge: "Legacy", legacy: true },
  "claude-3-5-haiku-20241022":  { description: "Pre-4.x fast tier", badge: "Legacy", legacy: true },
  // OpenAI
  o3:           { description: "Frontier reasoning model", badge: "Reasoning" },
  "o3-mini":    { description: "Smaller reasoning model", badge: "Reasoning" },
  "o4-mini":    { description: "Newest small reasoning model", badge: "Fast" },
  "gpt-4o":     { description: "Multimodal flagship", badge: "Multimodal" },
  "gpt-4o-mini":{ description: "Smaller multimodal · cheap", badge: "Fast" },
  // Google
  "gemini-2.5-pro":   { description: "Google flagship · 1M-token context", badge: "Long ctx" },
  "gemini-2.5-flash": { description: "Faster, cheaper Gemini", badge: "Fast" },
  "gemini-2.0-flash": { description: "Older Flash · still supported", badge: "Legacy", legacy: true },
}

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
  ANTHROPIC: ["CLAUDE_CODE", "OPENCODE"],
  OPENAI: ["CODEX_CLI", "OPENCODE"],
  GOOGLE: ["GEMINI_CLI"],
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
          ? "bg-zinc-700/40 text-zinc-400"
          : badge === "Latest" || badge === "Default"
            ? "bg-blue-500/15 text-blue-300"
            : badge === "Reasoning"
              ? "bg-violet-500/15 text-violet-300"
              : badge === "Multimodal"
                ? "bg-fuchsia-500/15 text-fuchsia-300"
                : badge === "Long ctx"
                  ? "bg-emerald-500/15 text-emerald-300"
                  : badge === "Custom"
                    ? "bg-amber-500/15 text-amber-300"
                    : "bg-zinc-500/15 text-zinc-300",
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
