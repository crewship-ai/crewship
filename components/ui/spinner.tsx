import { Loader2Icon } from "lucide-react"

import { cn } from "@/lib/utils"

// Decorative by default: a spinner almost always sits next to visible text
// (and is frequently nested inside an existing role="status"/aria-live
// region), so it must not announce on its own. Callers that render a
// standalone, text-less spinner can opt back in by passing
// role="status" aria-label="…" aria-hidden={false}.
function Spinner({ className, ...props }: React.ComponentProps<"svg">) {
  return (
    <Loader2Icon
      aria-hidden="true"
      className={cn("size-4 animate-spin", className)}
      {...props}
    />
  )
}

export { Spinner }
