"use client"

import { useEffect, useMemo, useState } from "react"
import { Search, X, ChevronUp, ChevronDown } from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import { useHotkeys } from "react-hotkeys-hook"

import { Button } from "@/components/ui/button"
import { spring } from "@/lib/motion"
import type { ChatTurn } from "@/hooks/use-chat"

interface SearchHit {
  turnId: string
  preview: string
  index: number
}

interface ConversationSearchProps {
  turns: ChatTurn[]
}

export function ConversationSearch({ turns }: ConversationSearchProps) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState("")
  const [cursor, setCursor] = useState(0)

  useHotkeys(
    "mod+f",
    (e) => {
      e.preventDefault()
      setOpen((v) => !v)
    },
    { enableOnFormTags: true, enableOnContentEditable: true },
  )

  useHotkeys(
    "esc",
    () => setOpen(false),
    { enabled: open, enableOnFormTags: true },
    [open],
  )

  const hits = useMemo<SearchHit[]>(() => {
    if (!query.trim()) return []
    const q = query.toLowerCase()
    const out: SearchHit[] = []
    turns.forEach((turn, ti) => {
      const text = turn.parts
        .filter((p) => p.type === "text")
        .map((p) => p.content)
        .join(" ")
      const idx = text.toLowerCase().indexOf(q)
      if (idx >= 0) {
        const start = Math.max(0, idx - 24)
        const end = Math.min(text.length, idx + q.length + 24)
        out.push({
          turnId: turn.id,
          preview: `${start > 0 ? "…" : ""}${text.slice(start, end)}${end < text.length ? "…" : ""}`,
          index: ti,
        })
      }
    })
    return out
  }, [turns, query])

  useEffect(() => {
    if (cursor >= hits.length) setCursor(0)
  }, [hits, cursor])

  const goNext = () => {
    if (!hits.length) return
    setCursor((c) => (c + 1) % hits.length)
  }
  const goPrev = () => {
    if (!hits.length) return
    setCursor((c) => (c - 1 + hits.length) % hits.length)
  }

  useEffect(() => {
    if (!open || !hits.length) return
    if (cursor < 0 || cursor >= hits.length) return
    const hit = hits[cursor]
    const node = document.querySelector(`[data-turn-id="${hit.turnId}"]`)
    node?.scrollIntoView({ behavior: "smooth", block: "center" })
  }, [open, hits, cursor])

  return (
    <AnimatePresence>
      {open && (
        <motion.div
          initial={{ opacity: 0, y: -16 }}
          animate={{ opacity: 1, y: 0 }}
          exit={{ opacity: 0, y: -16 }}
          transition={spring.smooth}
          className="absolute top-2 right-16 z-30 w-[400px] rounded-lg border bg-background shadow-xl"
          role="search"
        >
          <div className="flex items-center gap-2 px-2 py-1.5 border-b">
            <Search className="h-3.5 w-3.5 text-muted-foreground" />
            <input
              type="text"
              aria-label="Search conversation"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search in conversation…"
              autoFocus
              className="flex-1 bg-transparent outline-none text-sm placeholder:text-muted-foreground"
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault()
                  if (e.shiftKey) goPrev()
                  else goNext()
                }
              }}
            />
            <span className="text-xs text-muted-foreground tabular-nums">
              {hits.length === 0 ? "0" : `${cursor + 1}/${hits.length}`}
            </span>
            <Button aria-label="Previous result" size="icon-sm" variant="ghost" onClick={goPrev} disabled={!hits.length}>
              <ChevronUp className="h-3.5 w-3.5" />
            </Button>
            <Button aria-label="Next result" size="icon-sm" variant="ghost" onClick={goNext} disabled={!hits.length}>
              <ChevronDown className="h-3.5 w-3.5" />
            </Button>
            <Button aria-label="Close search" size="icon-sm" variant="ghost" onClick={() => setOpen(false)}>
              <X className="h-3.5 w-3.5" />
            </Button>
          </div>
          {hits.length > 0 && (
            <div className="px-3 py-1.5 text-xs text-muted-foreground border-t bg-muted/20">
              {hits[cursor].preview}
            </div>
          )}
        </motion.div>
      )}
    </AnimatePresence>
  )
}
