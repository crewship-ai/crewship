"use client"

import { useEffect, useState } from "react"
import { Calendar } from "lucide-react"
import { cn } from "@/lib/utils"

export interface ScheduleEditorProps {
  cron: string | null | undefined
  prompt: string | null | undefined
  enabled: boolean
  lastRun?: string | null
  nextRun?: string | null
  onSave: (next: { cron: string; prompt: string; enabled: boolean }) => void | Promise<void>
  readOnly?: boolean
}

/**
 * Cron-driven self-trigger editor: cron expression + prompt + on/off toggle.
 * Backed by agents.schedule_cron / schedule_prompt / schedule_enabled
 * columns; surface for the existing schedule machinery.
 */
export function ScheduleEditor({
  cron,
  prompt,
  enabled,
  lastRun,
  nextRun,
  onSave,
  readOnly = false,
}: ScheduleEditorProps) {
  const [editing, setEditing] = useState(false)
  const [draftCron, setDraftCron] = useState(cron ?? "")
  const [draftPrompt, setDraftPrompt] = useState(prompt ?? "")
  const [draftEnabled, setDraftEnabled] = useState(enabled)
  const [saving, setSaving] = useState(false)

  // Sync drafts back from props once we're not actively editing — keeps
  // the editor honest after parent re-fetches (e.g. another tab toggled
  // the schedule, or onSave returned a normalized cron expression).
  useEffect(() => {
    if (editing) return
    setDraftCron(cron ?? "")
    setDraftPrompt(prompt ?? "")
    setDraftEnabled(enabled)
  }, [editing, cron, prompt, enabled])

  const handleToggle = async (next: boolean) => {
    if (readOnly) return
    try {
      setSaving(true)
      await onSave({ cron: cron ?? "", prompt: prompt ?? "", enabled: next })
    } finally {
      setSaving(false)
    }
  }

  const handleSave = async () => {
    setSaving(true)
    try {
      await onSave({ cron: draftCron, prompt: draftPrompt, enabled: draftEnabled })
      setEditing(false)
    } finally {
      setSaving(false)
    }
  }

  const handleCancel = () => {
    setDraftCron(cron ?? "")
    setDraftPrompt(prompt ?? "")
    setDraftEnabled(enabled)
    setEditing(false)
  }

  return (
    <section className="space-y-3">
      <div className="flex items-baseline justify-between">
        <h2 className="text-lg font-semibold flex items-center gap-2">
          <Calendar className="h-4 w-4 text-muted-foreground" />
          Schedule
        </h2>
        <div className="flex items-center gap-2 text-xs">
          <span className="text-muted-foreground">Enabled</span>
          <button
            type="button"
            disabled={readOnly || saving}
            onClick={() => handleToggle(!enabled)}
            className={cn(
              "relative inline-flex items-center w-9 h-5 rounded-full transition-colors",
              enabled ? "bg-emerald-600/70" : "bg-zinc-700",
              (readOnly || saving) && "opacity-50 cursor-not-allowed",
            )}
            aria-pressed={enabled}
          >
            <span
              className={cn(
                "absolute w-4 h-4 rounded-full bg-white transition-transform",
                enabled ? "translate-x-[18px]" : "translate-x-0.5",
              )}
            />
          </button>
        </div>
      </div>

      <div className="rounded-xl border border-white/8 bg-card divide-y divide-white/5">
        {editing ? (
          <>
            <div className="px-4 py-2.5 grid grid-cols-[180px_1fr] gap-3 items-center">
              <span className="text-xs text-muted-foreground">Cron</span>
              <input
                value={draftCron}
                onChange={(e) => setDraftCron(e.target.value)}
                placeholder="0 9 * * 1-5"
                className="bg-zinc-950 border border-white/15 rounded px-2 py-1 text-sm font-mono outline-none focus:border-blue-400"
              />
            </div>
            <div className="px-4 py-2.5 grid grid-cols-[180px_1fr] gap-3 items-start">
              <span className="text-xs text-muted-foreground mt-1.5">Prompt</span>
              <textarea
                value={draftPrompt}
                onChange={(e) => setDraftPrompt(e.target.value)}
                rows={3}
                className="bg-zinc-950 border border-white/15 rounded px-2 py-1 text-sm outline-none focus:border-blue-400 resize-y min-h-[60px]"
                placeholder="What this agent should do every time the schedule fires…"
              />
            </div>
            <div className="px-4 py-2 flex justify-end gap-2">
              <button
                type="button"
                onClick={handleCancel}
                className="text-xs px-2.5 py-1 rounded text-muted-foreground hover:text-foreground"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={handleSave}
                disabled={saving}
                className="text-xs px-3 py-1 rounded bg-emerald-600/80 hover:bg-emerald-500 text-white disabled:opacity-50"
              >
                {saving ? "Saving…" : "Save"}
              </button>
            </div>
          </>
        ) : (
          <>
            <div className="px-4 py-2.5 grid grid-cols-[180px_1fr] gap-3 items-center">
              <span className="text-xs text-muted-foreground">Cron</span>
              <div className="flex items-center gap-2">
                {cron ? (
                  <code className="text-sm bg-zinc-950 px-2 py-0.5 rounded border border-white/10 font-mono">
                    {cron}
                  </code>
                ) : (
                  <em className="text-sm text-muted-foreground italic">not set</em>
                )}
                {!readOnly && (
                  <button
                    type="button"
                    onClick={() => setEditing(true)}
                    className="text-[11px] text-muted-foreground hover:text-foreground"
                  >
                    edit
                  </button>
                )}
              </div>
            </div>
            <div className="px-4 py-2.5 grid grid-cols-[180px_1fr] gap-3 items-start">
              <span className="text-xs text-muted-foreground mt-0.5">Prompt</span>
              <span className="text-sm text-foreground/85">
                {prompt || <em className="text-muted-foreground italic">not set</em>}
              </span>
            </div>
            {(lastRun || nextRun) && (
              <div className="px-4 py-2 grid grid-cols-2 gap-3 text-xs">
                {lastRun && (
                  <div>
                    <span className="text-muted-foreground">Last run:</span>
                    <span className="text-foreground/85 ml-1">{lastRun}</span>
                  </div>
                )}
                {nextRun && (
                  <div>
                    <span className="text-muted-foreground">Next run:</span>
                    <span className="text-foreground/85 ml-1">{nextRun}</span>
                  </div>
                )}
              </div>
            )}
          </>
        )}
      </div>
    </section>
  )
}
