import { getCrewIconDef, getGradientPalette } from "@/lib/crew-icon"
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
  const s = sizeMap[size]
  const IconComp = def.icon

  return (
    <div
      className={cn(
        "bg-gradient-to-br flex items-center justify-center shrink-0",
        palette.from,
        palette.to,
        s.box,
        className,
      )}
    >
      <IconComp className={cn(s.icon, palette.text)} />
    </div>
  )
}
