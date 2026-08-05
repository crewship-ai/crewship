import { crewColorHex, getCrewIconDef, getGradientPalette, iconColorProps } from "@/lib/entities"
import { cn } from "@/lib/utils"

interface CrewIconProps {
  icon: string
  color?: string | null
  size?: "sm" | "md" | "lg" | "xl"
  className?: string
}

const sizeMap = {
  sm: { box: "h-7 w-7 rounded-lg", icon: "h-3.5 w-3.5" },
  md: { box: "h-10 w-10 rounded-xl", icon: "h-5 w-5" },
  lg: { box: "h-12 w-12 rounded-xl", icon: "h-6 w-6" },
  xl: { box: "h-14 w-14 rounded-2xl", icon: "h-7 w-7" },
}

export function CrewIcon({ icon, color, size = "md", className }: CrewIconProps) {
  const def = getCrewIconDef(icon)
  const palette = getGradientPalette(color)
  // A crew's colour is a palette id for some rows and a raw hex for most.
  // The class-based palette can only express the ids, so a hex is tinted
  // inline — otherwise every hex-coloured crew silently renders in the
  // fallback palette and a workspace of five crews looks like one crew.
  const hex = crewColorHex(color)
  const glyph = iconColorProps(color)
  const s = sizeMap[size]
  const IconComp = def.icon

  return (
    <div
      className={cn(
        "flex items-center justify-center shrink-0",
        // The gradient utility and its stops belong together: with an inline
        // tint they are not just unused, they would paint over it.
        hex ? undefined : ["bg-gradient-to-br", palette.from, palette.to],
        s.box,
        className,
      )}
      style={
        hex
          ? // Same weight as the class-based stops (15% → 8%), one hue.
            { backgroundImage: `linear-gradient(to bottom right, ${hex}26, ${hex}14)` }
          : undefined
      }
    >
      {/* Same decision as every other icon on the product, made in one
          place: see iconColorProps. */}
      <IconComp className={cn(s.icon, glyph.className)} style={glyph.style} />
    </div>
  )
}
