"use client"

import type { KeyboardEvent } from "react"
import { SiOllama } from "react-icons/si"
import { PROVIDER_ICONS } from "@/components/icons/provider-icons"
import { Cpu, Timer } from "lucide-react"
import { CLI_ADAPTERS } from "@/lib/cli-adapters"
import { CreateSurfaceField, CreateSurfaceSection } from "@/components/layout/create-surface"
import { ConfigModel } from "../canvas/config-model"
import { cn } from "@/lib/utils"
import type { AgentDraft } from "./types"
import { defaultModelForProvider, isKnownModel } from "./llm-models"

const PROVIDERS = { ANTHROPIC: "Anthropic", OPENAI: "OpenAI", GOOGLE: "Google", CURSOR: "Cursor", FACTORY: "Factory", OLLAMA: "Ollama" } as const
const DEFAULT_RUNNER = { ANTHROPIC: "CLAUDE_CODE", OPENAI: "CODEX_CLI", GOOGLE: "GEMINI_CLI", CURSOR: "CURSOR_CLI", FACTORY: "FACTORY_DROID", OLLAMA: "OPENCODE" } as const

export function AgentModelSettings({ draft, setDraft, workspaceId }: { draft: AgentDraft; setDraft: (draft: AgentDraft) => void; workspaceId: string }) {
  return <CreateSurfaceSection title="Model and execution" icon={Cpu} accent="teal" hint="Choose the AI model and the program that runs it.">
    <CreateSurfaceField label="Model provider">
      <div role="radiogroup" aria-label="Model provider" onKeyDown={moveRadio} className="grid grid-cols-2 gap-2 lg:grid-cols-3">{Object.entries(PROVIDERS).map(([key, label]) => {
        const provider = key as AgentDraft["llmProvider"]
        const Icon = key === "OLLAMA" ? SiOllama : PROVIDER_ICONS[key]
        return <button key={key} type="button" role="radio" aria-checked={draft.llmProvider === key} tabIndex={draft.llmProvider === key ? 0 : -1} onClick={() => setDraft({ ...draft, llmProvider: provider, llmModel: isKnownModel(provider, draft.llmModel) ? draft.llmModel : defaultModelForProvider(provider), cliAdapter: draft.cliAdapter === "OPENCODE" ? "OPENCODE" : DEFAULT_RUNNER[provider] })} className={cn("flex items-center gap-2 rounded-lg border p-3 text-sm focus-visible:ring-2 focus-visible:ring-primary", draft.llmProvider === key ? "border-primary bg-primary/10" : "border-border hover:bg-muted")}><Icon className="h-5 w-5 shrink-0" aria-hidden="true" />{label}</button>
      })}</div>
    </CreateSurfaceField>
    <ConfigModel draftMode label="Model" workspaceId={workspaceId} provider={draft.llmProvider} value={draft.llmModel} onSave={model => setDraft({ ...draft, llmModel: model })} />
    <CreateSurfaceField label="Runs with" hint="The application launched inside the crew container. Choosing a runner selects its matching provider; OpenCode can use multiple providers.">
      <div role="radiogroup" aria-label="Agent runner" onKeyDown={moveRadio} className="grid grid-cols-1 gap-2 md:grid-cols-2">{Object.entries(CLI_ADAPTERS).map(([key, adapter]) => {
        const Icon = adapter.icon
        return <button key={key} type="button" role="radio" aria-checked={draft.cliAdapter === key} tabIndex={draft.cliAdapter === key ? 0 : -1} onClick={() => {
          const provider = (key === "OPENCODE" ? draft.llmProvider : adapter.provider) as AgentDraft["llmProvider"]
          setDraft({ ...draft, cliAdapter: key as AgentDraft["cliAdapter"], llmProvider: provider, llmModel: isKnownModel(provider, draft.llmModel) ? draft.llmModel : defaultModelForProvider(provider) })
        }} className={cn("flex items-start gap-3 rounded-lg border p-3 text-left focus-visible:ring-2 focus-visible:ring-primary", draft.cliAdapter === key ? "border-primary bg-primary/10" : "border-border hover:bg-muted")}><Icon className="mt-0.5 h-5 w-5 shrink-0" aria-hidden="true" /><span><span className="block text-sm">{adapter.label}</span><span className="mt-1 block text-xs text-muted-foreground">{adapter.description}</span></span></button>
      })}</div>
    </CreateSurfaceField>
    <CreateSurfaceField label="Maximum run duration" htmlFor="agent-timeout" hint="Minutes per run. A run that exceeds this limit stops and is marked as timed out.">
      <div className="flex items-center gap-2"><Timer className="h-4 w-4 text-muted-foreground" aria-hidden="true" /><input id="agent-timeout" type="number" min="1" max="120" step="any" value={draft.timeoutSeconds / 60} onChange={event => { const value = Number(event.target.value); if (Number.isFinite(value)) setDraft({ ...draft, timeoutSeconds: Math.round(Math.min(120, Math.max(1, value)) * 60) }) }} className="w-28 rounded-md border border-border bg-background px-3 py-2 text-sm" /><span className="text-sm text-muted-foreground">minutes</span></div>
    </CreateSurfaceField>
    <p className="text-xs text-muted-foreground">Provider credentials and billing access are managed separately. Changing the model does not connect an account.</p>
  </CreateSurfaceSection>
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
