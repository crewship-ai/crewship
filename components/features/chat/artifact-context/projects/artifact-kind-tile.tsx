import { FileQuestion } from "lucide-react"
import { cn } from "@/lib/utils"
import { KIND_ACCENT, KIND_ICON, artifactKind, type ArtifactKindKey } from "../new-artifact/artifact-kinds"

/**
 * The kind's coloured tile and its word, both read from the one kind
 * registry, so a chat card wears the same colour and glyph as the library row
 * for the same item. `null` is a kind the client cannot confirm: a neutral,
 * dashed tile that claims no kind.
 */
export function ArtifactKindTile({ kind, size = "md", className }: { kind: ArtifactKindKey | null; size?: "sm" | "md"; className?: string }) {
  const Icon = kind ? KIND_ICON[kind] : FileQuestion
  const accent = kind ? KIND_ACCENT[kind] : null
  return <span aria-hidden="true" data-kind={kind ?? "unknown"}
    className={cn("flex shrink-0 items-center justify-center rounded-[28%] border",
      accent ? cn(accent.chip, accent.fg) : "border-dashed border-border bg-muted text-muted-foreground",
      size === "sm" ? "size-6" : "size-8", className)}>
    <Icon className={size === "sm" ? "size-3.5" : "size-4"} />
  </span>
}

/** The kind's word in a quiet outlined label, as in the library list. */
export function ArtifactKindBadge({ kind }: { kind: ArtifactKindKey }) {
  return <span className="shrink-0 rounded-sm border bg-muted px-1.5 text-micro font-medium leading-[1.6] text-muted-foreground">{artifactKind(kind).label}</span>
}
