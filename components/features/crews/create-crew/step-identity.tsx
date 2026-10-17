"use client"

import { Pencil } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  CreateSurfaceDescriptionInput,
  CreateSurfaceField,
  CreateSurfaceSection,
  CreateSurfaceTitleInput,
} from "@/components/layout/create-surface"
import { getCrewIconDef } from "@/lib/entities"
import { normalizeSlug, slugFromName, type CrewStart, type WizardState } from "./types"
import { InlineIconPicker, StartChoice } from "./step-start"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
  /** Opens the full icon catalogue (a panel of the wizard). */
  onPickIcon: () => void
  /** New crew only: how it starts. Absent while editing a crew. */
  start?: CrewStart
  onStart?: (start: CrewStart) => void
}

export function StepIdentity({ state, setState, onPickIcon, start, onStart }: Props) {
  const onNameChange = (val: string) => {
    if (state.slugTouched) {
      setState({ name: val })
      return
    }
    // Derive through the same rule the field itself enforces, so the
    // auto-filled slug and a hand-typed one cannot disagree. The old inline
    // version dropped underscores, which the server accepts.
    setState({ name: val, slug: slugFromName(val) })
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
      <CreateSurfaceSection title="Name" concept="crews">
        <div className="flex items-start gap-3">
          {/* Editing keeps the icon tile that opens the full picker; New crew
              picks icon and colour inline below. */}
          {!start && (
            <button
              type="button"
              onClick={onPickIcon}
              className="group relative shrink-0 rounded-xl outline-none transition-opacity hover:opacity-80 focus-visible:ring-2 focus-visible:ring-primary"
              aria-label="Pick icon and color"
            >
              <CrewIcon icon={state.icon} color={state.color} size="lg" className="border border-foreground/10" />
              <span className="absolute -bottom-1 -right-1 flex h-5 w-5 items-center justify-center rounded-full bg-primary text-primary-foreground ring-2 ring-card">
                <Pencil className="h-3 w-3" strokeWidth={2.5} />
              </span>
            </button>
          )}
          <div className="min-w-0 flex-1">
            <label htmlFor="crew-wizard-name" className="sr-only">Name</label>
            <CreateSurfaceTitleInput
              id="crew-wizard-name"
              value={state.name}
              onChange={(e) => onNameChange(e.target.value)}
              autoFocus
              placeholder="Engineering"
            />
            {!start && (
              <button
                type="button"
                onClick={onPickIcon}
                className="mt-1 block text-label text-muted-foreground-soft transition-colors hover:text-foreground/80 max-sm:px-2 max-sm:py-4"
              >
                {getCrewIconDef(state.icon).label} · {state.color} — tap to change
              </button>
            )}
          </div>
        </div>

        <CreateSurfaceField
          label="Slug"
          htmlFor="crew-wizard-slug"
          required
          // "Lowercase, no spaces" is gone because the field now enforces it
          // rather than asking. What is left is the one fact the input cannot
          // make true by itself: you do not get to change this later.
          hint={
            <>
              <strong>Permanent</strong> — URLs, YAML and the CLI use it, as in{" "}
              <code className="rounded bg-muted px-1 py-0.5 font-mono">crewship agent create --crew {state.slug || "engineering"}</code>
            </>
          }
        >
          <input
            id="crew-wizard-slug"
            value={state.slug}
            onChange={(e) => setState({ slug: normalizeSlug(e.target.value), slugTouched: true })}
            placeholder="engineering"
            className="h-8 w-full rounded-md border border-hairline bg-background px-3 font-mono text-xs outline-none transition-shadow focus:border-primary focus:ring-2 focus:ring-primary/20 coarse:h-12 coarse:text-sm"
          />
        </CreateSurfaceField>
      </CreateSurfaceSection>

      {start && <InlineIconPicker state={state} setState={setState} onMore={onPickIcon} />}

      {start !== "yaml" && (
        <CreateSurfaceField
          label="What should this crew do?"
          htmlFor="crew-wizard-description"
          hint={start === "suggest"
            ? "The lead reads this to propose the team; you can change everything before anything is created."
            : "Shown on the crew and read by its agents."}
        >
          <CreateSurfaceDescriptionInput
            id="crew-wizard-description"
            value={state.description}
            onChange={(e) => setState({ description: e.target.value })}
            placeholder="What does this crew do? Weekly research on competitors and a short report for the team"
            rows={3}
            className="rounded-lg border border-hairline bg-foreground/[0.02] p-2.5 text-xs"
          />
        </CreateSurfaceField>
      )}

      {start && onStart && <StartChoice value={start} onChange={onStart} />}


    </>
  )
}
