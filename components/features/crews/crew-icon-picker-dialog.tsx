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

export interface CrewIconPickerDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  crewName: string
  /** Current icon slug (from CREW_ICONS catalog). */
  icon: string | null
  /** Current color id (from GRADIENT_PALETTES). */
  color: string | null
  onSave: (next: { icon: string; color: string }) => void | Promise<void>
}

/**
 * Crew icon + color picker, opened from the crew canvas.
 *
 * Wears the shared shell and the kit's own picker, which is what New crew's
 * icon step already renders. Two reasons the hand-rolled version had to go:
 *
 *  · **Colour.** Radix's DialogContent is `bg-background` — oklch(0.10) in
 *    this theme, the darkest surface in the palette — while every migrated
 *    create surface is `bg-card` at oklch(0.155). The icon well inside it was
 *    darker again (`bg-background/30`). Opened from a page that uses the
 *    lighter card, it read as a hole rather than a dialog.
 *  · **Two pickers for one job.** This file had its own preview, its own
 *    palette row and its own search-plus-grid; `CreateSurfacePicker` is that
 *    component, already built, already carrying the categories this one
 *    lacked. 345 icons behind a name-substring search and no categories is
 *    the browsing problem the kit's version solves.
 */
export function CrewIconPickerDialog({
  open,
  onOpenChange,
  crewName,
  icon,
  color,
  onSave,
}: CrewIconPickerDialogProps) {
  const [draftIcon, setDraftIcon] = useState(icon ?? "briefcase")
  const [draftColor, setDraftColor] = useState(color ?? "blue")
  const [search, setSearch] = useState("")
  const [category, setCategory] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (open) {
      setDraftIcon(icon ?? "briefcase")
      setDraftColor(color ?? "blue")
      setSearch("")
      setCategory(null)
    }
  }, [open, icon, color])

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
        concept="crews"
        context={crewName}
        title="Icon"
        description="Pick an icon and a colour. The same icon reused across crews with different colours is a quick visual differentiator."
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
