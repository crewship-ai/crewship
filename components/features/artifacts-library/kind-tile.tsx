import { cn } from "@/lib/utils"
import { kindAccent, KINDS, type ArtifactKind, type ResolvedKind } from "./kinds"

/** The kind's glyph on its tinted square, from the one registry: Word reads blue and Sheet green before the label is read. */
export function KindTile({ kind, size = "md", className }: { kind: ArtifactKind; size?: "sm" | "md" | "lg"; className?: string }) {
  const Icon = KINDS[kind].icon
  const accent = kindAccent(kind)
  return <span aria-hidden="true" data-kind-tile={kind} className={cn("flex shrink-0 items-center justify-center rounded-[28%] border", accent.chip, accent.fg,
    size === "lg" ? "size-10" : size === "sm" ? "size-5" : "size-7", className)}>
    <Icon className={size === "lg" ? "size-5" : size === "sm" ? "size-3" : "size-3.5"} />
  </span>
}

/** The kind's name in its colour, with a neutral qualifier (Static, Canvas, Library) when it has one. */
export function KindBadge({ resolved, className }: { resolved: ResolvedKind; className?: string }) {
  const def = KINDS[resolved.kind]
  const Icon = def.icon
  const accent = kindAccent(resolved.kind)
  return <span className={cn("inline-flex shrink-0 items-center gap-1.5", className)}>
    <span className={cn("inline-flex h-5 items-center gap-1 rounded-md px-1.5 text-micro font-medium", accent.soft, accent.fg)}>
      <Icon aria-hidden className="size-3" />{def.label}
    </span>
    {resolved.badge && <span className="rounded-sm border bg-muted px-1 text-micro font-medium leading-[1.6] text-muted-foreground">{resolved.badge}</span>}
  </span>
}
