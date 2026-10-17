"use client"

import { useState } from "react"
import { Bot, Layers, Plus, Sparkles, Upload, X } from "lucide-react"

import { CrewIcon } from "@/components/ui/crew-icon"
import { Button } from "@/components/ui/button"
import { CreateSurfaceField, CreateSurfaceLoading, CreateSurfaceNotice } from "@/components/layout/create-surface"
import { GRADIENT_PALETTES, getCrewIconDef } from "@/lib/entities"
import { agentRoleLabel } from "@/lib/agent-role"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { cn } from "@/lib/utils"

import { asCrewColor, normalizeSlug, type CrewStart, type WizardState } from "./types"
import type { AISuggestResponse } from "./api"

/** The four ways to start a crew (New crew, step 1). */
export const CREW_STARTS: { id: CrewStart; icon: typeof Plus; title: string; sub: string; team: string }[] = [
  { id: "suggest", icon: Sparkles, title: "Suggest a team", sub: "From your description, with AI; you review it", team: "Team" },
  { id: "template", icon: Layers, title: "From a template", sub: "Tested teams with their tools", team: "Template" },
  { id: "yaml", icon: Upload, title: "Import crewship.yaml", sub: "The same format as crewship export", team: "File" },
  { id: "empty", icon: Plus, title: "Empty crew", sub: "You add the agents", team: "Agents" },
]

export function StartChoice({ value, onChange }: { value: CrewStart; onChange: (v: CrewStart) => void }) {
  return (
    <CreateSurfaceField label="Start with">
      <div role="radiogroup" aria-label="Start with" className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {CREW_STARTS.map((s) => {
          const on = s.id === value
          const Icon = s.icon
          return (
            <button
              key={s.id}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => onChange(s.id)}
              className={cn(
                "flex min-w-0 flex-col items-start gap-1 rounded-lg border p-2.5 text-left transition-colors coarse:min-h-[44px]",
                on ? "border-primary/40 bg-primary/[0.08]" : "border-hairline bg-foreground/[0.02] hover:bg-foreground/[0.05]",
              )}
            >
              <Icon className={cn("size-4", on ? "text-primary-hover" : "text-muted-foreground")} aria-hidden />
              <b className="text-label font-medium text-foreground">{s.title}</b>
              <span className="text-micro leading-snug text-muted-foreground">{s.sub}</span>
            </button>
          )
        })}
      </div>
    </CreateSurfaceField>
  )
}

/** The twenty icons New crew offers inline; the full catalogue is one click away. */
export const QUICK_CREW_ICONS = [
  "search", "code", "server", "clock", "globe", "shield", "mail", "star", "box", "layers",
  "terminal", "bot", "chart", "database", "rocket", "brain", "users", "wrench", "zap", "briefcase",
]

/**
 * Icon and colour, inline on step 1: a preview, the colour row and a grid of
 * icons. Stored on the crew (icon, color) and shown wherever it is listed.
 */
export function InlineIconPicker({ state, setState, onMore }: {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
  onMore: () => void
}) {
  return (
    <CreateSurfaceField label="Icon and colour">
      <div className="space-y-2.5 rounded-lg border border-hairline p-3" data-testid="crew-icon-picker">
        <div className="flex items-center gap-3">
          <CrewIcon icon={state.icon} color={state.color} size="md" />
          <span className="text-label text-muted-foreground">Shown on the crew’s agents, in lists and in Activity.</span>
        </div>
        <div role="radiogroup" aria-label="Colour" className="flex flex-wrap gap-1.5">
          {GRADIENT_PALETTES.map((g) => (
            <button
              key={g.id}
              type="button"
              role="radio"
              aria-checked={state.color === g.id}
              aria-label={g.id}
              onClick={() => setState({ color: asCrewColor(g.id) })}
              className={cn(
                "size-6 rounded-full ring-offset-2 ring-offset-card transition-shadow coarse:size-[44px]",
                g.swatch,
                state.color === g.id ? "ring-2 ring-foreground/70" : "hover:ring-2 hover:ring-foreground/20",
              )}
            />
          ))}
        </div>
        <div role="radiogroup" aria-label="Icon" className="grid grid-cols-10 gap-1 max-sm:grid-cols-5">
          {QUICK_CREW_ICONS.map((name) => {
            const def = getCrewIconDef(name)
            const Glyph = def.icon
            const on = state.icon === name
            return (
              <button
                key={name}
                type="button"
                role="radio"
                aria-checked={on}
                aria-label={def.label}
                title={def.label}
                onClick={() => setState({ icon: name })}
                className={cn(
                  "grid aspect-square place-items-center rounded-md border transition-colors coarse:min-h-[44px]",
                  on ? "border-primary/50 bg-primary/10 text-primary-hover" : "border-transparent text-muted-foreground hover:bg-foreground/[0.05] hover:text-foreground",
                )}
              >
                <Glyph className="size-4" aria-hidden />
              </button>
            )
          })}
        </div>
        <button type="button" onClick={onMore} className="text-label text-primary-hover hover:underline">
          More icons…
        </button>
      </div>
    </CreateSurfaceField>
  )
}

