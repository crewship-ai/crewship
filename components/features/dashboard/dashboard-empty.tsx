"use client"

import * as React from "react"
import { LayoutDashboard } from "lucide-react"

import { PageEmpty } from "@/components/layout/page-state"
import { RecipeInstallSheet } from "@/components/features/recipes/recipe-install-sheet"
import { apiFetch } from "@/lib/api-fetch"

interface RecipeRef {
  slug: string
}

/**
 * A workspace without crews: one empty state instead of a page of empty
 * cards. "See an example" opens the first crew recipe; with no recipes on
 * this server the button is not offered.
 */
export function DashboardEmpty({ workspaceId, onInstalled }: { workspaceId: string; onInstalled?: () => void }) {
  const [example, setExample] = React.useState<string | null>(null)
  const [open, setOpen] = React.useState(false)

  React.useEffect(() => {
    let live = true
    apiFetch("/api/v1/recipes")
      .then((r) => (r.ok ? r.json() : []))
      .then((data: RecipeRef[]) => {
        if (live) setExample(Array.isArray(data) && data[0]?.slug ? data[0].slug : null)
      })
      .catch(() => {
        if (live) setExample(null)
      })
    return () => {
      live = false
    }
  }, [])

  return (
    <>
      <PageEmpty
        icon={LayoutDashboard}
        title="Your workspace is ready"
        description="Add a crew and a model account; the dashboard fills in as agents work."
        primary={{ label: "New crew", href: "/crews?new=crew" }}
        secondary={example ? { label: "See an example", onClick: () => setOpen(true) } : undefined}
        cli="crewship crew create"
      />
      {example && (
        <RecipeInstallSheet
          workspaceId={workspaceId}
          recipeSlug={open ? example : null}
          open={open}
          onOpenChange={setOpen}
          onInstalled={onInstalled}
        />
      )}
    </>
  )
}
