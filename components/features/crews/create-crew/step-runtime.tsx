"use client"

import { useState } from "react"
import { Cpu, MemoryStick, Clock, Network as NetIcon, X } from "lucide-react"
import { cn } from "@/lib/utils"
import { CPU_PRESETS, MEMORY_PRESETS, TTL_PRESETS, type WizardState } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

export function StepRuntime({ state, setState }: Props) {
  return (
    <div className="space-y-3">
      <Group title="Container resources" badge="LIMITS" hint={`applied to crewship-team-${state.slug || "<slug>"}`}>
        <Field
          icon={MemoryStick}
          label="Memory"
          help="hard limit; container OOM-killed above this"
          cli={`--memory-mb ${state.memoryMB}`}
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
        </Field>

        <Field icon={Cpu} label="CPUs" help="fractional cores; e.g. 0.5 = half a core" cli={`--cpus ${state.cpus}`}>
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
              suffix="cores"
            />
          </ChipRow>
        </Field>

        <Field
          icon={Clock}
          label="Auto-stop after idle"
          help="container stops after N hours of no activity (saves $)"
          cli={state.ttlHours === null ? "(no --ttl)" : `--ttl ${state.ttlHours}`}
        >
          <ChipRow>
            {TTL_PRESETS.map((p) => (
              <Chip key={String(p.value)} active={state.ttlHours === p.value} onClick={() => setState({ ttlHours: p.value })}>
                {p.label}
              </Chip>
            ))}
          </ChipRow>
        </Field>
      </Group>

      <Group title="Network policy" badge="SECURITY" hint="controls outbound HTTP from container">
        <Field icon={NetIcon} label="Mode" help={undefined} cli={`--network-mode ${state.networkMode}`}>
          <div className="inline-flex rounded-lg border border-white/10 bg-card p-0.5">
            <SegBtn active={state.networkMode === "free"} onClick={() => setState({ networkMode: "free", allowedDomains: [] })}>Free</SegBtn>
            <SegBtn active={state.networkMode === "restricted"} onClick={() => setState({ networkMode: "restricted" })}>Restricted</SegBtn>
          </div>
          <p className="text-[11px] text-muted-foreground mt-1.5">
            <strong>Free:</strong> agents can hit any HTTP endpoint. <strong>Restricted:</strong> only the allowed domains below.
          </p>
        </Field>

        {state.networkMode === "restricted" && (
          <Field
            label="Allowed domains"
            help="supports wildcards (*.github.com)"
            cli={state.allowedDomains.length > 0 ? `--allowed-domains ${state.allowedDomains.join(",")}` : "(empty)"}
          >
            <DomainChips
              value={state.allowedDomains}
              onChange={(v) => setState({ allowedDomains: v })}
            />
          </Field>
        )}
      </Group>
    </div>
  )
}

// =============================================================================
// Building blocks
// =============================================================================

function Group({ title, badge, hint, children }: { title: string; badge?: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-white/10 bg-card/50 p-3.5 space-y-3">
      <div className="text-xs font-semibold text-foreground/80 flex items-center gap-2 -mb-1">
        {title}
        {badge && (
          <span className="text-[9px] uppercase tracking-wider px-1.5 py-0.5 rounded-full bg-white/5 border border-white/10 text-muted-foreground font-medium">
            {badge}
          </span>
        )}
        {hint && <span className="ml-auto text-[11px] text-muted-foreground font-normal font-mono">{hint}</span>}
      </div>
      {children}
    </div>
  )
}

function Field({ icon: Icon, label, help, cli, children }: { icon?: React.ElementType; label: string; help?: string; cli?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <div className="text-[12px] flex items-center gap-1.5">
        {Icon && <Icon className="h-3.5 w-3.5 text-muted-foreground" />}
        <span className="font-medium">{label}</span>
        {help && <span className="text-[11px] text-muted-foreground">— {help}</span>}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex-1 min-w-0">{children}</div>
        {cli && (
          <code className="text-[10px] font-mono px-1.5 py-0.5 rounded bg-black/30 text-muted-foreground whitespace-nowrap">
            {cli}
          </code>
        )}
      </div>
    </div>
  )
}

function ChipRow({ children }: { children: React.ReactNode }) {
  return <div className="flex flex-wrap gap-1.5">{children}</div>
}

function Chip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "px-2.5 py-1 rounded-md text-[11.5px] border transition-colors",
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

  if (active || editing) {
    return (
      <div className={cn(
        "inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-[11.5px]",
        active ? "bg-blue-500/20 border-blue-400" : "bg-card border-white/10",
      )}>
        <input
          type="number"
          autoFocus={editing}
          value={draft}
          min={min}
          max={max}
          step={step}
          onChange={(e) => setDraft(e.target.value)}
          onBlur={() => {
            const n = Number(draft)
            if (!Number.isNaN(n) && n >= min && n <= max) onChange(n)
            else setDraft(String(value))
            setEditing(false)
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter") (e.target as HTMLInputElement).blur()
            if (e.key === "Escape") { setDraft(String(value)); setEditing(false) }
          }}
          className="w-14 bg-transparent outline-none text-right text-blue-300 font-medium"
        />
        <span className="text-[10px] text-muted-foreground">{suffix}</span>
      </div>
    )
  }

  return (
    <button
      type="button"
      onClick={() => { setDraft(String(value)); setEditing(true) }}
      className="px-2.5 py-1 rounded-md text-[11.5px] border border-white/10 bg-card text-foreground/70 hover:border-white/20"
    >
      Custom…
    </button>
  )
}

function SegBtn({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "px-3 py-1 text-xs rounded-md transition-colors",
        active ? "bg-blue-500/20 text-blue-300" : "text-muted-foreground hover:text-foreground/80",
      )}
    >
      {children}
    </button>
  )
}

function DomainChips({ value, onChange }: { value: string[]; onChange: (v: string[]) => void }) {
  const [draft, setDraft] = useState("")

  const commit = () => {
    const trimmed = draft.trim().toLowerCase()
    if (!trimmed) return
    if (value.includes(trimmed)) { setDraft(""); return }
    onChange([...value, trimmed])
    setDraft("")
  }

  return (
    <div className="flex flex-wrap gap-1.5 p-2 bg-zinc-950 border border-white/15 rounded-md min-h-[40px] focus-within:border-blue-400">
      {value.map((d) => (
        <span key={d} className="inline-flex items-center gap-1 pl-2 pr-1 py-0.5 rounded-full bg-white/5 font-mono text-[11px] text-foreground/80">
          {d}
          <button type="button" onClick={() => onChange(value.filter((x) => x !== d))} className="text-muted-foreground hover:text-red-400 px-0.5">
            <X className="h-3 w-3" />
          </button>
        </span>
      ))}
      <input
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
        placeholder={value.length === 0 ? "github.com, *.npmjs.org, api.anthropic.com…" : "add another…"}
        className="flex-1 min-w-[120px] bg-transparent border-0 outline-none text-xs font-mono px-1 py-0.5"
      />
    </div>
  )
}