/**
 * Step 2 of "Suggest a team": the lead proposes agents from the description
 * (POST /crew-ai-suggest, the call behind `crewship crew suggest`). Nothing
 * exists yet; every agent can be renamed or removed before Create.
 */
export function StepSuggest({ workspaceId, state, setState }: {
  workspaceId: string
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}) {
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [asked, setAsked] = useState(state.suggested.length > 0)

  const ask = async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await apiFetch(`/api/v1/crew-ai-suggest?workspace_id=${encodeURIComponent(workspaceId)}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ description: state.description.trim() || state.name.trim() }),
      })
      if (!res.ok) throw await toApiError(res, `The suggestion did not come back (${res.status}).`)
      const data = (await res.json()) as AISuggestResponse
      setState({ suggested: Array.isArray(data.agents) ? data.agents : [] })
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
      setAsked(true)
    }
  }

  if (!asked && !loading) {
    return (
      <div className="space-y-3">
        <p className="text-body text-muted-foreground">
          The lead reads “{state.description.trim() || state.name.trim() || "your description"}” and proposes the agents. Nothing is created until you press Create.
        </p>
        <Button size="sm" onClick={() => void ask()}><Sparkles className="size-3.5" aria-hidden /> Suggest a team</Button>
      </div>
    )
  }
  if (loading) return <CreateSurfaceLoading rows={3} />
  if (error) {
    return (
      <CreateSurfaceNotice tone="warn">
        {error} Try again, or go back and start from a template or an empty crew.{" "}
        <button type="button" className="text-primary-hover hover:underline" onClick={() => void ask()}>Try again</button>
      </CreateSurfaceNotice>
    )
  }

  const update = (i: number, patch: { name?: string; role_title?: string }) =>
    setState({
      suggested: state.suggested.map((a, j) =>
        j === i ? { ...a, ...patch, ...(patch.name !== undefined ? { slug: normalizeSlug(patch.name) } : {}) } : a,
      ),
    })

  return (
    <div className="space-y-2" data-testid="crew-suggested-team">
      <p className="flex items-center gap-1.5 text-label text-muted-foreground">
        <Sparkles className="size-3.5" aria-hidden /> Suggested from your description. Nothing exists yet; rename or remove any agent.
      </p>
      {state.suggested.length === 0 && (
        <p className="text-body text-muted-foreground">No agents were suggested. Go back and describe the crew in a little more detail.</p>
      )}
      {state.suggested.map((a, i) => (
        <div key={i} className="flex items-center gap-2 rounded-lg border border-hairline p-2">
          <Bot className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <input
            aria-label={`Agent ${i + 1} name`}
            value={a.name}
            onChange={(e) => update(i, { name: e.target.value })}
            className="h-8 min-w-0 flex-1 rounded-md border border-hairline bg-background px-2 text-body outline-none focus:border-primary coarse:h-[44px]"
          />
          <input
            aria-label={`Agent ${i + 1} title`}
            value={a.role_title}
            onChange={(e) => update(i, { role_title: e.target.value })}
            className="h-8 min-w-0 flex-1 rounded-md border border-hairline bg-background px-2 text-label text-muted-foreground outline-none focus:border-primary coarse:h-[44px] max-sm:hidden"
          />
          <span className="shrink-0 text-micro text-muted-foreground">{agentRoleLabel(a.agent_role)}</span>
          <button
            type="button"
            aria-label={`Remove ${a.name}`}
            onClick={() => setState({ suggested: state.suggested.filter((_, j) => j !== i) })}
            className="grid size-7 shrink-0 place-items-center rounded-md text-muted-foreground hover:bg-foreground/[0.06] hover:text-foreground coarse:size-[44px]"
          >
            <X className="size-3.5" aria-hidden />
          </button>
        </div>
      ))}
    </div>
  )
}
