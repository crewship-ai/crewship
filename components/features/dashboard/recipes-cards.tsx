"use client"

import * as React from "react"
import { motion } from "motion/react"
import { ArrowRight, Sparkles } from "lucide-react"
import { CrewIcon } from "@/components/ui/crew-icon"
import { asCrewColor } from "@/components/features/crews/create-crew/types"
import { cn } from "@/lib/utils"
import { RecipeInstallSheet } from "@/components/features/recipes/recipe-install-sheet"
import { apiFetch } from "@/lib/api-fetch"

interface Recipe {
  slug: string
  name: string
  description: string
  icon: string
  color: string
}

interface Props {
  workspaceId: string
  onInstalled?: () => void
}

export function RecipesEmptyState({ workspaceId, onInstalled }: Props) {
  const [recipes, setRecipes] = React.useState<Recipe[]>([])
  const [loading, setLoading] = React.useState(true)
  const [activeSlug, setActiveSlug] = React.useState<string | null>(null)
  const [open, setOpen] = React.useState(false)

  React.useEffect(() => {
    apiFetch("/api/v1/recipes")
      .then((r) => r.ok ? r.json() : [])
      .then((data: Recipe[]) => setRecipes(Array.isArray(data) ? data : []))
      .catch(() => setRecipes([]))
      .finally(() => setLoading(false))
  }, [])

  if (loading || recipes.length === 0) return null

  return (
    <>
      <div className="space-y-4">
        <div className="flex items-center gap-2">
          <Sparkles className="h-4 w-4 text-blue-400" />
          <h2 className="text-sm font-semibold tracking-wider uppercase text-muted-foreground">Get started in one click</h2>
        </div>
        <div className="grid gap-4 grid-cols-1 sm:grid-cols-3">
          {recipes.map((r, idx) => (
            <motion.button
              key={r.slug}
              type="button"
              onClick={() => { setActiveSlug(r.slug); setOpen(true) }}
              initial={{ opacity: 0, y: 8 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.2, delay: idx * 0.05 }}
              className={cn(
                "group flex flex-col items-start gap-3 rounded-xl border bg-card p-5 text-left transition-all",
                "border-white/10 hover:border-blue-400/50 hover:bg-white/[0.02] hover:shadow-lg hover:shadow-blue-500/5",
              )}
            >
              <CrewIcon icon={r.icon} color={asCrewColor(r.color)} size="md" />
              <div className="space-y-1 flex-1">
                <div className="text-sm font-semibold">{r.name}</div>
                <div className="text-xs text-muted-foreground line-clamp-2 leading-relaxed">{r.description}</div>
              </div>
              <span className="text-xs text-blue-400 inline-flex items-center gap-1 mt-1 group-hover:gap-2 transition-all">
                Install <ArrowRight className="h-3 w-3" />
              </span>
            </motion.button>
          ))}
        </div>
      </div>

      <RecipeInstallSheet
        workspaceId={workspaceId}
        recipeSlug={activeSlug}
        open={open}
        onOpenChange={(o) => { setOpen(o); if (!o) setActiveSlug(null) }}
        onInstalled={onInstalled}
      />
    </>
  )
}
