"use client"

import { useCallback, useEffect, useState } from "react"
import { ChevronDown, ChevronUp } from "lucide-react"

import { Button } from "@/components/ui/button"

export function useListScroll() {
  const [list, setList] = useState<HTMLDivElement | null>(null)
  const listRef = useCallback((node: HTMLDivElement | null) => setList(node), [])
  const [position, setPosition] = useState({ up: false, down: false })

  const measure = useCallback(() => {
    if (!list) {
      setPosition((current) => current.up || current.down ? { up: false, down: false } : current)
      return
    }
    const up = list.scrollTop > 1
    const down = list.scrollTop + list.clientHeight < list.scrollHeight - 1
    setPosition((current) => current.up === up && current.down === down ? current : { up, down })
  }, [list])

  useEffect(() => {
    if (!list) {
      measure()
      return
    }
    const resize = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure)
    resize?.observe(list)
    const mutations = typeof MutationObserver === "undefined" ? null : new MutationObserver(measure)
    mutations?.observe(list, { childList: true, subtree: true })
    list.addEventListener("scroll", measure, { passive: true })
    window.addEventListener("resize", measure)
    measure()
    return () => {
      resize?.disconnect()
      mutations?.disconnect()
      list.removeEventListener("scroll", measure)
      window.removeEventListener("resize", measure)
    }
  }, [list, measure])

  const scroll = useCallback((direction: -1 | 1) => {
    if (!list) return
    list.scrollBy({ top: direction * Math.max(120, list.clientHeight * 0.7), behavior: "smooth" })
  }, [list])

  return { listRef, ...position, scroll }
}

export function ListScrollControls({ label, controller }: {
  label: string
  controller: ReturnType<typeof useListScroll>
}) {
  if (!controller.up && !controller.down) return null
  return (
    <span className="hidden items-center gap-0.5 sm:inline-flex" role="group" aria-label={`${label} scroll controls`}>
      <Button type="button" variant="ghost" size="icon-xs" disabled={!controller.up} onClick={() => controller.scroll(-1)} aria-label={`Scroll ${label} up`} title="Scroll up" className="border border-border/60 text-muted-foreground hover:text-foreground">
        <ChevronUp aria-hidden="true" />
      </Button>
      <Button type="button" variant="ghost" size="icon-xs" disabled={!controller.down} onClick={() => controller.scroll(1)} aria-label={`Scroll ${label} down`} title="Scroll down" className="border border-border/60 text-muted-foreground hover:text-foreground">
        <ChevronDown aria-hidden="true" />
      </Button>
    </span>
  )
}
