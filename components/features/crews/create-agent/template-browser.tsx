"use client"

import { useMemo, useState } from "react"
import { Search } from "lucide-react"
import {
  BUILTIN_PERSONAS,
  filterPersonas,
  categoryCounts,
} from "@/lib/agent-personas"
import type { AgentPersona, PersonaCategory } from "@/lib/agent-personas"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { cn } from "@/lib/utils"
import type { PersonaSource } from "./types"

const CATEGORY_LABELS: Record<PersonaCategory, string> = {
  engineering: "Engineering",
  research: "Research",
  quality: "Quality / Testing",
  writing: "Writing",
  devops: "DevOps",
  coordinator: "Coordinator",
  custom: "Custom",
}

interface TemplateBrowserProps {
  selected: AgentPersona | null
  onSelect: (persona: AgentPersona) => void
}

/** Browser-only — list with tabs / search / category filter. The preview
 *  pane was moved out (rendered separately by step-persona below the
 *  browser) so the prompt + edit affordance is more discoverable on
 *  narrow viewports. */
export function TemplateBrowser({ selected, onSelect }: TemplateBrowserProps) {
  const [source, setSource] = useState<PersonaSource>("builtin")
  const [search, setSearch] = useState("")
  const [category, setCategory] = useState<PersonaCategory | "all">("all")

  const sourcePersonas: AgentPersona[] = source === "builtin" ? BUILTIN_PERSONAS : []
  const filtered = useMemo(
    () => filterPersonas(sourcePersonas, { search, category }),
    [sourcePersonas, search, category],
  )
  const counts = useMemo(() => categoryCounts(sourcePersonas), [sourcePersonas])
  const sourceCounts: Record<PersonaSource, number | "—"> = {
    builtin: BUILTIN_PERSONAS.length,
    mine: "—",
    workspace: "—",
    marketplace: "—",
  }

  return (
    <div className="border border-white/[0.08] rounded-xl overflow-hidden bg-card-2 flex flex-col min-h-[320px] max-h-[420px]">
      {/* Search bar */}
      <div className="px-3 py-2.5 border-b border-white/[0.08]">
        <div className="relative flex-1">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground/60" />
          <input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder='Search personas… (e.g. "data analyst", "test", "research")'
            aria-label="Search personas by name, role, or category"
            className="w-full pl-8 pr-3 py-1.5 bg-zinc-950 border border-white/[0.15] rounded-md text-[12.5px] outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-400/15"
          />
        </div>
      </div>

      {/* Source tabs */}
      <div className="flex px-3 border-b border-white/[0.08]">
        {(["builtin", "mine", "workspace", "marketplace"] as PersonaSource[]).map((src) => {
          const isActive = source === src
          const isDisabled = src !== "builtin"
          return (
            <button
              key={src}
              type="button"
              disabled={isDisabled}
              onClick={() => !isDisabled && setSource(src)}
              title={isDisabled ? "Coming soon" : undefined}
              className={cn(
                "px-3 py-2 text-[12px] flex items-center gap-1.5 border-b-2 -mb-px transition-colors",
                isActive ? "border-blue-400 text-foreground" : "border-transparent text-muted-foreground",
                !isDisabled && "hover:text-foreground/80 cursor-pointer",
                isDisabled && "opacity-50 cursor-not-allowed",
              )}
            >
              <span className="capitalize">{sourceLabel(src)}</span>
              <span
                className={cn(
                  "text-[10px] px-1.5 rounded-full font-semibold min-w-[18px] text-center",
                  isActive ? "bg-blue-500/15 text-blue-300" : "bg-white/[0.05] text-muted-foreground",
                )}
              >
                {sourceCounts[src]}
              </span>
            </button>
          )
        })}
      </div>

      {/* Category chips */}
      <div className="flex gap-1.5 px-3 py-2 border-b border-white/[0.08] overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        <CategoryChip
          active={category === "all"}
          onClick={() => setCategory("all")}
          label="All"
          count={counts.all}
        />
        {(Object.keys(CATEGORY_LABELS) as PersonaCategory[])
          .filter((c) => (counts[c] ?? 0) > 0)
          .map((c) => (
            <CategoryChip
              key={c}
              active={category === c}
              onClick={() => setCategory(c)}
              label={CATEGORY_LABELS[c]}
              count={counts[c] ?? 0}
            />
          ))}
      </div>

      {/* List */}
      <div className="flex-1 min-h-0 overflow-y-auto p-1.5">
        {source !== "builtin" ? (
          <ComingSoon source={source} />
        ) : filtered.length === 0 ? (
          <EmptyState search={search} />
        ) : (
          <div className="grid grid-cols-2 gap-1.5">
            {filtered.map((p) => (
              <PersonaRow
                key={p.id}
                persona={p}
                active={selected?.id === p.id}
                onClick={() => onSelect(p)}
              />
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function sourceLabel(s: PersonaSource): string {
  if (s === "builtin") return "Built-in"
  if (s === "mine") return "Mine"
  if (s === "workspace") return "Workspace"
  return "Marketplace"
}

function CategoryChip({
  active,
  onClick,
  label,
  count,
}: {
  active: boolean
  onClick: () => void
  label: string
  count: number
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "px-2.5 py-1 rounded-full text-[11.5px] border whitespace-nowrap flex items-center gap-1.5 transition-colors",
        active
          ? "bg-blue-500/15 border-blue-400/45 text-blue-300"
          : "bg-white/[0.03] border-white/[0.08] text-foreground/80 hover:border-white/[0.15]",
      )}
    >
      {label}
      <span className={cn("text-[10px]", active ? "opacity-80" : "text-muted-foreground/60")}>{count}</span>
    </button>
  )
}

function PersonaRow({
  persona,
  active,
  onClick,
}: {
  persona: AgentPersona
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "p-2.5 rounded-lg border text-left transition-all flex gap-2.5 items-start",
        active
          ? "border-blue-400/45 bg-blue-500/10"
          : "border-white/[0.08] bg-card hover:border-white/[0.15] hover:bg-white/[0.03]",
      )}
    >
      <span className="w-9 h-9 rounded-lg overflow-hidden border border-white/[0.08] bg-zinc-900 shrink-0">
        <img
          src={getAgentAvatarUrl(persona.suggestedSlug, persona.avatarStyle)}
          alt=""
          className="w-full h-full"
        />
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[12.5px] font-semibold truncate flex items-center gap-1.5">
          {persona.name}
          <span className="text-[10px] font-normal text-muted-foreground">·</span>
          <span className="text-[10px] font-normal text-muted-foreground truncate">{persona.roleTitle}</span>
        </div>
        <div className="text-[11px] text-muted-foreground line-clamp-2 mt-0.5">{persona.blurb}</div>
        <div className="flex gap-1 flex-wrap mt-1.5">
          <Pill>{persona.llmModel.replace("claude-", "").replace("-4-5", "").replace("-4-6", "")}</Pill>
          <Pill>{persona.toolProfile.toLowerCase()}</Pill>
          {persona.agentRole === "LEAD" && <Pill>lead</Pill>}
        </div>
      </div>
    </button>
  )
}

function Pill({ children }: { children: React.ReactNode }) {
  return (
    <span className="text-[9px] font-mono px-1.5 py-0.5 rounded bg-white/[0.04] text-muted-foreground">
      {children}
    </span>
  )
}

function EmptyState({ search }: { search: string }) {
  return (
    <div className="text-center py-10 px-4">
      <p className="text-sm text-muted-foreground mb-1">No personas match</p>
      <p className="text-[11px] text-muted-foreground/60">
        {search ? (
          <>
            Try a different search term, or click <span className="text-foreground/80">All</span> to see every persona.
          </>
        ) : (
          <>This category is empty in the current source.</>
        )}
      </p>
    </div>
  )
}

function ComingSoon({ source }: { source: PersonaSource }) {
  const labels: Record<PersonaSource, { title: string; desc: string }> = {
    builtin: { title: "", desc: "" },
    mine: {
      title: "My templates",
      desc: "Personas you save from the agent canvas will show up here. Coming soon.",
    },
    workspace: {
      title: "Workspace templates",
      desc: "Personas shared across this workspace by your team. Coming soon.",
    },
    marketplace: {
      title: "Marketplace",
      desc: "Browse and import personas from the community. Coming soon.",
    },
  }
  const l = labels[source]
  return (
    <div className="text-center py-10 px-4">
      <p className="text-sm text-foreground/80 mb-1">{l.title}</p>
      <p className="text-[11px] text-muted-foreground/70 max-w-[260px] mx-auto">{l.desc}</p>
    </div>
  )
}
