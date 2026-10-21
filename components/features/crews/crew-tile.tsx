import { getCrewDotColor, getCrewIconDef } from "@/lib/entities"
import { cn } from "@/lib/utils"

/**
 * The crew's tile as Harbor 2 draws it: a solid square in the crew colour with
 * a white glyph (48 px on the crew page, 20 px in the panel). The colour is a
 * palette id or a hex, whichever picker wrote it; getCrewDotColor reads both.
 */
export function CrewTile({ icon, color, size = 20, className }: { icon?: string | null; color?: string | null; size?: number; className?: string }) {
  const Icon = getCrewIconDef(icon || "briefcase").icon
  return (
    <span
      data-slot="crew-tile"
      aria-hidden
      className={cn("inline-grid shrink-0 place-items-center text-white", className)}
      style={{ width: size, height: size, borderRadius: "28%", backgroundColor: getCrewDotColor(color) }}
    >
      <Icon className="text-white" style={{ width: Math.round(size * 0.56), height: Math.round(size * 0.56) }} />
    </span>
  )
}
