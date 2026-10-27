import { Lock } from "lucide-react"
import { cn } from "@/lib/utils"
import type { CraftLeaseHolder } from "../projects/types"

const CHIP = "inline-flex items-center gap-1 rounded-full px-1.5 text-micro font-semibold leading-[1.6]"

/** Lease state for a Craft card in the conversation: who edits, or free to edit. */
export function CraftStateChip({ holder, mine }: { holder: CraftLeaseHolder | null; mine: boolean }) {
  if (!holder) return <span className={cn(CHIP, "bg-success/15 text-success")}>Free to edit</span>
  return <span className={cn(CHIP, "bg-warn/15 text-warn")}>
    <Lock aria-hidden className="size-2.5" />Editing: {mine ? "you" : holder.display_name}
  </span>
}
