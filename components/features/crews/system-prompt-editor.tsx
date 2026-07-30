"use client"

import { useEffect, useRef, useState } from "react"
import { FileText } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard } from "@/components/ui/detail"
import { cn } from "@/lib/utils"

export interface SystemPromptEditorProps {
  value: string | null | undefined
  /** Persist on Save click. Throw to surface error inline. */
  onSave: (next: string) => void | Promise<void>
  /** Last-updated timestamp text (e.g. "updated 4d ago"). */
  updatedHint?: string
  /** Read-only when true (e.g. user lacks permission). */
  readOnly?: boolean
}

/**
 * System prompt editor with explicit Save / Cancel — never blur-saves.
 * The system prompt is the highest-stakes field per agent (typically
 * 800+ chars of behavioral spec). A blur-save would silently overwrite
 * it on accidental focus changes; we require an explicit click.
 *
 * Cmd/Ctrl+S inside the textarea triggers Save. Esc cancels.
 */
export function SystemPromptEditor({
  value,
  onSave,
  updatedHint,
  readOnly = false,
}: SystemPromptEditorProps) {
  const [draft, setDraft] = useState(value ?? "")
  const [editing, setEditing] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const taRef = useRef<HTMLTextAreaElement | null>(null)

  // Re-sync when parent updates the prop (e.g. WebSocket external change)
  // — but ONLY when not editing, to avoid clobbering the user's draft.
  useEffect(() => {
    if (!editing) setDraft(value ?? "")
  }, [value, editing])

  useEffect(() => {
    if (editing && taRef.current) {
      taRef.current.focus()
      taRef.current.setSelectionRange(taRef.current.value.length, taRef.current.value.length)
    }
  }, [editing])

  // Derive dirty from the live prop, not a render-once `initial`. Keeps
  // the indicator honest after parent re-fetches push a new value while
  // the user happens to have an open draft equal to the persisted state.
  const dirty = draft !== (value ?? "")

  const handleSave = async () => {
    if (!dirty) {
      setEditing(false)
      return
    }
    setSaving(true)
    setError(null)
    try {
      await onSave(draft)
      setEditing(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const handleCancel = () => {
    setDraft(value ?? "")
    setError(null)
    setEditing(false)
  }

  const charCount = (value ?? "").length

  return (
    <DetailCard
      bare
      icon={FileText}
      title="System prompt"
      subtitle="system_prompt.md"
      tone={dirty && editing ? "warn" : "default"}
      footer={`${charCount} chars${updatedHint ? ` · ${updatedHint}` : ""}`}
      action={
        readOnly ? null : editing ? (
          <span className="flex items-center gap-1.5">
            {dirty && <span className="h-1.5 w-1.5 rounded-full bg-warn" title="Unsaved changes" />}
            <Button variant="ghost" size="xs" onClick={handleCancel} disabled={saving}>
              Cancel
            </Button>
            <Button variant="soft" size="xs" onClick={handleSave} disabled={!dirty || saving}>
              {saving ? "Saving…" : "Save"}
            </Button>
          </span>
        ) : (
          <Button variant="outline" size="xs" onClick={() => setEditing(true)}>
            Edit
          </Button>
        )
      }
    >
      {editing ? (
        <textarea
          ref={taRef}
          value={draft}
          aria-label="System prompt"
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "s") {
              e.preventDefault()
              void handleSave()
            } else if (e.key === "Escape") {
              e.preventDefault()
              handleCancel()
            }
          }}
          spellCheck={false}
          className={cn(
            "type-row min-h-[260px] w-full resize-y bg-transparent px-4 py-3",
            "font-mono leading-relaxed text-foreground outline-none",
          )}
        />
      ) : (
        <pre className="type-row max-h-[260px] overflow-y-auto px-4 py-3 font-mono leading-relaxed text-foreground/85 whitespace-pre-wrap">
          {value || (
            <em className="not-italic text-muted-foreground">
              empty — click Edit to write a system prompt
            </em>
          )}
        </pre>
      )}

      {error && (
        <p className="type-meta border-t border-destructive/20 bg-destructive/5 px-4 py-2 text-destructive">
          Save failed: {error}
        </p>
      )}
    </DetailCard>
  )
}
