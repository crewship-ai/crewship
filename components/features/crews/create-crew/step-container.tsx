"use client"

import { useMemo } from "react"
import dynamic from "next/dynamic"
import { Clock, Cpu, Globe, HardDrive, MemoryStick, Network, Package, ShieldCheck, TriangleAlert } from "lucide-react"
import {
  CreateSurfaceDisclosure,
  CreateSurfaceGrid,
  CreateSurfaceNotice,
  CreateSurfaceSection,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"
import { Switch } from "@/components/ui/switch"
import { useAbilities } from "@/hooks/use-abilities"
import { PACKAGE_REGISTRY_DOMAINS, mergeDomains } from "../registry-presets"
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

/**
 * Everything about the box the crew runs in, on one step.
 *
 * This used to be two: a Runtime step that opened on CPU, memory and an
 * allowlist, and a Container step underneath it. Three things were wrong with
 * that and all three are product decisions, recorded here because the code no
 * longer shows what it used to be:
 *
 *  · **Sizing led the step.** It is an administrator's question — the defaults
 *    hold two agents and the server already returns sizing advisories — so it
 *    is folded away under a summary rather than being the first thing a new
 *    user is asked to have an opinion about.
 *  · **Egress defaulted to an allowlist** that is still maturing. A
 *    half-working allowlist fails as a silent timeout deep inside a run, which
 *    is the worst failure shape a platform has. Open is the default and the
 *    allowlist is one switch away, because "we will throttle later" only works
 *    if the throttle is already built.
 *  · **MCP had a card of its own.** Tools reach agents through Composio and
 *    the integrations surface now; a crew-level MCP editor in the create path
 *    was a second way to say the same thing, and the one nobody uses.
 *
 * Base image and preinstalled tooling still come from `RuntimeConfig`, which
 * reads the real `/api/v1/features/catalog` and already brand-colours the
 * image icons — the chrome around it changed, not the capability inside it.
 */
export function StepContainer({ state, setState }: Props) {
  const { role } = useAbilities()
  const canEditPrivileged = role === "OWNER" || role === "ADMIN"

  return (
    <div className="flex flex-col gap-4">
      <CreateSurfaceSection
        title="Image and tooling"
        icon={HardDrive}
        accent="teal"
        hint="Optional. The defaults run debian:bookworm-slim with nothing added."
      >
        <RuntimeConfig
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
          // Network and Size sit under this on the same step. At the
          // component's own 420px both landed roughly two screens down, which
          // is the "where did it go" the old two-step wizard had for other
          // reasons.
          browserHeight="240px"
        />
      </CreateSurfaceSection>

      <NetworkSection state={state} setState={setState} />
      <SizeDisclosure state={state} setState={setState} />
    </div>
  )
}

// =============================================================================
// Egress
// =============================================================================

function NetworkSection({ state, setState }: Props) {
  const restricted = state.networkMode === "restricted"

  return (
    <CreateSurfaceSection title="Network" icon={Network} accent="purple" hint="Outbound HTTP from the container.">
      <CreateSurfaceToggleRow
        icon={restricted ? ShieldCheck : Globe}
        accent={restricted ? "green" : "amber"}
        label={restricted ? "Allowlist" : "Open egress"}
        hint={
          restricted
            ? "Only the listed hosts, plus the provider APIs the sidecar always permits."
            : "The container reaches any host."
        }
        control={
          <Switch
            checked={restricted}
            onCheckedChange={(on) => setState(on ? { networkMode: "restricted" } : { networkMode: "free", allowedDomains: [] })}
            aria-label="Restrict egress to an allowlist"
          />
        }
      />

      {restricted && (
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
              An empty allowlist locks all egress. Add at least one host unless that is what you mean.
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

function SizeDisclosure({ state, setState }: Props) {
  const summary = useMemo(() => {
    const ttl = state.ttlHours == null ? "no auto-stop" : `stops after ${state.ttlHours} h`
    return `${state.cpus} ${state.cpus === 1 ? "core" : "cores"} · ${prettyMemory(state.memoryMB)} · ${ttl}`
  }, [state.cpus, state.memoryMB, state.ttlHours])

  return (
    <CreateSurfaceDisclosure icon={Cpu} accent="slate" label="Size" summary={summary}>
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
