"use client"

import { useState } from "react"
import { Search, Pencil, Check } from "lucide-react"
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
  const onNameChange = (val: string) => {
    if (state.slugTouched) {
      setState({ name: val })
      return
    }
    const auto = val.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")
    setState({ name: val, slug: auto })
  }

  return (
    <div className="grid grid-cols-[200px_1fr] gap-6 items-start">
      {/* Left column — Icon + color, both labeled */}
      <div className="space-y-4">
        <IconSection icon={state.icon} color={state.color} onPick={(icon) => setState({ icon })} />
        <ColorSection color={state.color} onPick={(color) => setState({ color })} />
      </div>

      {/* Right column — form fields */}
      <div className="min-w-0 space-y-4">
        <div className="grid grid-cols-[2fr_1fr] gap-3">
          <div>
            <Label required>Name</Label>
            <input
              value={state.name}
              onChange={(e) => onNameChange(e.target.value)}
              autoFocus
              placeholder="Engineering"
              className="mt-1.5 w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-400/20 transition-shadow"
            />
          </div>
          <div>
            <Label required>Slug</Label>
            <input
              value={state.slug}
              onChange={(e) => setState({ slug: e.target.value, slugTouched: true })}
              placeholder="engineering"
              className="mt-1.5 w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm font-mono outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-400/20 transition-shadow"
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
            className="mt-1.5 w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-400/20 transition-shadow"
          />
        </div>

        <div className="rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2.5 text-xs text-foreground/80 flex gap-2.5 items-start">
          <span className="shrink-0 text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded bg-blue-500/90 text-blue-950 mt-0.5">
            TIP
          </span>
          <span className="leading-relaxed">
            Icon, color, and description are editable later. <strong>Slug is permanent</strong> — it's used in URLs and CLI commands like
            {" "}<code className="text-[11px] font-mono bg-black/40 px-1 py-0.5 rounded">crewship agent create --crew {state.slug || "engineering"}</code>.
          </span>
        </div>
      </div>
    </div>
  )
}

// =============================================================================
// Icon section — large preview tile with popover picker
// =============================================================================

function IconSection({ icon, color, onPick }: { icon: string; color: string; onPick: (name: string) => void }) {
  const [iconQuery, setIconQuery] = useState("")
  const iconResults = searchCrewIcons(iconQuery)

  return (
    <div>
      <Label>Icon</Label>
      <div className="mt-2 flex flex-col items-center gap-2">
        <Popover>
          <PopoverTrigger asChild>
            <button
              type="button"
              className="group relative outline-none focus-visible:ring-2 focus-visible:ring-blue-400 rounded-2xl"
              aria-label="Pick icon"
            >
              <CrewIcon icon={icon} color={color} size="xl" className="border border-white/10 group-hover:border-white/20 transition-colors" />
              <span className="absolute -bottom-1 -right-1 w-6 h-6 rounded-full bg-blue-500 text-white flex items-center justify-center ring-2 ring-card shadow-lg group-hover:bg-blue-400 transition-colors">
                <Pencil className="h-3 w-3" />
              </span>
            </button>
          </PopoverTrigger>
          <PopoverContent align="start" sideOffset={8} className="w-[340px] p-3">
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
                      onClick={() => onPick(name)}
                      title={def.label}
                      className={cn(
                        "rounded-lg border p-1.5 transition-colors hover:bg-white/5 flex items-center justify-center",
                        icon === name
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
        <span className="text-[10.5px] text-muted-foreground capitalize font-medium">
          {getCrewIconDef(icon).label}
        </span>
      </div>
    </div>
  )
}

// =============================================================================
// Color section — labeled, 4×2 grid of generous swatches with active checkmark
// =============================================================================

function ColorSection({ color, onPick }: { color: string; onPick: (id: string) => void }) {
  const active = GRADIENT_PALETTES.find((p) => p.id === color) ?? GRADIENT_PALETTES[0]
  return (
    <div>
      <div className="flex items-baseline justify-between">
        <Label>Color</Label>
        <span className="text-[10.5px] text-muted-foreground capitalize">{active.id}</span>
      </div>
      <div className="mt-2 grid grid-cols-4 gap-2">
        {GRADIENT_PALETTES.map((p) => {
          const isActive = p.id === color
          return (
            <button
              key={p.id}
              type="button"
              onClick={() => onPick(p.id)}
              title={p.id}
              aria-label={`Color ${p.id}`}
              className={cn(
                "group relative aspect-square rounded-lg transition-transform outline-none focus-visible:ring-2 focus-visible:ring-blue-400 focus-visible:ring-offset-2 focus-visible:ring-offset-card",
                isActive ? "scale-105 shadow-lg" : "hover:scale-105",
              )}
              style={{
                background: `linear-gradient(135deg, ${p.dot} 0%, ${darken(p.dot)} 100%)`,
                boxShadow: isActive ? `0 0 0 2px var(--card), 0 0 0 4px ${p.dot}` : undefined,
              }}
            >
              {isActive && (
                <span className="absolute inset-0 flex items-center justify-center">
                  <Check className="h-3.5 w-3.5 text-white drop-shadow-md" strokeWidth={3} />
                </span>
              )}
            </button>
          )
        })}
      </div>
    </div>
  )
}

// =============================================================================
// Helpers
// =============================================================================

function Label({ children, required }: { children: React.ReactNode; required?: boolean }) {
  return (
    <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
      {children}
      {required && <span className="text-red-400 ml-1">*</span>}
    </label>
  )
}

// Roughly darken a hex color for the bottom of the swatch gradient. Doesn't
// need to be perfect — pure visual polish.
function darken(hex: string): string {
  const m = /^#?([\da-f]{6})$/i.exec(hex)
  if (!m) return hex
  const n = parseInt(m[1], 16)
  const r = Math.max(0, ((n >> 16) & 0xff) - 40)
  const g = Math.max(0, ((n >> 8) & 0xff) - 40)
  const b = Math.max(0, (n & 0xff) - 40)
  return `#${[r, g, b].map((x) => x.toString(16).padStart(2, "0")).join("")}`
}
