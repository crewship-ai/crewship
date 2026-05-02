"use client"

import { useId, useState } from "react"
import { Cpu, MemoryStick, Clock, Network as NetIcon, Globe, Lock, X, Plus } from "lucide-react"
import { cn } from "@/lib/utils"
import { CPU_PRESETS, MEMORY_PRESETS, TTL_PRESETS, type WizardState } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

export function StepRuntime({ state, setState }: Props) {
  return (
    <div className="space-y-3">
      {/* Container resources — three side-by-side cells */}
      <SectionHeader
        title="Container resources"
        badge="LIMITS"
        hint={`crewship-team-${state.slug || "<slug>"}`}
      />
      <div className="grid grid-cols-3 gap-2.5">
        <ResourceCell
          icon={MemoryStick}
          label="Memory"
          value={prettyMemory(state.memoryMB)}
          help="Hard limit"
          cli={`--memory-mb ${state.memoryMB}`}
          tone="blue"
        >
          <ChipRow>
            {MEMORY_PRESETS.map((p) => (
              <Chip key={p.value} active={state.memoryMB === p.value} onClick={() => setState({ memoryMB: p.value })}>
                {p.label}
              </Chip>
            ))}
            <CustomNumberChip
              active={!MEMORY_PRESETS.some((p) => p.value === state.memoryMB)}
              value={state.memoryMB}
              onChange={(v) => setState({ memoryMB: v })}
              min={128}
              max={65536}
              suffix="MB"
            />
          </ChipRow>
        </ResourceCell>

        <ResourceCell
          icon={Cpu}
          label="CPUs"
          value={`${state.cpus} ${state.cpus === 1 ? "core" : "cores"}`}
          help="Fractional cores OK"
          cli={`--cpus ${state.cpus}`}
          tone="violet"
        >
          <ChipRow>
            {CPU_PRESETS.map((p) => (
              <Chip key={p.value} active={state.cpus === p.value} onClick={() => setState({ cpus: p.value })}>
                {p.label}
              </Chip>
            ))}
            <CustomNumberChip
              active={!CPU_PRESETS.some((p) => p.value === state.cpus)}
              value={state.cpus}
              onChange={(v) => setState({ cpus: v })}
              min={0.1}
              max={64}
              step={0.1}
              suffix="cpu"
            />
          </ChipRow>
        </ResourceCell>

        <ResourceCell
          icon={Clock}
          label="Auto-stop"
          value={state.ttlHours === null ? "Never" : `${state.ttlHours} h idle`}
          help="Saves cost"
          cli={state.ttlHours === null ? "(no --ttl)" : `--ttl ${state.ttlHours}`}
          tone="amber"
        >
          <ChipRow>
            {TTL_PRESETS.map((p) => (
              <Chip key={String(p.value)} active={state.ttlHours === p.value} onClick={() => setState({ ttlHours: p.value })}>
                {p.label}
              </Chip>
            ))}
          </ChipRow>
        </ResourceCell>
      </div>

      {/* Network policy — full-width cell with mode + conditional domain list */}
      <SectionHeader
        title="Network policy"
        badge="SECURITY"
        hint="outbound HTTP from container"
      />
      <NetworkCell state={state} setState={setState} />
    </div>
  )
}

// =============================================================================
// Section header
// =============================================================================

function SectionHeader({ title, badge, hint }: { title: string; badge?: string; hint?: string }) {
  return (
    <div className="flex items-baseline gap-2.5 pt-1">
      <h3 className="text-[12.5px] font-semibold text-foreground/90">{title}</h3>
      {badge && (
        <span className="text-[9px] uppercase tracking-wider px-1.5 py-0.5 rounded-full bg-white/[0.06] border border-white/10 text-muted-foreground font-medium">
          {badge}
        </span>
      )}
      {hint && <span className="ml-auto text-[11px] text-muted-foreground/70 font-mono">{hint}</span>}
    </div>
  )
}

// =============================================================================
// ResourceCell — card-style cell for a single resource (memory / CPU / TTL)
// =============================================================================

