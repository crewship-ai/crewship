"use client"

import { useState } from "react"
import { Pencil } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { CrewIconPickerDialog } from "../crew-icon-picker-dialog"
import { asCrewColor, type WizardState } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

export function StepIdentity({ state, setState }: Props) {
  const [pickerOpen, setPickerOpen] = useState(false)

  const onNameChange = (val: string) => {
    if (state.slugTouched) {
      setState({ name: val })
      return
    }
    const auto = val.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")
    setState({ name: val, slug: auto })
  }

  return (
    <>
      <div className="grid grid-cols-[160px_1fr] gap-6 items-start">
        {/* Left column — single icon-tile button that opens the full picker dialog */}
        <div className="flex flex-col items-center gap-2">
          <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium self-start">
            Icon &amp; color
          </label>
          <button
            type="button"
            onClick={() => setPickerOpen(true)}
            className="group relative outline-none focus-visible:ring-2 focus-visible:ring-blue-400 rounded-2xl"
            aria-label="Pick icon and color"
          >
            <CrewIcon
              icon={state.icon}
              color={state.color}
              size="xl"
              className="border border-white/10 group-hover:border-white/25 transition-colors scale-110"
            />
            <span className="absolute -bottom-1 -right-1 w-6 h-6 rounded-full bg-blue-500 text-white flex items-center justify-center ring-2 ring-card shadow-lg group-hover:bg-blue-400 transition-colors">
              <Pencil className="h-3 w-3" />
            </span>
          </button>
          <button
            type="button"
            onClick={() => setPickerOpen(true)}
            className="text-[11px] text-muted-foreground hover:text-foreground/80 transition-colors capitalize"
          >
            {state.icon} · {state.color}
          </button>
        </div>

        {/* Right column — form fields */}
        <div className="min-w-0 space-y-4">
          <div className="grid grid-cols-[2fr_1fr] gap-3">
            <div>
              <Label required htmlFor="crew-wizard-name">Name</Label>
              <input
                id="crew-wizard-name"
                value={state.name}
                onChange={(e) => onNameChange(e.target.value)}
                autoFocus
                placeholder="Engineering"
                className="mt-1.5 w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-400/20 transition-shadow"
              />
            </div>
            <div>
              <Label required htmlFor="crew-wizard-slug">Slug</Label>
              <input
                id="crew-wizard-slug"
                value={state.slug}
                onChange={(e) => setState({ slug: e.target.value, slugTouched: true })}
                placeholder="engineering"
                className="mt-1.5 w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm font-mono outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-400/20 transition-shadow"
              />
            </div>
          </div>

          <div>
            <Label htmlFor="crew-wizard-description">
              Description
              <span className="text-muted-foreground normal-case tracking-normal text-[11px] font-normal ml-2">
                optional, shown in roster &amp; sidebar
              </span>
            </Label>
            <input
              id="crew-wizard-description"
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

      <CrewIconPickerDialog
        open={pickerOpen}
        onOpenChange={setPickerOpen}
        crewName={state.name || "new crew"}
        icon={state.icon}
        color={state.color}
        onSave={({ icon, color }) => {
          setState({ icon, color: asCrewColor(color) })
        }}
      />
    </>
  )
}

function Label({ children, required, htmlFor }: { children: React.ReactNode; required?: boolean; htmlFor?: string }) {
  return (
    <label htmlFor={htmlFor} className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
      {children}
      {required && <span className="text-red-400 ml-1">*</span>}
    </label>
  )
}
