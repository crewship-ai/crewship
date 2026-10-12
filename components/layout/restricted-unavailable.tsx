"use client"

import Link from "next/link"
import { Lock } from "lucide-react"

import { Button } from "@/components/ui/button"

/**
 * What a restricted session sees at a screen its access does not include
 * (#2861): a direct URL, an old bookmark, a link someone shared. The page
 * itself never mounts, so none of its requests are sent.
 */
export function RestrictedUnavailable({ home }: { home: string | null }) {
  return (
    <div role="status" className="mx-auto flex max-w-md flex-col items-center gap-3 px-4 py-16 text-center">
      <Lock className="h-6 w-6 text-muted-foreground" aria-hidden />
      <h1 className="text-base font-semibold">Not available with your access</h1>
      <p className="text-sm text-muted-foreground">
        This screen is not part of your access in this workspace. Ask an administrator if you need it.
      </p>
      {home && (
        <Button asChild variant="outline" size="sm">
          <Link href={home}>Go to {home === "/chat" ? "Chat" : "your workspace"}</Link>
        </Button>
      )}
    </div>
  )
}