const TONE_STYLES = {
  blue: { ring: "border-blue-400/20", icon: "bg-blue-500/15 text-blue-300", value: "text-blue-300" },
  violet: { ring: "border-violet-400/20", icon: "bg-violet-500/15 text-violet-300", value: "text-violet-300" },
  amber: { ring: "border-amber-400/20", icon: "bg-amber-500/15 text-amber-300", value: "text-amber-300" },
} as const

function ResourceCell({
  icon: Icon, label, value, help, cli, tone, children,
}: {
  icon: React.ElementType
  label: string
  value: string
  help: string
  cli: string
  tone: keyof typeof TONE_STYLES
  children: React.ReactNode
}) {
  const t = TONE_STYLES[tone]
  return (
    <div className={cn(
      "rounded-lg border bg-card/60 backdrop-blur-sm p-3 flex flex-col gap-2.5 transition-colors hover:bg-card/80",
      "border-white/10",
      t.ring,
    )}>
      <div className="flex items-start gap-2.5">
        <div className={cn("h-8 w-8 rounded-md flex items-center justify-center shrink-0", t.icon)}>
          <Icon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <span className="text-[10.5px] uppercase tracking-wider text-muted-foreground font-medium">{label}</span>
            <span className="text-[10px] text-muted-foreground/60 truncate">— {help}</span>
          </div>
          <div className={cn("text-[15px] font-semibold leading-tight mt-0.5", t.value)}>{value}</div>
        </div>
      </div>
      <div className="min-h-[28px]">{children}</div>
      <code className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-black/30 text-muted-foreground/70 truncate self-start">
        {cli}
      </code>
    </div>
  )
}

// =============================================================================
// NetworkCell — full-width cell with Free/Restricted segmented + domain editor
// =============================================================================

function NetworkCell({ state, setState }: Props) {
  const restricted = state.networkMode === "restricted"

  return (
    <div className="rounded-lg border border-white/10 bg-card/60 backdrop-blur-sm p-3 space-y-3">
      <div className="flex items-start gap-3 flex-wrap">
        <div className={cn(
          "h-8 w-8 rounded-md flex items-center justify-center shrink-0",
          restricted ? "bg-amber-500/15 text-amber-300" : "bg-emerald-500/15 text-emerald-300",
        )}>
          <NetIcon className="h-4 w-4" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[10.5px] uppercase tracking-wider text-muted-foreground font-medium">Mode</div>
          <div className={cn(
            "text-[15px] font-semibold leading-tight mt-0.5",
            restricted ? "text-amber-300" : "text-emerald-300",
          )}>
            {restricted ? "Restricted" : "Free"}
            <span className="ml-2 text-[11px] text-muted-foreground font-normal">
              {restricted ? "— allowlist below" : "— any HTTP endpoint"}
            </span>
          </div>
        </div>

        {/* Toggle as two big mode cards */}
        <div className="grid grid-cols-2 gap-1.5 w-[280px] shrink-0">
          <ModeCard
            active={!restricted}
            onClick={() => setState({ networkMode: "free", allowedDomains: [] })}
            icon={Globe}
            label="Free"
            tone="emerald"
          />
          <ModeCard
            active={restricted}
            onClick={() => setState({ networkMode: "restricted" })}
            icon={Lock}
            label="Restricted"
            tone="amber"
          />
        </div>
        <code className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-black/30 text-muted-foreground/70 self-start mt-1.5">
          --network-mode {state.networkMode}
        </code>
      </div>

      {restricted && (
        <div className="pt-2.5 border-t border-white/5">
          <div className="flex items-baseline justify-between mb-1.5">
            <span className="text-[10.5px] uppercase tracking-wider text-muted-foreground font-medium">
              Allowed domains
              <span className="ml-2 text-[10px] text-muted-foreground/60 normal-case tracking-normal">supports wildcards (<code className="font-mono">*.github.com</code>)</span>
            </span>
            <span className="text-[10px] text-muted-foreground">{state.allowedDomains.length} listed</span>
          </div>
          <DomainChips
            value={state.allowedDomains}
            onChange={(v) => setState({ allowedDomains: v })}
          />
          {state.allowedDomains.length === 0 && (
            <p className="text-[11px] text-amber-400/80 mt-1.5">
              ⚠ Empty allowlist locks all egress. Add at least one domain unless that's intentional.
            </p>
          )}
        </div>
      )}
    </div>
  )
}

