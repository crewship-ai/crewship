"use client"

import * as React from "react"
import { Search } from "lucide-react"
import {
  AnthropicIcon, OpenAIIcon, GeminiIcon, CursorIcon, FactoryIcon,
  GitHubIcon, GitLabIcon, VercelIcon, AWSIcon, CustomCLIIcon,
} from "@/components/icons/provider-icons"
import { Lock } from "lucide-react"
import { cn } from "@/lib/utils"
import type { CredentialProvider, WizardState } from "./types"
import { PROVIDER_TILES, defaultEnvVarName } from "./types"

const ICONS: Record<CredentialProvider, React.ComponentType<{ className?: string }>> = {
  ANTHROPIC: AnthropicIcon,
  OPENAI: OpenAIIcon,
  GOOGLE: GeminiIcon,
  CURSOR: CursorIcon,
  FACTORY: FactoryIcon,
  GITHUB: GitHubIcon,
  GITLAB: GitLabIcon,
  VERCEL: VercelIcon,
  AWS: AWSIcon,
  CUSTOM_CLI: CustomCLIIcon,
  NONE: Lock,
}

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

export function StepProvider({ state, setState }: Props) {
  const [query, setQuery] = React.useState("")
  const filtered = React.useMemo(() => {
    if (!query.trim()) return PROVIDER_TILES
    const q = query.toLowerCase()
    return PROVIDER_TILES.filter((t) => t.label.toLowerCase().includes(q))
  }, [query])

  return (
    <div className="space-y-3">
      <div className="relative">
        <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
        <input
          autoFocus
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search providers..."
          className="w-full pl-8 pr-2 py-2 text-sm bg-zinc-950 border border-white/15 rounded-md outline-none focus:border-blue-400"
        />
      </div>

      <div className="grid grid-cols-3 gap-2">
        {filtered.map((tile) => {
          const Icon = ICONS[tile.id]
          const isSelected = state.provider === tile.id
          return (
            <button
              key={tile.id}
              type="button"
              onClick={() => {
                const envName = defaultEnvVarName(tile.id, tile.defaultMethod)
                setState({
                  provider: tile.id,
                  authMethod: tile.defaultMethod,
                  type: tile.defaultType,
                  name: envName,
                })
              }}
              className={cn(
                "flex flex-col items-center gap-2 rounded-md border bg-zinc-950 p-3 transition-all",
                isSelected
                  ? "border-blue-400 ring-2 ring-blue-400/20"
                  : "border-white/10 hover:border-white/25 hover:bg-white/[0.02]",
              )}
            >
              <Icon className="h-8 w-8" />
              <span className="text-xs font-medium">{tile.label}</span>
            </button>
          )
        })}
      </div>

      <div className="rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2.5 text-xs text-foreground/80 flex gap-2.5 items-start">
        <span className="shrink-0 text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded bg-blue-500/90 text-blue-950 mt-0.5">
          TIP
        </span>
        <span className="leading-relaxed">
          Pick the provider your agent will call. Missing yours? Use <strong>Custom CLI</strong> for
          arbitrary CLI tools, or <strong>Generic Secret</strong> for any opaque value.
        </span>
      </div>
    </div>
  )
}
