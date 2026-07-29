"use client"

import { useEffect, useId, useRef, useState } from "react"
import { toast } from "sonner"
import { Check } from "lucide-react"

import { cn } from "@/lib/utils"

// =============================================================================
// Direct-control config rows.
//
// The old detail screens put an "Upravit" affordance on every row, which on a
// phone stacks into a column of identical blue words and reads as noise. These
// rows drop the edit mode entirely: the value IS the control. A text field
// commits on blur, a select/switch/preset commits on change, and every commit
// is optimistic with a rollback — if the PATCH is rejected the previous value
// comes back and the reason surfaces, instead of the UI quietly diverging from
// the server.
//
// Layout is a fixed 248px control column so selects, switches, steppers and
// inputs all start on the same vertical line. Right-aligning them instead
// leaves a ragged edge, because each control is a different width.
// =============================================================================

export interface ConfigRowProps {
  label: string
  hint?: string
  htmlFor?: string
  children: React.ReactNode
  /** Full-width control under the label — for textareas and chip rows. */
  full?: boolean
}

export function ConfigRow({ label, hint, htmlFor, children, full = false }: ConfigRowProps) {
  return (
    <div
      className={cn(
        "grid min-h-[38px] items-center gap-3.5 border-b border-border px-3 py-1.5 transition-colors last:border-b-0 hover:bg-white/[.025]",
        full ? "grid-cols-1 gap-1.5" : "grid-cols-1 md:grid-cols-[minmax(0,1fr)_248px]",
      )}
    >
      <div className="min-w-0">
        <label htmlFor={htmlFor} className="block text-label font-medium text-foreground">
          {label}
        </label>
        {hint && <span className="mt-0.5 block text-micro leading-snug text-muted-foreground-soft">{hint}</span>}
      </div>
      <div className={cn("flex min-w-0 items-center gap-2", full && "w-full")}>{children}</div>
    </div>
  )
}

/** Green tick that fades out after a successful commit. */
function SavedTick({ show }: { show: boolean }) {
  return (
    <span
      aria-hidden
      className={cn(
        "inline-flex shrink-0 items-center gap-1 text-micro text-success transition-opacity",
        show ? "opacity-100" : "opacity-0",
      )}
    >
      <Check className="h-3 w-3" />
    </span>
  )
}

function useOptimistic<T>(value: T, onSave: (next: T) => Promise<void> | void) {
  const [local, setLocal] = useState<T>(value)
  const [saved, setSaved] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  // Last value the server confirmed. A text field mutates `local` on every
  // keystroke, so rolling back to "whatever local was a moment ago" would
  // restore the rejected draft. The prop is the only trustworthy baseline.
  const server = useRef<T>(value)

  // Follow the record when it is refetched or patched from elsewhere.
  useEffect(() => {
    server.current = value
    setLocal(value)
  }, [value])
  useEffect(() => () => { if (timer.current) clearTimeout(timer.current) }, [])

  async function commit(next: T) {
    setLocal(next)
    try {
      await onSave(next)
      server.current = next
      setSaved(true)
      if (timer.current) clearTimeout(timer.current)
      timer.current = setTimeout(() => setSaved(false), 1500)
    } catch (err) {
      setLocal(server.current)
      toast.error(err instanceof Error ? err.message : "Uložení se nepovedlo")
    }
  }

  return { local, setLocal, saved, commit }
}

const inputBase =
  "w-full rounded-lg border border-border bg-background px-2.5 py-1 text-label text-foreground outline-none " +
  "transition-[border-color,box-shadow] hover:border-foreground/25 " +
  "focus:border-primary focus:shadow-[0_0_0_3px_color-mix(in_oklch,var(--primary)_20%,transparent)]"

export interface ConfigTextProps {
  label: string
  hint?: string
  value: string
  mono?: boolean
  multiline?: boolean
  placeholder?: string
  onSave: (next: string) => Promise<void> | void
}

export function ConfigText({ label, hint, value, mono, multiline, placeholder, onSave }: ConfigTextProps) {
  const id = useId()
  const { local, setLocal, saved, commit } = useOptimistic(value, onSave)

  function handleBlur() {
    if (local === value) return
    void commit(local)
  }

  const shared = {
    id,
    value: local,
    placeholder,
    onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => setLocal(e.target.value),
    onBlur: handleBlur,
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault()
        setLocal(value)
        ;(e.target as HTMLElement).blur()
      }
      if (e.key === "Enter" && !multiline) (e.target as HTMLElement).blur()
    },
    className: cn(inputBase, mono && "font-mono text-label"),
  }

  return (
    <ConfigRow label={label} hint={hint} htmlFor={id} full={multiline}>
      {multiline ? (
        <textarea {...shared} rows={3} className={cn(shared.className, "min-h-[62px] resize-y leading-relaxed")} />
      ) : (
        <input type="text" {...shared} />
      )}
      <SavedTick show={saved} />
    </ConfigRow>
  )
}

export interface ConfigSelectProps<T extends string> {
  label: string
  hint?: string
  value: T
  options: Array<{ value: T; label: string }>
  /** Rendered before the select — provider marks, crew colour, etc. */
  adornment?: React.ReactNode
  onSave: (next: T) => Promise<void> | void
}

