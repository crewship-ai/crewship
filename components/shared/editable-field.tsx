"use client"

import { useEffect, useRef, useState } from "react"
import { Check, ChevronDown, Pencil } from "lucide-react"
import { cn } from "@/lib/utils"

export interface EditableFieldProps {
  /** Current value as displayed when not editing. */
  value: string | null | undefined
  /** Called on commit (blur for text, change for select). Throw to surface an error. */
  onSave: (next: string) => void | Promise<void>
  /** Optional select options. When provided, renders a dropdown instead of a text input. */
  options?: ReadonlyArray<{ value: string; label: string }>
  /** Placeholder rendered when value is empty. */
  placeholder?: string
  /** Render value as monospace code (e.g. for slugs). */
  mono?: boolean
  /** Disable editing affordances entirely. */
  readOnly?: boolean
  /** Optional className for the outer container. */
  className?: string
  /** Optional formatter for the displayed value. */
  format?: (v: string) => React.ReactNode
}

/**
 * Click-to-edit field with optimistic update + inline checkmark feedback.
 *
 * Behavior:
 * - Click row → input/select activates.
 * - Text: blur or Enter commits, Escape reverts.
 * - Select: change commits immediately.
 * - Successful save shows a green check that fades after ~800ms.
 * - On error, value reverts to the prior committed state and a red dot
 *   tooltip surfaces the message (parent should toast separately if needed).
 *
 * Used by Profile / Runtime sections in agent-canvas + crew-canvas.
 */
export function EditableField({
  value,
  onSave,
  options,
  placeholder = "empty — click to add",
  mono = false,
  readOnly = false,
  className,
  format,
}: EditableFieldProps) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(value ?? "")
  const [savedFlash, setSavedFlash] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement | null>(null)

  useEffect(() => {
    if (!editing) setDraft(value ?? "")
  }, [value, editing])

  useEffect(() => {
    if (editing && inputRef.current) inputRef.current.focus()
  }, [editing])

  const commit = async (next: string) => {
    if (next === (value ?? "")) {
      setEditing(false)
      return
    }
    try {
      setError(null)
      await onSave(next)
      setSavedFlash(true)
      setTimeout(() => setSavedFlash(false), 800)
      setEditing(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setDraft(value ?? "")
      setEditing(false)
    }
  }

  if (options) {
    // Select variant — commits immediately on change
    return (
      <div className={cn("flex items-center gap-2", className)}>
        {readOnly ? (
          <span className="text-sm text-foreground/90">
            {format ? format(value ?? "") : (value ?? <em className="text-muted-foreground italic">{placeholder}</em>)}
          </span>
        ) : (
          <div className="relative inline-flex items-center">
            <select
              value={value ?? ""}
              onChange={(e) => commit(e.target.value)}
              className="appearance-none bg-transparent border border-transparent hover:border-white/10 rounded px-2 pr-6 py-0.5 text-sm text-foreground/90 cursor-pointer focus:outline-none focus:border-white/15"
            >
              {options.map((opt) => (
                <option key={opt.value} value={opt.value} className="bg-zinc-900">
                  {opt.label}
                </option>
              ))}
            </select>
            <ChevronDown className="absolute right-1 h-3 w-3 text-muted-foreground pointer-events-none" />
          </div>
        )}
        {savedFlash && <Check className="h-3 w-3 text-emerald-400" />}
        {error && <span className="text-[11px] text-red-400" title={error}>!</span>}
      </div>
    )
  }

  // Text variant
  return (
    <div className={cn("flex items-center gap-2 group", className)}>
      {editing ? (
        <input
          ref={inputRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => commit(draft)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault()
              ;(e.target as HTMLInputElement).blur()
            } else if (e.key === "Escape") {
              setDraft(value ?? "")
              setEditing(false)
            }
          }}
          className={cn(
            "flex-1 bg-transparent border border-white/15 rounded px-2 py-0.5 text-sm text-foreground outline-none focus:border-blue-400",
            mono && "font-mono",
          )}
        />
      ) : (
        <button
          type="button"
          disabled={readOnly}
          onClick={() => setEditing(true)}
          className={cn(
            "flex-1 text-left text-sm rounded px-2 py-0.5 -mx-2 transition-colors",
            !readOnly && "hover:bg-white/5 cursor-text",
            readOnly && "cursor-default",
          )}
        >
          {value ? (
            mono ? (
              <code className="font-mono text-foreground/90">{value}</code>
            ) : (
              <span className="text-foreground/90">{format ? format(value) : value}</span>
            )
          ) : (
            <em className="text-muted-foreground italic">{placeholder}</em>
          )}
        </button>
      )}
      {!editing && !readOnly && (
        <Pencil className="h-3 w-3 text-muted-foreground opacity-0 group-hover:opacity-100 transition-opacity" />
      )}
      {savedFlash && <Check className="h-3 w-3 text-emerald-400" />}
      {error && (
        <span className="text-[11px] text-red-400" title={error}>
          !
        </span>
      )}
    </div>
  )
}
