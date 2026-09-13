"use client"

import { useEffect, useMemo, useState } from "react"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfacePicker,
} from "@/components/layout/create-surface"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  CREW_ICON_CATEGORIES,
  GRADIENT_PALETTES,
  getCrewIconDef,
  searchCrewIcons,
} from "@/lib/entities"

export interface IconPickerDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** What the icon belongs to — the crew's name, the folder's name. */
  context: string
  /** Current icon slug (from CREW_ICONS catalog). */
  icon: string | null
  /** Current color id (from GRADIENT_PALETTES). */
  color: string | null
  onSave: (next: { icon: string; color: string }) => void | Promise<void>
  /** A CONCEPT_ICON key for the header glyph. Crews by default. */
  concept?: string
  description?: string
  /** Icon shown when `icon` is null — what the thing is drawn as today. */
  defaultIcon?: string
  defaultColor?: string
}

const DEFAULT_DESCRIPTION =
  "Pick an icon and a colour. The same icon reused with different colours is a quick visual differentiator."

/**
 * Icon + colour picker, opened from anything that carries a `CREW_ICONS` icon
 * and a palette colour — a crew's canvas, a page folder (#2527).
 *
 * This is the crew canvas's picker with its crew-shaped words moved to props;
 * `CrewIconPickerDialog` still exists and renders exactly this with the crew
 * defaults, so nothing about the crew flow changed. Wears the shared shell
 * and the kit's own picker, which is what New crew's icon step already
 * renders. Two reasons the hand-rolled version had to go, kept here because
 * they are the reason the shell is not optional:
 *
 *  · **Colour.** Radix's DialogContent is `bg-background` — oklch(0.10) in
 *    this theme, the darkest surface in the palette — while every migrated
 *    create surface is `bg-card` at oklch(0.155). The icon well inside it was
 *    darker again (`bg-background/30`). Opened from a page that uses the
 *    lighter card, it read as a hole rather than a dialog.
 *  · **Two pickers for one job.** The old file had its own preview, its own
 *    palette row and its own search-plus-grid; `CreateSurfacePicker` is that
 *    component, already built, already carrying the categories this one
 *    lacked. 345 icons behind a name-substring search and no categories is
 *    the browsing problem the kit's version solves.
 */
export function IconPickerDialog({
  open,
  onOpenChange,
  context,
  icon,
  color,
  onSave,
  concept = "crews",
  description = DEFAULT_DESCRIPTION,
  defaultIcon = "briefcase",
  defaultColor = "blue",
}: IconPickerDialogProps) {
  const [draftIcon, setDraftIcon] = useState(icon ?? defaultIcon)
  const [draftColor, setDraftColor] = useState(color ?? defaultColor)
  const [search, setSearch] = useState("")
  const [category, setCategory] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (open) {
      setDraftIcon(icon ?? defaultIcon)
      setDraftColor(color ?? defaultColor)
      setSearch("")
      setCategory(null)
    }
  }, [open, icon, color, defaultIcon, defaultColor])

  // Same resolver New crew's icon panel uses: a category when one is picked,
  // otherwise the search string, otherwise everything.
  const results = useMemo(() => searchCrewIcons(category ?? search), [search, category])

  const submit = async () => {
    setBusy(true)
    try {
      await onSave({ icon: draftIcon, color: draftColor })
      onOpenChange(false)
    } finally {
      setBusy(false)
    }
  }

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size="md"
      onSubmit={() => void submit()}
    >
      <CreateSurfaceHeader
        concept={concept}
        context={context}
        title="Icon"
        description={description}
        onClose={() => onOpenChange(false)}
      />

      <CreateSurfaceBody>
        <CreateSurfacePicker
          preview={<CrewIcon icon={draftIcon} color={draftColor} size="xl" />}
          previewHint={`${getCrewIconDef(draftIcon).label} · ${draftColor}`}
          palette={{
            value: draftColor,
            onChange: setDraftColor,
            options: GRADIENT_PALETTES.map((g) => ({ id: g.id, dot: g.dot })),
          }}
          categories={{
            value: category,
            options: CREW_ICON_CATEGORIES,
            onChange: (c) => { setCategory(c); setSearch("") },
          }}
          search={{
            value: search,
            onChange: (v) => { setSearch(v); setCategory(null) },
            placeholder: "Search icons…",
          }}
          options={results.map((name) => {
            const def = getCrewIconDef(name)
            return { id: name, label: def.label, render: <def.icon className="h-4 w-4 text-foreground/70" /> }
          })}
          value={draftIcon}
          onChange={setDraftIcon}
          columns={8}
        />
      </CreateSurfaceBody>

      <CreateSurfaceFooter
        onCancel={() => onOpenChange(false)}
        primaryLabel={busy ? "Saving…" : "Save"}
        onPrimary={() => void submit()}
        busy={busy}
        // Nothing reaches the server until Save, so there is no work for the
        // shell's are-you-sure to protect.
        guardCancel={false}
      />
    </CreateSurface>
  )
}
