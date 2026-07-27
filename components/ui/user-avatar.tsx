"use client"

import { useState } from "react"

import { cn } from "@/lib/utils"

/**
 * A person's face, wherever Crewship lists people.
 *
 * Extracted because three surfaces drew the same thing three ways — the top
 * bar, the Settings member roster and the capability grid — and only one of
 * them had been taught about `avatar_url`. Uploading a photo changed the top
 * bar and nothing else, so the same person could appear twice on one screen
 * with two different faces.
 *
 * A plain <img> rather than the Radix Avatar: that one only swaps the image
 * in after a load event, so every render starts on the fallback and the
 * initials visibly flash before the photo appears. Here the fallback is a
 * genuine fallback — no src, or the image failed to load.
 *
 * The src is a same-origin authed endpoint, so the session cookie rides
 * along automatically, and it carries a ?v=<unix> stamp the upload handler
 * bumps, which is what makes a re-upload beat the browser cache.
 */

/** Two letters from a name, or from the email when the name is not set yet
 *  — provisioned accounts have no full_name until the person picks one. */
export function personInitials(name: string | null | undefined, email: string): string {
  const trimmed = (name ?? "").trim()
  if (trimmed) {
    const parts = trimmed.split(/\s+/)
    if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
    return trimmed.slice(0, 2).toUpperCase()
  }
  return email.slice(0, 2).toUpperCase()
}

export function UserAvatar({
  name,
  email,
  src,
  className,
  textClassName,
}: {
  name?: string | null
  email: string
  src?: string | null
  /** Sizing lives with the caller: 24px in a dense roster, 40px in a menu. */
  className?: string
  textClassName?: string
}) {
  const [broken, setBroken] = useState(false)
  // The person, not the file — a screen-reader user scanning a member list
  // needs to know who this is.
  const label = (name ?? "").trim() || email

  if (!src || broken) {
    return (
      <span
        className={cn(
          "flex shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground",
          className ?? "h-6 w-6",
        )}
      >
        <span className={cn("font-semibold leading-none", textClassName ?? "text-[10px]")}>
          {personInitials(name, email)}
        </span>
      </span>
    )
  }

  return (
    <img
      src={src}
      alt={label}
      onError={() => setBroken(true)}
      className={cn("shrink-0 rounded-full object-cover", className ?? "h-6 w-6")}
    />
  )
}
