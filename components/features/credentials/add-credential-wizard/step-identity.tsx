"use client"

import * as React from "react"
import { Check, ChevronsUpDown, X } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { cn } from "@/lib/utils"
import type { WizardState } from "./types"

interface Crew {
  id: string
  name: string
}

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
  workspaceId: string
}

export function StepIdentity({ state, setState, workspaceId }: Props) {
  const [crews, setCrews] = React.useState<Crew[]>([])
  const [crewLoading, setCrewLoading] = React.useState(false)
  const [crewPopoverOpen, setCrewPopoverOpen] = React.useState(false)

  React.useEffect(() => {
    if (state.scope !== "CREW" || crews.length > 0) return
    setCrewLoading(true)
    fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      .then((r) => {
        // 4xx/5xx responses still resolve the promise; reject so the
        // catch branch sets [] instead of trying to parse an error
        // payload as Crew[] (CodeRabbit caught the silent failure).
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((data: Crew[]) => setCrews(Array.isArray(data) ? data : []))
      .catch(() => setCrews([]))
      .finally(() => setCrewLoading(false))
  }, [state.scope, workspaceId, crews.length])

  const lastFour = state.value.length >= 4 ? state.value.slice(-4) : ""
  const autoLabel = lastFour && state.provider ? `${state.provider.toLowerCase()} · ...${lastFour}` : ""

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">
            Name (env variable)
          </label>
          <input
            value={state.name}
            onChange={(e) => setState({ name: e.target.value })}
            placeholder="ANTHROPIC_API_KEY"
            className={cn(
              "w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm font-mono outline-none focus:border-blue-400",
              state.type !== "SECRET" && "bg-zinc-950/50",
            )}
            readOnly={state.type !== "SECRET" && state.provider !== "CUSTOM_CLI" && state.provider !== "NONE"}
          />
        </div>
        <div>
          <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">
            Account label <span className="text-red-400">*</span>
          </label>
          <input
            value={state.accountLabel}
            onChange={(e) => setState({ accountLabel: e.target.value })}
            placeholder={autoLabel || "e.g. production, my-claude-max"}
            className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm outline-none focus:border-blue-400"
            required
          />
        </div>
      </div>

      <div>
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">
          Description (optional)
        </label>
        <input
          value={state.description}
          onChange={(e) => setState({ description: e.target.value })}
          placeholder="What's this credential for?"
          className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm outline-none focus:border-blue-400"
        />
      </div>

      <div>
        <label htmlFor="credential-expires" className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">
          Expires
        </label>
        <input
          id="credential-expires"
          type="date"
          value={state.expiresAt}
          onChange={(e) => setState({ expiresAt: e.target.value })}
          className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm outline-none focus:border-blue-400"
        />
        <p className="text-[11px] text-muted-foreground mt-1">
          GitLab default = 365 days. Leave blank for no expiration override.
        </p>
      </div>

      {/* Scope: 2 cards (Workspace / Crew) instead of a Select */}
      <div>
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">
          Scope
        </label>
        <div className="grid grid-cols-2 gap-2">
          {(["WORKSPACE", "CREW"] as const).map((s) => {
            const isSelected = state.scope === s
            return (
              <button
                key={s}
                type="button"
                onClick={() => setState({ scope: s, crewIds: s === "WORKSPACE" ? [] : state.crewIds })}
                className={cn(
                  "rounded-md border bg-zinc-950 p-3 text-left transition-all",
                  isSelected
                    ? "border-blue-400 ring-2 ring-blue-400/20"
                    : "border-white/10 hover:border-white/25 hover:bg-white/[0.02]",
                )}
              >
                <div className="text-sm font-medium">
                  {s === "WORKSPACE" ? "Workspace" : "Specific crews"}
                </div>
                <div className="text-[11px] text-muted-foreground mt-0.5">
                  {s === "WORKSPACE"
                    ? "All agents in this workspace can use it"
                    : "Pick which crews can use it"}
                </div>
              </button>
            )
          })}
        </div>
      </div>

      {state.scope === "CREW" && (
        <div>
          <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">
            Crews <span className="text-red-400">*</span>
          </label>
          {crewLoading ? (
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <Spinner className="h-3.5 w-3.5" />
              Loading crews...
            </div>
          ) : (
            <>
              <Popover open={crewPopoverOpen} onOpenChange={setCrewPopoverOpen}>
                <PopoverTrigger asChild>
                  <Button variant="outline" role="combobox" className="w-full justify-between font-normal bg-zinc-950">
                    {state.crewIds.length === 0
                      ? "Select crews..."
                      : `${state.crewIds.length} crew${state.crewIds.length > 1 ? "s" : ""} selected`}
                    <ChevronsUpDown className="ml-2 h-4 w-4 shrink-0 opacity-50" />
                  </Button>
                </PopoverTrigger>
                <PopoverContent className="w-[--radix-popover-trigger-width] p-0" align="start">
                  <Command>
                    <CommandInput placeholder="Search crews..." />
                    <CommandList>
                      <CommandEmpty>No crews found.</CommandEmpty>
                      <CommandGroup>
                        {crews.map((c) => {
                          const isSel = state.crewIds.includes(c.id)
                          return (
                            <CommandItem
                              key={c.id}
                              value={c.name}
                              onSelect={() => {
                                setState({
                                  crewIds: isSel
                                    ? state.crewIds.filter((id) => id !== c.id)
                                    : [...state.crewIds, c.id],
                                })
                              }}
                            >
                              <Check className={cn("mr-2 h-4 w-4", isSel ? "opacity-100" : "opacity-0")} />
                              {c.name}
                            </CommandItem>
                          )
                        })}
                      </CommandGroup>
                    </CommandList>
                  </Command>
                </PopoverContent>
              </Popover>
              {state.crewIds.length > 0 && (
                <div className="flex flex-wrap gap-1 mt-2">
                  {state.crewIds.map((id) => {
                    const c = crews.find((x) => x.id === id)
                    return c ? (
                      <Badge
                        key={id}
                        variant="secondary"
                        className="cursor-pointer"
                        onClick={() => setState({ crewIds: state.crewIds.filter((x) => x !== id) })}
                      >
                        {c.name}
                        <X className="ml-1 h-3 w-3" />
                      </Badge>
                    ) : null
                  })}
                </div>
              )}
            </>
          )}
        </div>
      )}
    </div>
  )
}
