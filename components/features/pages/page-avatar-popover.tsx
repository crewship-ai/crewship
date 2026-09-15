"use client"

import * as React from "react"
import { toast } from "sonner"

import { CrewIconPopover } from "@/components/crew-icon-popover"
import { PageAvatar } from "@/components/features/pages/page-glyph"
import { pagesKeys } from "@/hooks/use-pages"
import { pageQueryString } from "@/hooks/use-page-grants"
import { ApiMutationError, useApiMutation } from "@/hooks/use-api-mutation"

/**
 * The page's avatar tile as a control (#2563): click it, pick an icon or a
 * colour, and it is saved on the spot — the way a routine's identity header
 * works (`routine-identity-header.tsx`), and for the same reason: changing
 * an icon is a two-second act, and a form with a Save button under it is
 * the wrong shape for that.
 *
 * Each pick is ONE `PATCH /pages/{slug}` carrying only the field that
 * changed, so the name, the description and the panels are untouched
 * whatever the Content card holds unsaved. Optimistic: the tile shows the
 * pick at once and goes back on a refusal, with the server's sentence in a
 * toast. The detail and the list are invalidated on success, which is what
 * makes the rail redraw without a reload.
 *
 * A viewer who may not edit the metadata gets the plain tile: the same
 * pixels, not a button that refuses.
 */
export interface PageAvatarPopoverProps {
  workspaceId: string
  slug: string
  icon: string | null | undefined
  color: string | null | undefined
  mayEdit: boolean
  size?: "sm" | "md"
  className?: string
}

export function PageAvatarPopover(props: PageAvatarPopoverProps) {
  // The plain tile mounts no mutation and needs no query client: a read-only
  // page view is rendered in places that have neither.
  if (!props.mayEdit) {
    return <PageAvatar icon={props.icon} color={props.color} size={props.size ?? "md"} className={props.className} />
  }
  return <EditablePageAvatar {...props} />
}

function EditablePageAvatar({
  workspaceId,
  slug,
  icon,
  color,
  size = "md",
  className,
}: PageAvatarPopoverProps) {
  const [shown, setShown] = React.useState({ icon: icon ?? null, color: color ?? null })
  // A later read of the page (somebody else's save, the Content card's own)
  // wins over what was shown, exactly like the routine header.
  React.useEffect(() => {
    setShown({ icon: icon ?? null, color: color ?? null })
  }, [icon, color, slug])

  const save = useApiMutation<{ icon?: string; color?: string; previous: typeof shown }>({
    request: (v) => ({
      input: `/api/v1/pages/${encodeURIComponent(slug)}${pageQueryString(workspaceId)}`,
      init: {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(v.icon !== undefined ? { icon: v.icon } : { color: v.color }),
      },
    }),
    invalidateKeys: [pagesKeys.detail(workspaceId, slug), pagesKeys.list(workspaceId)],
    onAlreadyRunning: (outcome) => toast.error(outcome.message),
    onError: (err, v) => {
      setShown(v.previous)
      toast.error(
        err instanceof ApiMutationError
          ? err.message
          : err instanceof Error
            ? `Could not save the icon: ${err.message}`
            : "Could not save the icon",
      )
    },
  })

  const tile = (
    <PageAvatar icon={shown.icon} color={shown.color} size={size} className={className} />
  )

  return (
    <div
      data-slot="page-avatar-popover"
      aria-busy={save.isPending}
      className={save.isPending ? "pointer-events-none opacity-70" : undefined}
    >
      <CrewIconPopover
        ariaLabel="Change page icon"
        // The popover's own preview needs a crew icon to draw; a page with
        // none previews as the dashboard glyph in the default palette. The
        // TILE — the trigger — keeps drawing the neutral default.
        icon={shown.icon ?? "dashboard"}
        color={shown.color ?? "blue"}
        size={size}
        trigger={tile}
        onIconChange={(next) => {
          const previous = shown
          setShown({ ...shown, icon: next })
          save.mutate({ icon: next, previous })
        }}
        onColorChange={(next) => {
          const previous = shown
          setShown({ ...shown, color: next })
          save.mutate({ color: next, previous })
        }}
      />
    </div>
  )
}
