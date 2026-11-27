import { cn } from "@/lib/utils"

/**
 * Where the side viewer sits beside the conversation. Expanded, it takes the
 * whole width. Otherwise it shares the row on a wide screen; below lg, with
 * chat artifact projects on, it lies over 78% of the chat so the conversation
 * keeps a readable column instead of being squeezed beside it.
 */
export function viewerAsideClass({ expanded, overlay, beside = false }: { expanded: boolean; overlay: boolean; /** An expanded Sheet beside the narrow chat. */ beside?: boolean }): string {
  if (expanded && beside) return "min-h-0 min-w-0 flex-1"
  if (expanded) return "min-h-0 min-w-0 shrink-0 w-full"
  return cn("min-h-0 min-w-0 shrink-0 w-[var(--chat-panel-width,45%)] min-w-[20rem] max-w-full",
    overlay && "max-lg:absolute max-lg:inset-y-0 max-lg:right-0 max-lg:z-20 max-lg:w-[78%] max-lg:min-w-0 max-lg:border-l max-lg:bg-background max-lg:shadow-xl")
}

/** The conversation column: the whole row, a narrow column beside an expanded Sheet, or folded. */
export function chatColumnClass({ hidden, narrow }: { hidden: boolean; narrow: boolean }): string {
  return cn("flex flex-col overflow-hidden min-w-0", hidden ? "hidden" : narrow ? "w-[22rem] shrink-0" : "flex-1")
}
