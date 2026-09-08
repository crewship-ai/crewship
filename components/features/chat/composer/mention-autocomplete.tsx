"use client"

import { useEffect, useId, useMemo, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { Bot, AtSign, User } from "lucide-react"

import { cn } from "@/lib/utils"
import { spring } from "@/lib/motion"

export interface CrewMember {
  id: string
  slug: string
  name: string
  role_title?: string
  description?: string
  kind?: "agent" | "human"
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

export function MentionAutocomplete({ text, textareaRef, members, onPick }: MentionAutocompleteProps) {
  const [trigger, setTrigger] = useState<MentionTrigger | null>(null)
  const [highlighted, setHighlighted] = useState(0)
  const listId = useId()

  useEffect(() => {
    const ta = textareaRef.current
    if (!ta) return
    const handler = () => {
      const t = detectMention(ta.value, ta.selectionStart ?? 0)
      setTrigger(t)
      setHighlighted(0)
    }
    // Input already handles typing. Only caret movement needs keyup; running
    // it for picker navigation resets ArrowDown and reopens after Escape.
    const onKeyUp = (event: KeyboardEvent) => {
      if (["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) handler()
    }
    ta.addEventListener("input", handler)
    ta.addEventListener("click", handler)
    ta.addEventListener("keyup", onKeyUp)
    return () => {
      ta.removeEventListener("input", handler)
      ta.removeEventListener("click", handler)
      ta.removeEventListener("keyup", onKeyUp)
    }
  }, [textareaRef])

  // Controlled updates (send clears, prefill, restored drafts) do not emit
  // native input events. Reconcile an existing picker, never reopen one that
  // Escape or a successful selection already dismissed.
  useEffect(() => {
    setTrigger((current) => {
      if (!current) return null
      const next = detectMention(text, textareaRef.current?.selectionStart ?? text.length)
      if (!next || next.start !== current.start) return null
      return next.query === current.query ? current : next
    })
  }, [text, textareaRef])

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

  const selected = Math.min(highlighted, Math.max(0, matches.length - 1))
  useEffect(() => {
    const ta = textareaRef.current
    if (!ta || !trigger || !matches.length) return
    ta.setAttribute("aria-autocomplete", "list")
    ta.setAttribute("aria-controls", listId)
    ta.setAttribute("aria-activedescendant", `${listId}-${selected}`)
    return () => {
      ta.removeAttribute("aria-autocomplete")
      ta.removeAttribute("aria-controls")
      ta.removeAttribute("aria-activedescendant")
    }
  }, [textareaRef, trigger, matches.length, listId, selected])

  useEffect(() => {
    if (!trigger) return
    const ta = textareaRef.current
    if (!ta) return
    const onKey = (e: KeyboardEvent) => {
      if (!matches.length || e.isComposing) return
      if (["ArrowDown", "ArrowUp", "Enter", "Tab", "Escape"].includes(e.key)) {
        e.preventDefault()
        // The composer also handles Enter. Picking a mention must never send
        // the unfinished message through its React key handler.
        e.stopPropagation()
      }
      if (e.key === "ArrowDown") {
        e.preventDefault()
        setHighlighted((h) => (h + 1) % matches.length)
      } else if (e.key === "ArrowUp") {
        e.preventDefault()
        setHighlighted((h) => (h - 1 + matches.length) % matches.length)
      } else if (e.key === "Enter" || e.key === "Tab") {
        e.preventDefault()
        onPick(matches[selected], trigger.start)
        setTrigger(null)
      } else if (e.key === "Escape") {
        setTrigger(null)
      }
    }
    ta.addEventListener("keydown", onKey)
    return () => ta.removeEventListener("keydown", onKey)
  }, [trigger, matches, selected, onPick, textareaRef])

  if (!trigger || !matches.length) return null

  return (
    <AnimatePresence>
      <motion.div
        initial={{ opacity: 0, y: 4, scale: 0.97 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        exit={{ opacity: 0, y: 4, scale: 0.97 }}
        transition={spring.snappy}
        className="absolute bottom-full mb-2 left-2 right-2 z-30 max-w-md rounded-lg border bg-popover shadow-lg overflow-hidden"
      >
        <div className="flex items-center gap-1.5 px-3 py-1.5 border-b text-xs text-muted-foreground">
          <AtSign className="h-3 w-3" />
          <span>{members.some((m) => m.kind === "human") ? "Mention a participant" : "Mention an agent"}</span>
        </div>
        <ul id={listId} role="listbox" aria-label="Mention suggestions" className="max-h-64 overflow-y-auto py-1">
          {matches.map((m, i) => (
            <li key={m.id} role="presentation">
              <button
                id={`${listId}-${i}`}
                type="button"
                role="option"
                aria-selected={i === selected}
                tabIndex={-1}
                onMouseDown={(event) => event.preventDefault()}
                onMouseEnter={() => setHighlighted(i)}
                onClick={() => {
                  onPick(m, trigger.start)
                  setTrigger(null)
                }}
                className={cn(
                  "flex w-full items-start gap-2 px-3 py-1.5 text-left text-sm",
                  i === selected && "bg-accent",
                )}
              >
                {m.kind === "human" ? <User className="h-4 w-4 text-muted-foreground shrink-0 mt-0.5" /> : <Bot className="h-4 w-4 text-muted-foreground shrink-0 mt-0.5" />}
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
