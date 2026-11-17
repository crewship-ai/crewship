"use client"

import { useCallback, useRef, type KeyboardEvent } from "react"
import { SiOllama } from "react-icons/si"
import { PROVIDER_ICONS } from "@/components/icons/provider-icons"
import { Cpu, Lock, Timer } from "lucide-react"
import { CLI_ADAPTERS } from "@/lib/cli-adapters"
import { CreateSurfaceField, CreateSurfaceSection } from "@/components/layout/create-surface"
import { ConfigModel } from "../canvas/config-model"
import { cn } from "@/lib/utils"
import type { AgentDraft } from "./types"
import { modelAfterSwitch } from "./llm-models"

import { PROVIDERS, DEFAULT_RUNNER, PROVIDER_RUNNERS, runnerAfterSwitch } from "./provider-options"
import { BedrockAccountModel } from "./bedrock-account-model"
import type { LoginCredential } from "@/lib/credentials/provider-logins"

// Shared by create/edit and the crew-template provider picker. Monochrome
// marks inherit the theme foreground; selection does not recolor brand glyphs.
const BRAND_ICON_COLOR: Record<string, string> = {
  ANTHROPIC: "#D97757", CLAUDE_CODE: "#D97757",
  GOOGLE: "#4285F4", GEMINI_CLI: "#4285F4",
  FACTORY: "#EF6F2E", FACTORY_DROID: "#EF6F2E",
}

export function AgentModelSettings({ draft, setDraft, workspaceId, providerConfirmed = true, onProviderConfirmed, seat = null, boundSeatId = null, onSeat }: { draft: AgentDraft; setDraft: (draft: AgentDraft) => void; workspaceId: string; providerConfirmed?: boolean; onProviderConfirmed?: () => void; seat?: LoginCredential | null; boundSeatId?: string | null; onSeat?: (seat: LoginCredential | null) => void }) {
  const runners = PROVIDER_RUNNERS[draft.llmProvider]
  const bedrock = draft.llmProvider === "AWS_BEDROCK"
  // A Bedrock account chosen below is the draft Pays with; read through a ref
  // so the callback handed to the account picker stays the same between renders.
  const latest = useRef(draft)
  latest.current = draft
  const pickBedrock = useCallback((account: LoginCredential | null, model: string) => {
    onSeat?.(account)
    if (latest.current.llmModel !== model || latest.current.cliAdapter !== "OPENCODE") setDraft({ ...latest.current, llmModel: model, cliAdapter: "OPENCODE" })
  }, [onSeat, setDraft])
  return <CreateSurfaceSection title="Model and execution" icon={Cpu} accent="teal" hint="Choose the AI model and the program that runs it.">
    <CreateSurfaceField label="Model provider">
      <ProviderPicker value={providerConfirmed ? draft.llmProvider : null} onChange={provider => {
        setDraft({ ...draft, llmProvider: provider, llmModel: modelAfterSwitch(draft.llmProvider, provider, draft.llmModel), cliAdapter: providerConfirmed ? runnerAfterSwitch(provider, draft.cliAdapter) : DEFAULT_RUNNER[provider] })
        // Another provider's account cannot pay for this one's models.
        if (seat && (seat.login?.provider ?? seat.provider) !== provider) onSeat?.(null)
        onProviderConfirmed?.()
      }} />
    </CreateSurfaceField>
    {providerConfirmed && <>{bedrock
      ? <BedrockAccountModel workspaceId={workspaceId} model={draft.llmModel} seat={seat} boundId={boundSeatId} onChange={pickBedrock} />
      : <ConfigModel key={draft.llmProvider} draftMode label="Model" workspaceId={workspaceId} provider={draft.llmProvider} value={draft.llmModel} onSave={model => setDraft({ ...draft, llmModel: model })} />}
    {runners.length === 1
      ? <CreateSurfaceField label="Runs with" hint={`${PROVIDERS[draft.llmProvider]} runs only on ${CLI_ADAPTERS[runners[0]]?.label ?? runners[0]}. It is installed automatically when the crew environment is prepared.`}>
          <FixedRunner runner={runners[0]} provider={PROVIDERS[draft.llmProvider]} />
        </CreateSurfaceField>
      : <CreateSurfaceField label="Runs with" hint={`The application launched inside the crew container. ${PROVIDERS[draft.llmProvider]} models run on its own CLI or on OpenCode.`}>
      <div role="radiogroup" aria-label="Agent runner" onKeyDown={moveRadio} className="grid grid-cols-1 gap-2 md:grid-cols-2">{runners.map((key) => {
        const adapter = CLI_ADAPTERS[key]
        const Icon = adapter.icon
        return <button key={key} type="button" role="radio" aria-checked={draft.cliAdapter === key} tabIndex={draft.cliAdapter === key ? 0 : -1} onClick={() => {
          setDraft({ ...draft, cliAdapter: key })
        }} className={cn("flex items-start gap-3 rounded-lg border p-3 text-left focus-visible:ring-2 focus-visible:ring-primary", draft.cliAdapter === key ? "border-primary bg-primary/10" : "border-border hover:bg-muted")}><Icon className="mt-0.5 h-5 w-5 shrink-0 text-foreground" style={{ color: BRAND_ICON_COLOR[key] }} aria-hidden="true" /><span><span className="block text-sm">{adapter.label}</span><span className="mt-1 block text-xs text-muted-foreground">{adapter.description}</span></span></button>
      })}</div>
    </CreateSurfaceField>}
    </>}
    {!providerConfirmed && <p className="text-sm text-muted-foreground">Choose a provider to see its model and runner. The runner is installed automatically when the crew environment is prepared.</p>}
    <CreateSurfaceField label="Maximum run duration" htmlFor="agent-timeout" hint="Minutes per run. A run that exceeds this limit stops and is marked as timed out.">
      <div className="flex items-center gap-2"><Timer className="h-4 w-4 text-muted-foreground" aria-hidden="true" /><input id="agent-timeout" type="number" min="1" max="120" step="any" value={draft.timeoutSeconds / 60} onChange={event => { const value = Number(event.target.value); if (Number.isFinite(value)) setDraft({ ...draft, timeoutSeconds: Math.round(Math.min(120, Math.max(1, value)) * 60) }) }} className="w-28 rounded-md border border-border bg-background px-3 py-2 text-sm" /><span className="text-sm text-muted-foreground">minutes</span></div>
    </CreateSurfaceField>
    {!bedrock && <p className="text-xs text-muted-foreground">Provider credentials and billing access are managed separately. Changing the model does not connect an account.</p>}
  </CreateSurfaceSection>
}

