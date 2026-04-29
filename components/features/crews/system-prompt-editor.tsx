"use client"

import { useEffect, useRef, useState } from "react"
import { FileText } from "lucide-react"
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
    <div className="space-y-3">
      <div className="flex items-baseline justify-between">
        <h2 className="text-lg font-semibold flex items-center gap-2">
          System prompt
          {dirty && editing && <span className="h-2 w-2 rounded-full bg-amber-400" title="Unsaved changes" />}
        </h2>
        <span className="text-[10px] text-muted-foreground">
          {charCount} chars{updatedHint ? ` · ${updatedHint}` : ""}
        </span>
      </div>

      <div className="rounded-xl border border-white/8 bg-card">
        <div className="flex items-center justify-between px-4 py-2 border-b border-white/5">
          <div className="flex items-center gap-2 text-xs text-muted-foreground">
            <FileText className="h-3 w-3" />
            <span>system_prompt.md</span>
          </div>
          {!readOnly && (
            <div className="flex items-center gap-2">
              {editing ? (
                <>
                  <button
                    type="button"
                    className="text-xs px-2.5 py-1 rounded text-muted-foreground hover:text-foreground"
                    onClick={handleCancel}
                    disabled={saving}
                  >
                    Cancel
                  </button>
                  <button
                    type="button"
                    className={cn(
                      "text-xs px-3 py-1 rounded text-white",
                      dirty && !saving
                        ? "bg-emerald-600/80 hover:bg-emerald-500"
                        : "bg-emerald-700/40 cursor-not-allowed",
                    )}
                    onClick={handleSave}
                    disabled={!dirty || saving}
                  >
                    {saving ? "Saving…" : "Save"}
                  </button>
                </>
              ) : (
                <button
                  type="button"
                  className="text-xs px-2.5 py-1 rounded border border-white/10 hover:bg-white/5 text-foreground/80"
                  onClick={() => setEditing(true)}
                >
                  Edit
                </button>
              )}
            </div>
          )}
        </div>

        {editing ? (
          <textarea
            ref={taRef}
            value={draft}
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
            className="w-full px-4 py-3 text-xs leading-relaxed font-mono text-foreground bg-transparent outline-none resize-y min-h-[260px]"
          />
        ) : (
          <pre className="px-4 py-3 text-xs leading-relaxed text-foreground/85 font-mono whitespace-pre-wrap max-h-[260px] overflow-y-auto">
            {value || (
              <em className="text-muted-foreground not-italic">
                empty — click Edit to write a system prompt
              </em>
            )}
          </pre>
        )}

        {error && (
          <div className="px-4 py-2 border-t border-red-500/20 bg-red-500/5 text-xs text-red-300">
            Save failed: {error}
          </div>
        )}
      </div>
    </div>
  )
}
