import { cn } from "@/lib/utils"

/**
 * Where the side viewer sits beside the conversation. Expanded, it takes the
 * whole width. Otherwise it shares the row on a wide screen; below lg, with
 * chat artifact projects on, it lies over 78% of the chat so the conversation
 * keeps a readable column instead of being squeezed beside it.
 */
export function viewerAsideClass({ expanded, overlay }: { expanded: boolean; overlay: boolean }): string {
  if (expanded) return "min-h-0 min-w-0 shrink-0 w-full"
  return cn("min-h-0 min-w-0 shrink-0 w-[45%] min-w-[20rem] max-w-full",
    overlay && "max-lg:absolute max-lg:inset-y-0 max-lg:right-0 max-lg:z-20 max-lg:w-[78%] max-lg:min-w-0 max-lg:border-l max-lg:bg-background max-lg:shadow-xl")
}
