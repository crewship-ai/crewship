"use client"

import { useId, useMemo, useState } from "react"
import dynamic from "next/dynamic"
import { Clock, Cpu, HardDrive, MemoryStick, Network, Package, TriangleAlert } from "lucide-react"
import {
  CreateSurfaceDisclosure,
  CreateSurfaceGrid,
  CreateSurfaceNotice,
  CreateSurfaceSection,
} from "@/components/layout/create-surface"
import { useAbilities } from "@/hooks/use-abilities"
import { PACKAGE_REGISTRY_DOMAINS, mergeDomains } from "../registry-presets"
import { BaseImageRow } from "./base-image"
import { Chip, ChipRow, CustomNumberChip, DomainChips, prettyMemory } from "./runtime-controls"
import {
  CPU_PRESETS, CPU_MIN, CPU_MAX,
  MEMORY_PRESETS, MEMORY_MIN_MB, MEMORY_MAX_MB,
  TTL_PRESETS, type WizardState,
} from "./types"

// Code-split RuntimeConfig (881 lines + a 1308-row catalog fetch). Without
// this, every page that mounts CreateCrewDialog (e.g. /crews) pays for it in
// the initial bundle even when the user never opens the wizard.
const RuntimeConfig = dynamic(
  () => import("../runtime-config").then((m) => m.RuntimeConfig),
  { ssr: false, loading: () => <SectionSkeleton /> },
)

function SectionSkeleton() {
  return (
    <div className="py-6 text-center text-xs text-muted-foreground" role="status" aria-live="polite">
      Loading…
    </div>
  )
}

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

export type EnvironmentSection = "environment" | "tools" | "versions" | "network" | "limits"

interface StepProps extends Props {
  activeSection?: EnvironmentSection
  /** Opens the wizard's base-image panel. The catalogue is not on this step. */
  onPickImage: () => void
}

/** Shared environment controls: one creation step, or focused sections in Edit. */
export function StepContainer({ state, setState, onPickImage, activeSection }: StepProps) {
  const { role } = useAbilities()
  const canEditPrivileged = role === "OWNER" || role === "ADMIN"

  return (
    <div className="flex flex-col gap-4">
      {/* Base image: one row saying what the crew runs on, and a panel to
          change it in. The catalogue used to be nine radio rows inline, on a
          step that also carries tooling, network and sizing. */}
      <div hidden={!!activeSection && activeSection !== "environment"}><CreateSurfaceSection title="Base image" icon={HardDrive} accent="teal">
        <BaseImageRow state={state} onChange={onPickImage} />
        {activeSection && <p className="text-sm text-muted-foreground">The base operating system shared by every agent in this crew. Add packages under Tools, or choose specific language versions under Tool versions.</p>}
      </CreateSurfaceSection></div>

      {/* Sections, not a tab strip.
       *
       * This used to wrap RuntimeConfig whole inside one "Image and tooling"
       * section, so the step showed a create surface with a four-tab strip
       * inside it — a navigation model no other door has, and the reason
       * Container kept reading as a different product from the rest of the
       * wizard. `layout="sections"` renders the same controls as the two
       * sections docs/prd/create-surface-parity.md §6.3 leads with, plus
       * three disclosures for what it does
       * not show. Nothing is removed; see the note on the prop. */}
      <div hidden={!!activeSection && !["tools", "versions"].includes(activeSection)}><RuntimeConfig
        value={{
          runtimeImage: state.runtimeImage,
          devcontainerConfig: state.devcontainerConfig,
          miseConfig: state.miseConfig,
        }}
        onChange={(v) => setState({
          runtimeImage: v.runtimeImage,
          devcontainerConfig: v.devcontainerConfig,
          miseConfig: v.miseConfig,
        })}
        canEditPrivileged={canEditPrivileged}
        layout="sections"
        focusedSection={activeSection ? activeSection === "versions" ? "versions" : "tools" : undefined}
        // The row above owns the image; without this the catalogue would be
        // on the step twice.
        hideBaseImage
        // Network and Size sit under this on the same step. At the
        // component's own 420px both landed roughly two screens down, which
        // is the "where did it go" the old two-step wizard had for other
        // reasons.
        browserHeight={activeSection ? "360px" : "240px"}
      />

      </div>
      <div hidden={!!activeSection && activeSection !== "network"}><NetworkSection state={state} setState={setState} /></div>
      <div hidden={!!activeSection && activeSection !== "limits"}><SizeDisclosure state={state} setState={setState} defaultOpen={!!activeSection} /></div>
    </div>
  )
}

// =============================================================================
// Egress
// =============================================================================

