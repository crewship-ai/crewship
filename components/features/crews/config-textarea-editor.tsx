"use client"

import { useEffect, useRef, useState } from "react"
import { Check, FileCode2, X } from "lucide-react"
import { cn } from "@/lib/utils"

export interface ConfigTextareaEditorProps {
  /** What kind of payload — used for validation hint and label. */
  format: "json" | "toml" | "text"
  /** Current persisted value (null/empty = "not set"). */
  value: string | null
  /** Filename label shown in the toolbar. */
  filename: string
  /** Sample placeholder for empty state. */
  placeholder?: string
  /** Persist on Save click. Throw to surface error inline. */
  onSave: (next: string | null) => void | Promise<void>
  /** Read-only when true. */
  readOnly?: boolean
  /** Optional hint shown above the textarea. */
  hint?: React.ReactNode
}

/**
 * Restored from the deleted crew-runtime-config component. Used for
 * devcontainer.json, mise.toml, and escalation_config.json payloads
 * — fields the backend's PATCH whitelists but the redesign initially
 * dropped as "CLI only". They're not CLI-only; they're inline-editable
 * with a basic JSON / TOML editor.
 *
 * Validation is lightweight (parse JSON if format=json) so the user
 * gets a red border before the API rejects the payload.
 */
export function ConfigTextareaEditor({
  format,
  value,
  filename,
  placeholder,
  onSave,
  readOnly = false,
  hint,
}: ConfigTextareaEditorProps) {
  const initial = value ?? ""
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(initial)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const taRef = useRef<HTMLTextAreaElement | null>(null)

  useEffect(() => {
    if (!editing) setDraft(value ?? "")
  }, [value, editing])

  useEffect(() => {
    if (editing && taRef.current) taRef.current.focus()
  }, [editing])

  const dirty = draft !== initial
  const validation = validate(draft, format)

  const handleSave = async () => {
    if (!dirty) {
      setEditing(false)
      return
    }
    if (validation.error) {
      setError(validation.error)
      return
    }
    setSaving(true)
    setError(null)
    try {
      // Send null for empty strings so the backend can clear the field.
      const trimmed = draft.trim()
      await onSave(trimmed === "" ? null : trimmed)
      setEditing(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const handleCancel = () => {
    setDraft(initial)
    setError(null)
    setEditing(false)
  }

  return (
    <div className="rounded border border-white/8 bg-zinc-950/30">
      <div className="flex items-center justify-between px-3 py-1.5 border-b border-white/5">
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <FileCode2 className="h-3 w-3" />
          <span className="font-mono">{filename}</span>
          {!editing && value && (
            <span className="text-[10px] text-muted-foreground/70">
              · {value.split(/\r?\n/).length} lines
            </span>
          )}
        </div>
        {!readOnly && (
          <div className="flex items-center gap-1.5">
            {editing ? (
              <>
                {validation.error && editing && (
                  <span className="text-[10px] text-red-300 flex items-center gap-1">
                    <X className="h-3 w-3" />
                    {validation.error}
                  </span>
                )}
                {!validation.error && dirty && (
                  <span className="text-[10px] text-emerald-300 flex items-center gap-1">
                    <Check className="h-3 w-3" />
                    valid
                  </span>
                )}
                <button
                  type="button"
                  onClick={handleCancel}
                  disabled={saving}
                  className="text-[11px] px-2 py-0.5 rounded text-muted-foreground hover:text-foreground"
                >
                  Cancel
                </button>
                <button
                  type="button"
                  onClick={handleSave}
                  disabled={!dirty || saving || Boolean(validation.error)}
                  className={cn(
                    "text-[11px] px-2 py-0.5 rounded text-white",
                    dirty && !saving && !validation.error
                      ? "bg-emerald-600/80 hover:bg-emerald-500"
                      : "bg-emerald-700/40 cursor-not-allowed",
                  )}
                >
                  {saving ? "Saving…" : "Save"}
                </button>
              </>
            ) : (
              <button
                type="button"
                onClick={() => setEditing(true)}
                className="text-[11px] px-2 py-0.5 rounded border border-white/10 hover:bg-white/5 text-foreground/80"
              >
                {value ? "Edit" : "Add"}
              </button>
            )}
          </div>
        )}
      </div>
      {hint && <div className="px-3 py-1.5 text-[10px] text-muted-foreground border-b border-white/5">{hint}</div>}
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
          placeholder={placeholder}
          className={cn(
            "w-full px-3 py-2 text-[11px] leading-relaxed font-mono bg-transparent outline-none resize-y min-h-[120px]",
            validation.error
              ? "text-foreground border-l-2 border-red-400"
              : "text-foreground",
          )}
        />
      ) : value ? (
        <pre className="px-3 py-2 text-[11px] leading-relaxed text-foreground/85 font-mono whitespace-pre-wrap max-h-[180px] overflow-y-auto">
          {value}
        </pre>
      ) : (
        <div className="px-3 py-3 text-[11px] text-muted-foreground italic">
          empty — click {readOnly ? "(read-only)" : "Add"} to configure
        </div>
      )}

      {error && (
        <div className="px-3 py-1.5 border-t border-red-500/20 bg-red-500/5 text-[11px] text-red-300">
          Save failed: {error}
        </div>
      )}
    </div>
  )
}

function validate(text: string, format: ConfigTextareaEditorProps["format"]): { error: string | null } {
  if (text.trim() === "") return { error: null }
  if (format === "json") {
    try {
      JSON.parse(text)
      return { error: null }
    } catch (err) {
      return { error: err instanceof Error ? err.message.split("\n")[0] : "invalid JSON" }
    }
  }
  // TOML/text: minimal validation — non-empty is valid.
  return { error: null }
}
