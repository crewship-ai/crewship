"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Bot, AtSign } from "lucide-react"

import { cn } from "@/lib/utils"
import { spring } from "@/lib/motion"

export interface CrewMember {
  id: string
  slug: string
  name: string
  role_title?: string
  description?: string
}

interface MentionAutocompleteProps {
  text: string
  textareaRef: React.RefObject<HTMLTextAreaElement | null>
  members: CrewMember[]
  onPick: (member: CrewMember, atIndex: number) => void
}

interface MentionTrigger {
  start: number
  query: string
}

function detectMention(text: string, caret: number): MentionTrigger | null {
  if (caret <= 0) return null
  let i = caret - 1
  while (i >= 0 && /[a-zA-Z0-9_-]/.test(text[i])) i--
  if (i < 0 || text[i] !== "@") return null
  if (i > 0 && /\S/.test(text[i - 1])) return null
  return { start: i, query: text.slice(i + 1, caret).toLowerCase() }
}

export function MentionAutocomplete({ text: _text, textareaRef, members, onPick }: MentionAutocompleteProps) {
  const [trigger, setTrigger] = useState<MentionTrigger | null>(null)
  const [highlighted, setHighlighted] = useState(0)
  const popoverRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const ta = textareaRef.current
    if (!ta) return
    const handler = () => {
      const t = detectMention(ta.value, ta.selectionStart ?? 0)
      setTrigger(t)
      setHighlighted(0)
    }
    ta.addEventListener("input", handler)
    ta.addEventListener("click", handler)
    ta.addEventListener("keyup", handler)
    return () => {
      ta.removeEventListener("input", handler)
      ta.removeEventListener("click", handler)
      ta.removeEventListener("keyup", handler)
    }
  }, [textareaRef])

  const matches = useMemo(() => {
    if (!trigger) return [] as CrewMember[]
    const q = trigger.query
    if (!q) return members.slice(0, 6)
    return members
      .filter((m) =>
        m.slug.toLowerCase().includes(q) || m.name.toLowerCase().includes(q),
      )
      .slice(0, 6)
  }, [trigger, members])

  useEffect(() => {
    if (!trigger) return
    const ta = textareaRef.current
    if (!ta) return
    const onKey = (e: KeyboardEvent) => {
      if (!matches.length) return
      if (e.key === "ArrowDown") {
        e.preventDefault()
        setHighlighted((h) => (h + 1) % matches.length)
      } else if (e.key === "ArrowUp") {
        e.preventDefault()
        setHighlighted((h) => (h - 1 + matches.length) % matches.length)
      } else if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault()
        onPick(matches[highlighted], trigger.start)
        setTrigger(null)
      } else if (e.key === "Escape") {
        setTrigger(null)
      }
    }
    ta.addEventListener("keydown", onKey)
    return () => ta.removeEventListener("keydown", onKey)
  }, [trigger, matches, highlighted, onPick, textareaRef])

  if (!trigger || !matches.length) return null

  return (
    <AnimatePresence>
      <motion.div
        ref={popoverRef}
        initial={{ opacity: 0, y: 4, scale: 0.97 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        exit={{ opacity: 0, y: 4, scale: 0.97 }}
        transition={spring.snappy}
        className="absolute bottom-full mb-2 left-2 right-2 z-30 max-w-md rounded-lg border bg-popover shadow-lg overflow-hidden"
      >
        <div className="flex items-center gap-1.5 px-3 py-1.5 border-b text-xs text-muted-foreground">
          <AtSign className="h-3 w-3" />
          <span>Mention an agent</span>
        </div>
        <ul className="max-h-64 overflow-y-auto py-1">
          {matches.map((m, i) => (
            <li key={m.id}>
              <button
                type="button"
                onMouseEnter={() => setHighlighted(i)}
                onClick={() => {
                  onPick(m, trigger.start)
                  setTrigger(null)
                }}
                className={cn(
                  "flex w-full items-start gap-2 px-3 py-1.5 text-left text-sm",
                  i === highlighted && "bg-accent",
                )}
              >
                <Bot className="h-4 w-4 text-muted-foreground shrink-0 mt-0.5" />
                <div className="flex flex-col min-w-0">
                  <span className="font-medium truncate">
                    @{m.slug} <span className="text-muted-foreground font-normal">· {m.name}</span>
                  </span>
                  {m.role_title && (
                    <span className="text-xs text-muted-foreground truncate">{m.role_title}</span>
                  )}
                </div>
              </button>
            </li>
          ))}
        </ul>
      </motion.div>
    </AnimatePresence>
  )
}
