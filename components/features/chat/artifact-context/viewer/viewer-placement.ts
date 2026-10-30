import { cn } from "@/lib/utils"

/**
 * Where the side viewer sits beside the conversation. Expanded, it takes the
 * whole width. Otherwise it shares the row on a wide screen; below 1180px, with
 * chat artifact projects on, it lies over 78% of the chat so the conversation
 * keeps a readable column instead of being squeezed beside it. "Tablet" is
 * below 1180px, as in the Harbor 2 prototype, so a 1024px landscape tablet
 * gets the overlay too.
 */
export function viewerAsideClass({ expanded, overlay }: { expanded: boolean; overlay: boolean }): string {
  if (expanded) return "min-h-0 min-w-0 shrink-0 w-full"
  return cn("min-h-0 min-w-0 shrink-0 w-[45%] min-w-[20rem] max-w-full",
    overlay && "max-[1179px]:absolute max-[1179px]:inset-y-0 max-[1179px]:right-0 max-[1179px]:z-20 max-[1179px]:w-[78%] max-[1179px]:min-w-0 max-[1179px]:border-l max-[1179px]:bg-background max-[1179px]:shadow-xl")
}
