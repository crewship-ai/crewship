"use client"

import { useEffect, useState } from "react"
import { Star } from "lucide-react"
import { cn } from "@/lib/utils"

/** Browser preference only; IDs never grant access or cache conversation content. */
export function useChatFavorites(scope: string) {
  const key = `${scope}:favorites:v1`
  const [favorites, setFavorites] = useState<string[]>([])
  useEffect(() => {
    try {
      const saved: unknown = JSON.parse(localStorage.getItem(key) || "[]")
      setFavorites(Array.isArray(saved) ? saved.filter((id): id is string => typeof id === "string") : [])
    } catch { setFavorites([]) }
  }, [key])
  function toggle(id: string) {
    setFavorites((old) => {
      const next = old.includes(id) ? old.filter((value) => value !== id) : [...old, id]
      try { localStorage.setItem(key, JSON.stringify(next)) } catch { /* Works for this visit when storage is unavailable. */ }
      return next
    })
  }
  return { favorites, toggle }
}

export function FavoriteButton({ name, active, onClick }: { name: string; active: boolean; onClick: () => void }) {
  return <button type="button" aria-label={`${active ? "Unpin" : "Pin"} ${name}`} aria-pressed={active} title={`${active ? "Remove from" : "Add to"} Favorites`} onClick={onClick} className={cn("kit-tap flex size-7 coarse:size-12 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent", active && "text-gold")}><Star aria-hidden className={cn("size-3", active && "fill-current")} /></button>
}
