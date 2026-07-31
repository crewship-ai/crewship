"use client"

import { useEffect, useState } from "react"
import { Calendar } from "lucide-react"
import { toast } from "sonner"
import { cn } from "@/lib/utils"
import { useDirtyForm } from "@/hooks/use-dirty-form"

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
  const [toggling, setToggling] = useState(false)

  // Cron and prompt are typed-in values committed by an explicit Save, which
  // is exactly what useDirtyForm is for: it keeps the draft when a write is
  // rejected, so a 4xx/5xx never quietly eats what someone typed. `enabled`
  // stays out of it — the switch is an atomic control that commits on the
  // spot (see the hook's own note about those).
  const form = useDirtyForm({ cron: cron ?? "", prompt: prompt ?? "" })

  // The switch renders this, not the `enabled` prop directly, so a failed
  // write can revert the visual flip instead of leaving it stuck showing a
  // state that was never actually saved.
  const [toggleEnabled, setToggleEnabled] = useState(enabled)
  useEffect(() => { setToggleEnabled(enabled) }, [enabled])

  const saving = toggling || form.status === "saving"

  const handleToggle = async (next: boolean) => {
    if (readOnly) return
    const previous = toggleEnabled
    setToggleEnabled(next) // optimistic — this is the only feedback the switch gives
    try {
      setToggling(true)
      await onSave({ cron: cron ?? "", prompt: prompt ?? "", enabled: next })
      toast.success(next ? "Schedule enabled" : "Schedule disabled")
    } catch (e) {
      setToggleEnabled(previous) // the write failed, don't leave the switch lying
      // The server's own words — it knows whether this was a validation
      // problem or a permission one, and guessing here would be a weaker
      // second copy of that rule.
      toast.error(e instanceof Error ? e.message : "Failed to update schedule")
    } finally {
      setToggling(false)
    }
  }

  const handleSave = async () => {
    let landed = false
    await form.submit(async (draft) => {
      try {
        // `enabled` comes from the switch, not from the draft: it commits on
        // its own, so re-sending a stale copy here would silently undo it.
        // `toggleEnabled` and not the `enabled` prop, because the prop is the
        // copy that lags — a toggle the server has already accepted is only
        // visible in the prop once the parent's refetch lands, and saving a
        // cron edit in that window would write the old value back.
        await onSave({ cron: draft.cron, prompt: draft.prompt, enabled: toggleEnabled })
        landed = true
      } catch (e) {
        toast.error(e instanceof Error ? e.message : "Failed to save schedule")
        // Rethrown, not swallowed: useDirtyForm records the message and keeps
        // the draft, so the edit survives for a retry.
        throw e
      }
    })
    // Only a write the server actually accepted closes the editor. A rejected
    // one leaves the form open and dirty — the read-only view would otherwise
    // show the old schedule as though it were the change that was just made.
    if (landed) {
      setEditing(false)
      toast.success("Schedule updated")
    }
  }

  const handleCancel = () => {
    form.reset()
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
            onClick={() => handleToggle(!toggleEnabled)}
            className={cn(
              "relative inline-flex items-center w-9 h-5 rounded-full transition-colors",
              toggleEnabled ? "bg-success/70" : "bg-muted",
              (readOnly || saving) && "opacity-50 cursor-not-allowed",
            )}
            aria-pressed={toggleEnabled}
          >
            <span
              className={cn(
                "absolute w-4 h-4 rounded-full bg-white transition-transform",
                toggleEnabled ? "translate-x-[18px]" : "translate-x-0.5",
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
                value={form.draft.cron}
                onChange={(e) => form.set("cron", e.target.value)}
                placeholder="0 9 * * 1-5"
                className="bg-background border border-white/15 rounded px-2 py-1 text-sm font-mono outline-none focus:border-primary"
              />
            </div>
            <div className="px-4 py-2.5 grid grid-cols-[180px_1fr] gap-3 items-start">
              <span className="text-xs text-muted-foreground mt-1.5">Prompt</span>
              <textarea
                value={form.draft.prompt}
                onChange={(e) => form.set("prompt", e.target.value)}
                rows={3}
                className="bg-background border border-white/15 rounded px-2 py-1 text-sm outline-none focus:border-primary resize-y min-h-[60px]"
                placeholder="What this agent should do every time the schedule fires…"
              />
            </div>
            <div className="px-4 py-2 flex items-center justify-end gap-2">
              {/* Outlives the toast: the reason the save was refused has to
                  still be readable next to the button you retry with. */}
              {form.status === "error" && (
                <span role="alert" className="mr-auto text-[11.5px] text-destructive min-w-0 truncate">
                  {form.error ?? "Save failed"}
                </span>
              )}
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
                className="text-xs px-3 py-1 rounded bg-success/80 hover:bg-success text-white disabled:opacity-50"
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
                  <code className="text-sm bg-background px-2 py-0.5 rounded border border-white/10 font-mono">
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