/** The one runner a provider has, shown as set rather than as a choice. */
function FixedRunner({ runner, provider }: { runner: string; provider: string }) {
  const adapter = CLI_ADAPTERS[runner]
  const Icon = adapter?.icon
  return <div className="flex min-h-11 items-center gap-3 rounded-lg border border-border bg-muted/40 px-3 py-2 text-sm">
    {Icon && <Icon className="h-5 w-5 shrink-0 text-foreground" aria-hidden="true" />}
    <span className="flex-1">{adapter?.label ?? runner}</span>
    <span className="inline-flex items-center gap-1 text-xs text-muted-foreground"><Lock className="h-3 w-3" aria-hidden="true" />set by {provider}</span>
  </div>
}

export function ProviderPicker({ value, onChange }: { value: AgentDraft["llmProvider"] | null; onChange: (provider: AgentDraft["llmProvider"]) => void }) {
  return <div role="radiogroup" aria-label="Model provider" onKeyDown={moveRadio} className="grid grid-cols-2 gap-2 lg:grid-cols-3">{Object.entries(PROVIDERS).map(([key, label], index) => {
    const provider = key as AgentDraft["llmProvider"]
    const Icon = key === "OLLAMA" ? SiOllama : PROVIDER_ICONS[key]
    return <button key={key} type="button" role="radio" aria-checked={value === key} tabIndex={value === key || (!value && index === 0) ? 0 : -1} onClick={() => onChange(provider)} className={cn("flex items-center gap-2 rounded-lg border p-3 text-sm focus-visible:ring-2 focus-visible:ring-primary", value === key ? "border-primary bg-primary/10" : "border-border hover:bg-muted")}><Icon className="h-5 w-5 shrink-0 text-foreground" style={{ color: BRAND_ICON_COLOR[key] }} aria-hidden="true" />{label}</button>
  })}</div>
}

function moveRadio(event: KeyboardEvent<HTMLDivElement>) {
  if (!["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return
  const radios = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="radio"]'))
  const index = radios.indexOf(event.target as HTMLButtonElement)
  if (index < 0) return
  event.preventDefault()
  const next = event.key === "Home" ? 0 : event.key === "End" ? radios.length - 1 : (index + (["ArrowLeft", "ArrowUp"].includes(event.key) ? -1 : 1) + radios.length) % radios.length
  radios[next]?.focus()
  radios[next]?.click()
}