function NetworkSection({ state, setState }: Props) {
  const modeId = useId()
  const [editingHosts, setEditingHosts] = useState(false)
  const restricted = state.networkMode === "restricted"
  const mode = !restricted ? "free" : state.allowedDomains.length || editingHosts ? "hosts" : "providers"
  return (
    <CreateSurfaceSection title="Network" icon={Network} accent="purple" hint="Connections allowed for every agent in this crew.">
      <div role="radiogroup" aria-label="Network access" className="space-y-2">
        {[
          { value: "providers", label: "Provider APIs only", hint: "Block general internet access; keep the connections needed to run AI models." },
          { value: "hosts", label: "Selected hosts", hint: "Allow provider APIs and the domains you list below." },
          { value: "free", label: "Open network", hint: "Allow internet, private network and localhost. Cloud metadata stays blocked." },
        ].map(option => <label key={option.value} className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 ${mode === option.value ? "border-primary bg-primary/10" : "border-border hover:bg-muted/40"}`}>
          <input type="radio" name={modeId} value={option.value} checked={mode === option.value} onChange={() => {
            setEditingHosts(option.value === "hosts")
            setState(option.value === "free" ? { networkMode: "free" } : option.value === "providers" ? { networkMode: "restricted", allowedDomains: [] } : { networkMode: "restricted" })
          }} className="mt-1 accent-primary" />
          <span><span className="block text-sm font-medium">{option.label}</span><span className="mt-1 block text-xs text-muted-foreground">{option.hint}</span></span>
        </label>)}
      </div>
      <p className="text-sm text-muted-foreground">{mode === "free" ? "This is the broadest network access available in Crewship." : mode === "providers" ? "Only the provider APIs and platform connections always permitted by Crewship. This is not a fully offline mode." : "Only the listed hosts, plus the provider APIs and platform connections always permitted by Crewship."}</p>
      {mode === "hosts" && (
        <div className="flex flex-col gap-1.5">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <span className="text-[11px] text-muted-foreground">
              Allowed hosts — wildcards work (<code className="font-mono">*.github.com</code>)
            </span>
            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={() => setState({ allowedDomains: mergeDomains(state.allowedDomains, PACKAGE_REGISTRY_DOMAINS) })}
                className="inline-flex items-center gap-1 rounded border border-hairline bg-card/60 px-1.5 py-0.5 text-[10px] text-foreground/80 hover:border-white/30 hover:text-foreground"
              >
                <Package className="h-3 w-3" aria-hidden="true" />
                Allow package registries
              </button>
              <span className="text-[10px] text-muted-foreground">{state.allowedDomains.length} listed</span>
            </div>
          </div>
          <DomainChips value={state.allowedDomains} onChange={(v) => setState({ allowedDomains: v })} />
          {state.allowedDomains.length === 0 && (
            <CreateSurfaceNotice tone="warn" icon={TriangleAlert}>
              No extra hosts are allowed yet. Provider APIs and platform connections remain available.
            </CreateSurfaceNotice>
          )}
        </div>
      )}
    </CreateSurfaceSection>
  )
}

// =============================================================================
// Sizing — folded away, because it is an administrator's question
// =============================================================================

function SizeDisclosure({ state, setState, defaultOpen = false }: Props & { defaultOpen?: boolean }) {
  const summary = useMemo(() => {
    const ttl = state.ttlHours == null ? "no auto-stop" : `stops after ${state.ttlHours} h`
    return `${state.cpus} ${state.cpus === 1 ? "core" : "cores"} · ${prettyMemory(state.memoryMB)} · ${ttl}`
  }, [state.cpus, state.memoryMB, state.ttlHours])

  return (
    <CreateSurfaceDisclosure icon={Cpu} accent="slate" label="Size" summary={summary} defaultOpen={defaultOpen}>
      <CreateSurfaceGrid>
        <SizeField icon={MemoryStick} label="Memory" help="Hard limit" cli={`--memory-mb ${state.memoryMB}`}>
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
              min={MEMORY_MIN_MB}
              max={MEMORY_MAX_MB}
              suffix="MB"
            />
          </ChipRow>
        </SizeField>

        <SizeField icon={Cpu} label="CPUs" help="Fractional cores OK" cli={`--cpus ${state.cpus}`}>
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
              min={CPU_MIN}
              max={CPU_MAX}
              step={0.5}
              suffix="cores"
            />
          </ChipRow>
        </SizeField>
      </CreateSurfaceGrid>

      <SizeField icon={Clock} label="Auto-stop" help="Saves cost" cli={`--ttl ${state.ttlHours ?? 0}`}>
        <ChipRow>
          {TTL_PRESETS.map((p) => (
            <Chip key={String(p.value)} active={state.ttlHours === p.value} onClick={() => setState({ ttlHours: p.value })}>
              {p.label}
            </Chip>
          ))}
        </ChipRow>
      </SizeField>

      {state.memoryMB < 4096 && (
        <CreateSurfaceNotice tone="warn" icon={TriangleAlert}>
          At {prettyMemory(state.memoryMB)} this crew cannot hold a second agent.
        </CreateSurfaceNotice>
      )}
    </CreateSurfaceDisclosure>
  )
}

function SizeField({
  icon: Icon, label, help, cli, children,
}: {
  icon: React.ElementType
  label: string
  help: string
  cli: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-baseline gap-2">
        <Icon className="h-3 w-3 shrink-0 self-center text-muted-foreground" aria-hidden="true" />
        <span className="text-[11px] font-medium text-foreground/85">{label}</span>
        <span className="truncate text-[10px] text-muted-foreground">— {help}</span>
      </div>
      {children}
      <code className="self-start truncate rounded bg-black/30 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
        {cli}
      </code>
    </div>
  )
}