export function ConfigSelect<T extends string>({
  label, hint, value, options, adornment, onSave,
}: ConfigSelectProps<T>) {
  const id = useId()
  const { local, saved, commit } = useOptimistic(value, onSave)

  return (
    <ConfigRow label={label} hint={hint} htmlFor={id}>
      {adornment}
      <select
        id={id}
        value={local}
        onChange={(e) => void commit(e.target.value as T)}
        className={cn(inputBase, "cursor-pointer appearance-none pr-7")}
        style={{
          backgroundImage:
            "linear-gradient(45deg, transparent 50%, var(--muted-foreground) 50%), linear-gradient(135deg, var(--muted-foreground) 50%, transparent 50%)",
          backgroundPosition: "calc(100% - 15px) 53%, calc(100% - 11px) 53%",
          backgroundSize: "4px 4px, 4px 4px",
          backgroundRepeat: "no-repeat",
        }}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
      <SavedTick show={saved} />
    </ConfigRow>
  )
}

export interface ConfigSwitchProps {
  label: string
  hint?: string
  checked: boolean
  /** Renders disabled — the capability exists but the caller may not use it. */
  locked?: boolean
  onSave: (next: boolean) => Promise<void> | void
}

export function ConfigSwitch({ label, hint, checked, locked = false, onSave }: ConfigSwitchProps) {
  const { local, saved, commit } = useOptimistic(checked, onSave)

  return (
    <ConfigRow label={label} hint={hint}>
      <button
        type="button"
        role="switch"
        aria-label={label}
        aria-checked={local}
        disabled={locked}
        onClick={() => { if (!locked) void commit(!local) }}
        className={cn(
          "relative h-6 w-10 shrink-0 rounded-full border transition-colors",
          local ? "border-transparent bg-success" : "border-border bg-surface-raised",
          locked && "cursor-not-allowed opacity-50",
        )}
      >
        <span
          className={cn(
            "absolute left-0.5 top-0.5 h-[18px] w-[18px] rounded-full transition-transform duration-300",
            local ? "translate-x-4 bg-white" : "bg-muted-foreground",
          )}
          style={{ transitionTimingFunction: "cubic-bezier(.34,1.56,.64,1)" }}
        />
      </button>
      <SavedTick show={saved} />
    </ConfigRow>
  )
}

export interface ConfigPresetsProps<T extends string | number> {
  label: string
  hint?: string
  value: T
  presets: Array<{ value: T; label: string }>
  onSave: (next: T) => Promise<void> | void
}

export function ConfigPresets<T extends string | number>({
  label, hint, value, presets, onSave,
}: ConfigPresetsProps<T>) {
  const { local, saved, commit } = useOptimistic(value, onSave)
  const isCustom = !presets.some((p) => p.value === local)

  return (
    <ConfigRow label={label} hint={hint}>
      <div className="flex w-full flex-wrap gap-1.5">
        {presets.map((p) => (
          <button
            key={String(p.value)}
            type="button"
            aria-pressed={local === p.value}
            onClick={() => void commit(p.value)}
            className={cn(
              "rounded-lg border px-2.5 py-1 text-label transition-colors",
              local === p.value
                ? "border-transparent bg-primary font-medium text-primary-foreground"
                : "border-border bg-background text-muted-foreground hover:border-foreground/25 hover:text-foreground",
            )}
          >
            {p.label}
          </button>
        ))}
        {isCustom && (
          <button
            type="button"
            aria-pressed
            className="rounded-lg border border-transparent bg-primary px-2.5 py-1 text-label font-medium text-primary-foreground"
          >
            vlastní · {String(local)}
          </button>
        )}
      </div>
      <SavedTick show={saved} />
    </ConfigRow>
  )
}

export interface ConfigCardOption<T extends string> {
  value: T
  title: string
  description: string
}

export interface ConfigCardsProps<T extends string> {
  /** Rendered as the card group's heading inside the section, not a row. */
  label?: string
  value: T
  options: ConfigCardOption<T>[]
  onSave: (next: T) => Promise<void> | void
}

/**
 * Radio cards. A three-way choice where each option needs a sentence of
 * explanation does not fit a segmented control — the label alone ("MINIMAL")
 * tells a reader nothing about what it costs them.
 */
export function ConfigCards<T extends string>({ value, options, onSave }: ConfigCardsProps<T>) {
  const { local, commit } = useOptimistic(value, onSave)

  return (
    <div className="grid gap-1.5 p-3" role="radiogroup">
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={local === o.value}
          onClick={() => void commit(o.value)}
          className={cn(
            "grid grid-cols-[auto_minmax(0,1fr)] items-center gap-2.5 rounded-[9px] border px-3 py-2.5 text-left transition-colors",
            local === o.value
              ? "border-primary bg-primary/10"
              : "border-border bg-background hover:border-foreground/25",
          )}
        >
          <span
            className={cn(
              "relative h-[15px] w-[15px] shrink-0 rounded-full border-[1.5px]",
              local === o.value ? "border-primary" : "border-border",
            )}
          >
            {local === o.value && <span className="absolute inset-[3px] rounded-full bg-primary" />}
          </span>
          <span>
            <span className="block text-label font-medium">{o.title}</span>
            <span className="mt-0.5 block text-micro leading-snug text-muted-foreground-soft">{o.description}</span>
          </span>
        </button>
      ))}
    </div>
  )
}

/** Read-only row for values this screen does not own (inherited from crew). */
export function ConfigReadOnly({ label, hint, value, note }: {
  label: string; hint?: string; value: React.ReactNode; note?: string
}) {
  return (
    <ConfigRow label={label} hint={hint}>
      <span className="truncate font-mono text-label">{value}</span>
      {note && <span className="shrink-0 text-micro text-muted-foreground-soft">{note}</span>}
    </ConfigRow>
  )
}
