/**
 * Class Name Utilities
 * Tailwind CSS class merging with clsx
 */

import { type ClassValue, clsx } from "clsx"
import { extendTailwindMerge } from "tailwind-merge"

/**
 * tailwind-merge only knows Tailwind's stock scale. Our typography scale lives
 * in app/globals.css (--text-micro … --text-display), so `text-label` looked to
 * it like an unknown `text-*` — which it files under *colour*. The moment a
 * real colour followed in the same call, the size was dropped as a conflict:
 *
 *   cn("px-2 text-label text-foreground")  ->  "px-2 text-foreground"
 *
 * No error, no warning: the class simply never reached the DOM and the element
 * inherited 16px. That is how the agent config screen ended up rendering its
 * values and dropdowns at 16px beside 12px labels. 504 uses across 82 files
 * were sitting on the same trapdoor, and the detail kit had already routed
 * around it by writing `text-[0.6875rem]` instead of a role name.
 *
 * Declaring the scale as a font-size group fixes the classification: sizes now
 * conflict with sizes, colours with colours.
 */
const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": [
        { text: ["micro", "label", "body", "default", "heading", "title", "display"] },
      ],
    },
  },
})

/**
 * Merge Tailwind CSS classes with proper handling of conflicts
 *
 * @example
 * cn('px-2 py-1', 'px-4') // 'py-1 px-4' (px-4 overrides px-2)
 * cn('text-red-500', condition && 'text-blue-500')
 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
