"use client"

import { useId, useState } from "react"
import { Plus, X } from "lucide-react"
import { cn } from "@/lib/utils"

/**
 * The controls the Container step's sizing and egress rows are built from.
 *
 * They used to live in `step-runtime.tsx`, one step earlier in the wizard,
 * wrapped in cards of their own. The step is gone — resources are an
 * administrator's question and now sit folded away inside Container — but the
 * controls themselves were fine and are lifted here unchanged rather than
 * rewritten alongside everything else.
 */

export function ChipRow({ children }: { children: React.ReactNode }) {
  return <div className="flex flex-wrap gap-1">{children}</div>
}

export function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "rounded border px-2 py-0.5 text-[11px] transition-colors",
        "max-sm:h-12 max-sm:px-3 max-sm:text-sm",
        active
          ? "border-primary bg-primary/20 text-primary"
          : "border-hairline bg-card text-foreground/70 hover:border-white/20",
      )}
    >
      {children}
    </button>
  )
}

export function CustomNumberChip({ active, value, onChange, min, max, step = 1, suffix }: {
  active: boolean
  value: number
  onChange: (v: number) => void
  min: number
  max: number
  step?: number
  suffix: string
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(String(value))
  const [error, setError] = useState<string | null>(null)
  // useId gives each chip instance (memory / cpu) a unique error id so
  // simultaneous editors don't collide on duplicate aria-describedby targets.
  const errorId = useId()

  // Show the editor while the user is editing OR while value is custom OR
  // while an error is sticky — without the error gate, an invalid blur
  // collapses the editor before the error message renders (CodeRabbit).
  const showEditor = active || editing || !!error

  if (showEditor) {
    return (
      <div className="inline-flex flex-col gap-0.5">
        <div className={cn(
          "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[11px]",
          error
            ? "border-destructive/60 bg-destructive/10"
            : active
              ? "border-primary bg-primary/20"
              : "border-hairline bg-card",
        )}>
          <input
            type="number"
            autoFocus={editing}
            value={draft}
            min={min}
            max={max}
            step={step}
            aria-invalid={error ? "true" : undefined}
            aria-describedby={error ? errorId : undefined}
            aria-label={`Custom ${suffix} value (range ${min}-${max})`}
            onChange={(e) => { setDraft(e.target.value); if (error) setError(null) }}
            onBlur={() => {
              const n = Number(draft)
              if (!Number.isNaN(n) && n >= min && n <= max) {
                onChange(n)
                setError(null)
                setEditing(false)
              } else {
                setError(`Enter ${min}-${max} ${suffix}`)
                setDraft(String(value))
                // Keep editing=true so the field stays mounted and the user
                // can read the error + retry without re-clicking Custom….
              }
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter") (e.target as HTMLInputElement).blur()
              if (e.key === "Escape") { setDraft(String(value)); setError(null); setEditing(false) }
            }}
            className={cn(
              "w-12 bg-transparent text-right font-medium outline-none",
              error ? "text-destructive" : "text-primary",
            )}
          />
          <span className="text-[9px] text-muted-foreground" aria-hidden="true">{suffix}</span>
        </div>
        {error && (
          <span id={errorId} role="alert" className="text-[10px] text-destructive/90">
            {error}
          </span>
        )}
      </div>
    )
  }

  return (
    <button
      type="button"
      onClick={() => { setDraft(String(value)); setEditing(true) }}
      className="rounded border border-hairline bg-card px-2 py-0.5 text-[11px] text-foreground/70 hover:border-white/20 max-sm:h-12 max-sm:px-3 max-sm:text-sm"
    >
      Custom…
    </button>
  )
}

export function DomainChips({ value, onChange }: { value: string[]; onChange: (v: string[]) => void }) {
  const [draft, setDraft] = useState("")
  const inputId = useId()

  const commit = () => {
    const trimmed = draft.trim().toLowerCase()
    if (!trimmed) return
    if (value.includes(trimmed)) { setDraft(""); return }
    onChange([...value, trimmed])
    setDraft("")
  }

  return (
    <div className="flex min-h-[40px] flex-wrap gap-1.5 rounded-md border border-hairline bg-background p-2 transition-shadow focus-within:border-primary focus-within:ring-2 focus-within:ring-primary/20">
      {value.map((d) => (
        <span key={d} className="inline-flex items-center gap-1 rounded-full border border-warn/30 bg-warn/10 py-0.5 pl-2 pr-1 font-mono text-[11px] text-warn/90">
          {d}
          <button type="button" onClick={() => onChange(value.filter((x) => x !== d))} className="px-0.5 text-warn/60 hover:text-destructive" aria-label={`Remove ${d}`}>
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
      <div className="flex min-w-[140px] flex-1 items-center gap-1">
        <label htmlFor={inputId} className="sr-only">Add an allowed domain</label>
        <Plus className="ml-1 h-3 w-3 text-muted-foreground-soft" aria-hidden="true" />
        <input
          id={inputId}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // A CJK input method uses Enter to accept its candidate, and that
            // keystroke reaches here first — committing on it stores half a
            // word as a hostname. `isComposing` is the flag the IME sets for
            // exactly this.
            if (e.nativeEvent.isComposing) return
            if (e.key === "Enter" || e.key === ",") {
              e.preventDefault()
              commit()
            } else if (e.key === "Backspace" && draft === "" && value.length > 0) {
              onChange(value.slice(0, -1))
            }
          }}
          onBlur={commit}
          placeholder={value.length === 0 ? "github.com, *.npmjs.org, api.anthropic.com" : "add another…"}
          className="flex-1 border-0 bg-transparent px-1 py-0.5 font-mono text-xs outline-none placeholder:text-muted-foreground-soft"
        />
      </div>
    </div>
  )
}

export function prettyMemory(mb: number): string {
  if (mb >= 1024) {
    const gb = mb / 1024
    return Number.isInteger(gb) ? `${gb} GB` : `${gb.toFixed(1)} GB`
  }
  return `${mb} MB`
}
