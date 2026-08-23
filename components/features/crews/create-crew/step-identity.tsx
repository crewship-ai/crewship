"use client"

import { useMemo, useState } from "react"
import { Pencil } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { CreateSurfacePicker } from "@/components/layout/create-surface"
import { CREW_ICONS, GRADIENT_PALETTES } from "@/lib/entities"
import { asCrewColor, type WizardState } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

export function StepIdentity({ state, setState }: Props) {
  const [pickerOpen, setPickerOpen] = useState(false)
  const [iconSearch, setIconSearch] = useState("")

  const iconOptions = useMemo(() => {
    const q = iconSearch.trim().toLowerCase()
    return CREW_ICONS.filter((i) => (q ? i.label.toLowerCase().includes(q) : true)).map((i) => ({
      id: i.name,
      label: i.label,
      render: <i.icon className="h-4 w-4 text-foreground/70" />,
    }))
  }, [iconSearch])

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
            onClick={() => setPickerOpen((o) => !o)}
            aria-expanded={pickerOpen}
            className="group relative rounded-2xl outline-none focus-visible:ring-2 focus-visible:ring-primary"
            aria-label="Pick icon and color"
          >
            <CrewIcon
              icon={state.icon}
              color={state.color}
              size="xl"
              className="border border-white/10 group-hover:border-white/25 transition-colors scale-110"
            />
            <span className="absolute -bottom-1 -right-1 w-6 h-6 rounded-full bg-primary text-white flex items-center justify-center ring-2 ring-card shadow-lg group-hover:bg-primary transition-colors">
              <Pencil className="h-3 w-3" />
            </span>
          </button>
          <button
            type="button"
            onClick={() => setPickerOpen((o) => !o)}
            aria-expanded={pickerOpen}
            // Padding, not size: it is a caption that happens to be clickable,
            // and 17px of text is a 17px target. The icon above opens the same
            // picker and is large; this still has to be hittable.
            className="capitalize text-[11px] text-muted-foreground transition-colors hover:text-foreground/80 max-sm:px-2 max-sm:py-4"
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
                className="mt-1.5 w-full rounded-md border border-white/15 bg-background px-3 py-2 text-sm outline-none transition-shadow focus:border-primary focus:ring-2 focus:ring-primary/20 max-sm:h-12"
              />
            </div>
            <div>
              <Label required htmlFor="crew-wizard-slug">Slug</Label>
              <input
                id="crew-wizard-slug"
                value={state.slug}
                onChange={(e) => setState({ slug: e.target.value, slugTouched: true })}
                placeholder="engineering"
                className="mt-1.5 w-full rounded-md border border-white/15 bg-background px-3 py-2 font-mono text-sm outline-none transition-shadow focus:border-primary focus:ring-2 focus:ring-primary/20 max-sm:h-12"
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
              className="mt-1.5 w-full rounded-md border border-white/15 bg-background px-3 py-2 text-sm outline-none transition-shadow focus:border-primary focus:ring-2 focus:ring-primary/20 max-sm:h-12"
            />
          </div>

          <div className="rounded-md border border-info/25 bg-info/[0.05] px-3 py-2.5 text-xs text-foreground/80 flex gap-2.5 items-start">
            <span className="shrink-0 text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded bg-info/90 text-info mt-0.5">
              TIP
            </span>
            <span className="leading-relaxed">
              Icon, color, and description are editable later. <strong>Slug is permanent</strong> — it's used in URLs and CLI commands like
              {" "}<code className="text-[11px] font-mono bg-black/40 px-1 py-0.5 rounded">crewship agent create --crew {state.slug || "engineering"}</code>.
            </span>
          </div>
        </div>
      </div>

      {/* In the body, not a second Dialog on top of the first.
       *
       * This used to mount CrewIconPickerDialog, which is a full Radix
       * `<Dialog>` — so opening it put two overlays, two headers and two
       * Cancel buttons on screen at once, which is the exact shape this
       * migration exists to remove. That component is NOT changed: the crew
       * detail page (crew-canvas.tsx) renders it standalone, where a dialog
       * IS the right answer.
       *
       * A plain conditional rather than CreateSurfaceDisclosure: the
       * disclosure owns its open state internally, so the icon tile above —
       * which is the affordance people actually click — could not drive it. */}
      {pickerOpen && (
        <CreateSurfacePicker
          preview={<CrewIcon icon={state.icon} color={state.color} size="xl" />}
          previewHint="The face this crew wears in the roster, the sidebar, and every issue it owns."
          palette={{
            value: state.color,
            onChange: (id) => setState({ color: asCrewColor(id) }),
            options: GRADIENT_PALETTES.map((g) => ({ id: g.id, dot: g.dot })),
          }}
          search={{ value: iconSearch, onChange: setIconSearch, placeholder: "Search icons…" }}
          options={iconOptions}
          value={state.icon}
          onChange={(icon) => setState({ icon })}
        />
      )}
    </>
  )
}

function Label({ children, required, htmlFor }: { children: React.ReactNode; required?: boolean; htmlFor?: string }) {
  return (
    <label htmlFor={htmlFor} className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
      {children}
      {required && <span className="text-destructive ml-1">*</span>}
    </label>
  )
}
