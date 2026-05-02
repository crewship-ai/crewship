"use client"

import { useState } from "react"
import { Search, Pencil } from "lucide-react"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { CrewIcon } from "@/components/ui/crew-icon"
import { GRADIENT_PALETTES, getCrewIconDef, searchCrewIcons, CREW_ICON_CATEGORIES } from "@/lib/crew-icon"
import { cn } from "@/lib/utils"
import type { WizardState } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

export function StepIdentity({ state, setState }: Props) {
  const [iconQuery, setIconQuery] = useState("")
  const iconResults = searchCrewIcons(iconQuery)

  const onNameChange = (val: string) => {
    if (state.slugTouched) {
      setState({ name: val })
      return
    }
    const auto = val.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")
    setState({ name: val, slug: auto })
  }

  return (
    <div className="flex gap-6 items-start">
      <div className="shrink-0 flex flex-col items-center gap-3">
        <Label>Icon &amp; color</Label>
        <Popover>
          <PopoverTrigger asChild>
            <button
              type="button"
              className="relative outline-none focus-visible:ring-2 focus-visible:ring-blue-400 rounded-2xl"
              aria-label="Pick icon"
            >
              <CrewIcon icon={state.icon} color={state.color} size="xl" className="border border-white/10" />
              <span className="absolute -bottom-1 -right-1 w-6 h-6 rounded-full bg-blue-500 text-white flex items-center justify-center ring-2 ring-card">
                <Pencil className="h-3 w-3" />
              </span>
            </button>
          </PopoverTrigger>
          <PopoverContent align="start" className="w-[340px] p-3">
            <div className="space-y-3">
              <div className="relative">
                <Search className="absolute left-2.5 top-2.5 h-3.5 w-3.5 text-muted-foreground" />
                <input
                  value={iconQuery}
                  onChange={(e) => setIconQuery(e.target.value)}
                  placeholder="Search icons… (try 'engineering', 'security')"
                  className="w-full pl-8 pr-2 py-1.5 text-xs bg-zinc-950 border border-white/15 rounded outline-none focus:border-blue-400"
                />
              </div>
              <div className="flex flex-wrap gap-1">
                {CREW_ICON_CATEGORIES.slice(0, 12).map((cat) => (
                  <button
                    key={cat}
                    type="button"
                    onClick={() => setIconQuery(iconQuery === cat ? "" : cat)}
                    className={cn(
                      "px-1.5 py-0.5 rounded text-[10px] capitalize border transition-colors",
                      iconQuery === cat
                        ? "border-blue-400 bg-blue-500/20 text-blue-300"
                        : "border-white/10 text-muted-foreground hover:border-white/20",
                    )}
                  >
                    {cat}
                  </button>
                ))}
              </div>
              <div className="grid grid-cols-8 gap-1.5 max-h-48 overflow-y-auto pr-1">
                {iconResults.slice(0, 64).map((name) => {
                  const def = getCrewIconDef(name)
                  const Icon = def.icon
                  return (
                    <button
                      key={name}
                      type="button"
                      onClick={() => setState({ icon: name })}
                      title={def.label}
                      className={cn(
                        "rounded-lg border p-1.5 transition-colors hover:bg-white/5 flex items-center justify-center",
                        state.icon === name
                          ? "border-blue-400 bg-blue-500/10"
                          : "border-white/10",
                      )}
                    >
                      <Icon className="h-4 w-4 text-muted-foreground" />
                    </button>
                  )
                })}
              </div>
            </div>
          </PopoverContent>
        </Popover>

        <div className="flex gap-1.5 mt-2">
          {GRADIENT_PALETTES.map((p) => (
            <button
              key={p.id}
              type="button"
              onClick={() => setState({ color: p.id })}
              title={p.id}
              className={cn(
                "h-6 w-6 rounded-md border-2 transition-all",
                state.color === p.id ? "border-foreground scale-110" : "border-transparent hover:scale-105",
              )}
              style={{ backgroundColor: p.dot }}
            />
          ))}
        </div>
      </div>

      <div className="flex-1 min-w-0 space-y-3">
        <div className="flex gap-3">
          <div className="flex-[2] min-w-0">
            <Label required>Name</Label>
            <input
              value={state.name}
              onChange={(e) => onNameChange(e.target.value)}
              autoFocus
              placeholder="Engineering"
              className="mt-1 w-full bg-zinc-950 border border-white/15 rounded px-2 py-1.5 text-sm outline-none focus:border-blue-400"
            />
          </div>
          <div className="flex-1 min-w-0">
            <Label required>Slug</Label>
            <input
              value={state.slug}
              onChange={(e) => setState({ slug: e.target.value, slugTouched: true })}
              placeholder="engineering"
              className="mt-1 w-full bg-zinc-950 border border-white/15 rounded px-2 py-1.5 text-sm font-mono outline-none focus:border-blue-400"
            />
          </div>
        </div>

        <div>
          <Label>
            Description
            <span className="text-muted-foreground/60 normal-case tracking-normal text-[11px] font-normal ml-2">
              optional, shown in roster &amp; sidebar
            </span>
          </Label>
          <input
            value={state.description}
            onChange={(e) => setState({ description: e.target.value })}
            placeholder="What does this crew do, in one line?"
            className="mt-1 w-full bg-zinc-950 border border-white/15 rounded px-2 py-1.5 text-sm outline-none focus:border-blue-400"
          />
        </div>

        <div className="rounded border border-blue-500/30 bg-blue-500/[0.06] px-3 py-2 text-xs text-foreground/80 flex gap-2 items-start">
          <span className="shrink-0 text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded bg-blue-500 text-blue-950">
            TIP
          </span>
          <span>
            Icon, color, and description are editable later. <strong>Slug is permanent</strong> — it's used in URLs and CLI commands like
            {" "}<code className="text-[11px] font-mono bg-black/40 px-1 py-0.5 rounded">crewship agent create --crew {state.slug || "engineering"}</code>.
          </span>
        </div>
      </div>
    </div>
  )
}

function Label({ children, required }: { children: React.ReactNode; required?: boolean }) {
  return (
    <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
      {children}
      {required && <span className="text-red-400 ml-1">*</span>}
    </label>
  )
}
