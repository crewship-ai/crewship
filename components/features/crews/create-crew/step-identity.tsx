"use client"

import { useMemo, useState } from "react"
import { Pencil } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  CreateSurfaceDescriptionInput,
  CreateSurfaceField,
  CreateSurfacePicker,
  CreateSurfaceSection,
  CreateSurfaceTitleInput,
} from "@/components/layout/create-surface"
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
      {/* The kit's sections, the kit's title input, the kit's field — the same
       *  three parts New project and New agent are built from.
       *
       *  This step used to be a `grid-cols-[160px_1fr]` of hand-rolled labels
       *  and bordered inputs with its own <Label> helper at the bottom of the
       *  file. It carried the same fields and read as a different product:
       *  the name was a bordered box the size of the slug beside it rather
       *  than the title of the thing being made, and the caption under the
       *  icon was the only clue the tile could be clicked. */}
      <CreateSurfaceSection title="Identity" concept="crews">
        <div className="flex items-start gap-3">
          <button
            type="button"
            onClick={() => setPickerOpen((o) => !o)}
            aria-expanded={pickerOpen}
            className="group relative shrink-0 rounded-2xl outline-none transition-opacity hover:opacity-80 focus-visible:ring-2 focus-visible:ring-primary"
            aria-label="Pick icon and color"
          >
            <CrewIcon
              icon={state.icon}
              color={state.color}
              size="lg"
              className="border border-white/10 transition-colors group-hover:border-white/25"
            />
            <span className="absolute -bottom-1 -right-1 flex h-5 w-5 items-center justify-center rounded-full bg-primary text-white shadow-lg ring-2 ring-card">
              <Pencil className="h-3 w-3" />
            </span>
          </button>

          <div className="min-w-0 flex-1">
            <label htmlFor="crew-wizard-name" className="sr-only">Name</label>
            <CreateSurfaceTitleInput
              id="crew-wizard-name"
              value={state.name}
              onChange={(e) => onNameChange(e.target.value)}
              autoFocus
              placeholder="Engineering"
            />
            {/* The caption is still a target, not only a label: it is the
             *  written-out version of the pencil badge, and `max-sm:` padding
             *  is what makes 17px of text a 44px thumb target. */}
            <button
              type="button"
              onClick={() => setPickerOpen((o) => !o)}
              aria-expanded={pickerOpen}
              className="mt-1 block text-[11px] capitalize text-muted-foreground-soft transition-colors hover:text-foreground/80 max-sm:px-2 max-sm:py-4"
            >
              {state.icon} · {state.color} — tap to change
            </button>
          </div>
        </div>

        <CreateSurfaceField
          label="Slug"
          htmlFor="crew-wizard-slug"
          required
          // The permanence warning was a TIP notice taking four lines under
          // the fields. It is one fact about one field, so it belongs on that
          // field: same words, no box.
          hint={
            <>
              Lowercase, no spaces — how agents address this crew. <strong>Permanent</strong>: it is the
              URL and the CLI argument, as in{" "}
              <code className="rounded bg-black/40 px-1 py-0.5 font-mono text-[11px]">
                crewship agent create --crew {state.slug || "engineering"}
              </code>
              .
            </>
          }
        >
          <input
            id="crew-wizard-slug"
            value={state.slug}
            onChange={(e) => setState({ slug: e.target.value, slugTouched: true })}
            placeholder="engineering"
            className="h-8 w-full rounded-md border border-hairline bg-background px-3 font-mono text-xs outline-none transition-shadow focus:border-primary focus:ring-2 focus:ring-primary/20 max-sm:h-12 max-sm:text-sm"
          />
        </CreateSurfaceField>
      </CreateSurfaceSection>

      <CreateSurfaceSection title="What this crew is for" hint="optional">
        <label htmlFor="crew-wizard-description" className="sr-only">Description</label>
        <CreateSurfaceDescriptionInput
          id="crew-wizard-description"
          value={state.description}
          onChange={(e) => setState({ description: e.target.value })}
          placeholder="What does this crew do, in one line? It shows up wherever the crew is listed."
          rows={3}
          className="rounded-lg border border-hairline bg-foreground/[0.02] p-2.5 text-xs"
        />
      </CreateSurfaceSection>

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
