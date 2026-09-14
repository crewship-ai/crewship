"use client"

import { IconPickerDialog } from "@/components/ui/icon-picker-dialog"

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
 * The picker itself is `IconPickerDialog` (`components/ui/icon-picker-dialog.tsx`),
 * which page folders open too (#2527); this is the crew-shaped call with the
 * crew's defaults — `briefcase`, `blue`, the crews concept glyph and the
 * sentence about reusing an icon across crews. Nothing the crew canvas sees
 * changed when the body moved.
 */
export function CrewIconPickerDialog({
  open,
  onOpenChange,
  crewName,
  icon,
  color,
  onSave,
}: CrewIconPickerDialogProps) {
  return (
    <IconPickerDialog
      open={open}
      onOpenChange={onOpenChange}
      context={crewName}
      icon={icon}
      color={color}
      onSave={onSave}
      concept="crews"
      description="Pick an icon and a colour. The same icon reused across crews with different colours is a quick visual differentiator."
    />
  )
}
