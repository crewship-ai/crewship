"use client"

import { useEffect, useRef, useState } from "react"
import { motion } from "motion/react"
import { Pencil, Check, X } from "lucide-react"

import { Action, Actions } from "@/components/ai-elements/actions"
import { Button } from "@/components/ui/button"
import { spring } from "@/lib/motion"
import { cn } from "@/lib/utils"

interface EditableUserMessageProps {
  text: string
  timestamp: Date
  onSave: (next: string) => void
  className?: string
}

export function EditableUserMessage({
  text,
  timestamp,
  onSave,
  className,
}: EditableUserMessageProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(text)
  const taRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => setDraft(text), [text])

  useEffect(() => {
    if (editing) {
      const ta = taRef.current
      if (ta) {
        ta.focus()
        ta.setSelectionRange(ta.value.length, ta.value.length)
      }
    }
  }, [editing])

  if (editing) {
    return (
      <motion.div
        initial={{ opacity: 0, scale: 0.98 }}
        animate={{ opacity: 1, scale: 1 }}
        transition={spring.snappy}
        className="flex flex-col gap-2 rounded-2xl bg-blue-500/10 border border-blue-400/20 shadow-sm px-4 py-3 ml-auto max-w-[80%] w-full"
      >
        <textarea
          ref={taRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault()
              if (draft.trim()) {
                onSave(draft.trim())
                setEditing(false)
              }
            } else if (e.key === "Escape") {
              setEditing(false)
              setDraft(text)
            }
          }}
          rows={Math.min(8, Math.max(2, draft.split("\n").length))}
          className="bg-transparent outline-none resize-none text-sm"
        />
        <div className="flex items-center justify-end gap-2">
          <span className="text-[10px] text-muted-foreground mr-auto">
            ⌘↩ to send · Esc to cancel
          </span>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setEditing(false)
              setDraft(text)
            }}
            className="h-7 gap-1"
          >
            <X className="h-3 w-3" />
            Cancel
          </Button>
          <Button
            size="sm"
            onClick={() => {
              if (draft.trim()) {
                onSave(draft.trim())
                setEditing(false)
              }
            }}
            disabled={!draft.trim() || draft === text}
            className="h-7 gap-1"
          >
            <Check className="h-3 w-3" />
            Save & resend
          </Button>
        </div>
      </motion.div>
    )
  }

  return (
    <div className={cn("flex flex-col gap-1 ml-auto max-w-[80%]", className)}>
      <div className="rounded-2xl rounded-br-sm bg-blue-500/10 border border-blue-400/20 shadow-sm px-4 py-3 text-sm">
        {text}
      </div>
      <div className="flex items-center justify-end gap-2 opacity-0 group-hover:opacity-100 transition-opacity">
        <span className="text-[10px] text-muted-foreground">
          {timestamp.toLocaleTimeString("en-GB", {
            hour: "2-digit",
            minute: "2-digit",
          })}
        </span>
        <Actions className="opacity-100">
          <Action
            tooltip="Edit & resend"
            onClick={() => setEditing(true)}
          >
            <Pencil className="h-3.5 w-3.5" />
          </Action>
        </Actions>
      </div>
    </div>
  )
}