function ModeCard({ active, onClick, icon: Icon, label, tone }: {
  active: boolean
  onClick: () => void
  icon: React.ElementType
  label: string
  tone: "emerald" | "amber"
}) {
  const tones = {
    emerald: { active: "border-emerald-400 bg-emerald-500/10 text-emerald-300", icon: "text-emerald-400" },
    amber: { active: "border-amber-400 bg-amber-500/10 text-amber-300", icon: "text-amber-400" },
  } as const
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "rounded-md border px-3 py-2 text-xs font-medium flex items-center justify-center gap-2 transition-all",
        active
          ? `${tones[tone].active} shadow-sm`
          : "border-white/10 bg-card/40 text-muted-foreground hover:border-white/20 hover:text-foreground/80",
      )}
    >
      <Icon className={cn("h-3.5 w-3.5", active ? tones[tone].icon : "")} />
      {label}
    </button>
  )
}

// =============================================================================
// Building blocks
// =============================================================================

function ChipRow({ children }: { children: React.ReactNode }) {
  return <div className="flex flex-wrap gap-1">{children}</div>
}

function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "px-2 py-0.5 rounded text-[11px] border transition-colors",
        active
          ? "bg-blue-500/20 border-blue-400 text-blue-300"
          : "bg-card border-white/10 text-foreground/70 hover:border-white/20",
      )}
    >
      {children}
    </button>
  )
}

function CustomNumberChip({ active, value, onChange, min, max, step = 1, suffix }: {
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
          "inline-flex items-center gap-1 px-1.5 py-0.5 rounded border text-[11px]",
          error
            ? "bg-red-500/10 border-red-400/60"
            : active
              ? "bg-blue-500/20 border-blue-400"
              : "bg-card border-white/10",
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
              "w-12 bg-transparent outline-none text-right font-medium",
              error ? "text-red-300" : "text-blue-300",
            )}
          />
          <span className="text-[9px] text-muted-foreground" aria-hidden="true">{suffix}</span>
        </div>
        {error && (
          <span id={errorId} role="alert" className="text-[10px] text-red-300/90">
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
      className="px-2 py-0.5 rounded text-[11px] border border-white/10 bg-card text-foreground/70 hover:border-white/20"
    >
      Custom…
    </button>
  )
}

function DomainChips({ value, onChange }: { value: string[]; onChange: (v: string[]) => void }) {
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
    <div className="flex flex-wrap gap-1.5 p-2 bg-zinc-950 border border-white/15 rounded-md min-h-[40px] focus-within:border-blue-400 focus-within:ring-2 focus-within:ring-blue-400/20 transition-shadow">
      {value.map((d) => (
        <span key={d} className="inline-flex items-center gap-1 pl-2 pr-1 py-0.5 rounded-full bg-amber-500/10 border border-amber-400/30 font-mono text-[11px] text-amber-200/90">
          {d}
          <button type="button" onClick={() => onChange(value.filter((x) => x !== d))} className="text-amber-400/60 hover:text-red-400 px-0.5" aria-label={`Remove ${d}`}>
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
      <div className="flex items-center gap-1 flex-1 min-w-[140px]">
        <label htmlFor={inputId} className="sr-only">Add an allowed domain</label>
        <Plus className="h-3 w-3 text-muted-foreground/50 ml-1" aria-hidden="true" />
        <input
          id={inputId}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" || e.key === ",") {
              e.preventDefault()
              commit()
            } else if (e.key === "Backspace" && draft === "" && value.length > 0) {
              onChange(value.slice(0, -1))
            }
          }}
          onBlur={commit}
          placeholder={value.length === 0 ? "github.com, *.npmjs.org, api.anthropic.com" : "add another…"}
          className="flex-1 bg-transparent border-0 outline-none text-xs font-mono px-1 py-0.5 placeholder:text-muted-foreground/40"
        />
      </div>
    </div>
  )
}

// =============================================================================
// Helpers
// =============================================================================

function prettyMemory(mb: number): string {
  if (mb >= 1024) {
    const gb = mb / 1024
    return Number.isInteger(gb) ? `${gb} GB` : `${gb.toFixed(1)} GB`
  }
  return `${mb} MB`
}
