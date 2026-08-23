"use client"

import { useMemo } from "react"
import {
  AlertTriangle, FolderCog, HeartPulse, KeyRound, Plus, ShieldAlert, ShieldCheck,
  Terminal, Trash2,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  CreateSurfaceNotice,
  CreateSurfaceSection,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"
import { cn } from "@/lib/utils"
import {
  KNOWN_CAPS,
  isAllowedMountSource,
  isServerGrantableCap,
  type MountEntry,
} from "./runtime-config-data"

// Structured, labeled controls for the highest-blast-radius container
// escape hatches (#1380): privileged, capAdd, extra mounts, the docker
// --init reaper, extra containerEnv, and the start hook (init script).
// A controlled component — parent (runtime-config.tsx) owns the JSON
// (de)serialization and keeps a raw-JSON escape hatch for anything this
// UI does not model.

export interface SecurityConfigValue {
  privileged: boolean
  init: boolean
  capAdd: string[]
  mounts: MountEntry[]
  containerEnv: Record<string, string>
  /** postStartCommand — the "init script" that runs on every container start. */
  postStartCommand: string
}

interface RuntimeSecurityConfigProps {
  value: SecurityConfigValue
  onChange: (value: SecurityConfigValue) => void
  /** When false the privileged toggle is read-only (non-admin, or the
   *  workspace has not opted into allow_privileged_credentials). */
  canEditPrivileged?: boolean
}

export function RuntimeSecurityConfig({
  value,
  onChange,
  canEditPrivileged = false,
}: RuntimeSecurityConfigProps) {
  const patch = (p: Partial<SecurityConfigValue>) => onChange({ ...value, ...p })

  const capSet = useMemo(() => new Set(value.capAdd), [value.capAdd])

  // Caps stored on the crew that this UI has no row for. Kept aside so a
  // toggle doesn't silently drop an operator's hand-written capability the way
  // a plain KNOWN_CAPS filter would.
  const unknownCaps = useMemo(
    () => value.capAdd.filter((c) => !KNOWN_CAPS.some((k) => k.name === c)),
    [value.capAdd],
  )

  function toggleCap(name: string) {
    const next = new Set(capSet)
    if (next.has(name)) next.delete(name)
    else next.add(name)
    // Preserve KNOWN_CAPS declaration order for a stable, diff-friendly JSON,
    // then re-append anything we don't model.
    patch({
      capAdd: [
        ...KNOWN_CAPS.filter((c) => next.has(c.name)).map((c) => c.name),
        ...unknownCaps,
      ],
    })
  }

  function updateMount(i: number, m: Partial<MountEntry>) {
    const mounts = value.mounts.map((row, idx) => (idx === i ? { ...row, ...m } : row))
    patch({ mounts })
  }

  function addMount() {
    patch({ mounts: [...value.mounts, { source: "", target: "", type: "bind", readonly: false }] })
  }

  function removeMount(i: number) {
    patch({ mounts: value.mounts.filter((_, idx) => idx !== i) })
  }

  const envRows = useMemo(() => Object.entries(value.containerEnv), [value.containerEnv])

  // What the grid shows: the capabilities the server will actually accept,
  // plus anything already stored on this crew — a legacy cap saved before the
  // gate landed has to stay on screen or it cannot be unchecked.
  const shownCaps = useMemo(
    () => KNOWN_CAPS.filter((c) => isServerGrantableCap(c.name) || capSet.has(c.name)),
    [capSet],
  )
  const hiddenCaps = useMemo(
    () => KNOWN_CAPS.filter((c) => !isServerGrantableCap(c.name) && !capSet.has(c.name)),
    [capSet],
  )

  function updateEnv(oldKey: string, key: string, val: string) {
    const next: Record<string, string> = {}
    for (const [k, v] of Object.entries(value.containerEnv)) {
      if (k === oldKey) {
        if (key) next[key] = val
      } else {
        next[k] = v
      }
    }
    if (oldKey === "" && key) next[key] = val
    patch({ containerEnv: next })
  }

  function addEnv() {
    // Stage an empty key; committed once the operator types a name.
    patch({ containerEnv: { ...value.containerEnv, "": "" } })
  }

  function removeEnv(key: string) {
    const next = { ...value.containerEnv }
    delete next[key]
    patch({ containerEnv: next })
  }

  return (
    <div className="flex flex-col gap-4">
      {/* Isolation.
       *
       * The danger notice used to render permanently, in destructive red,
       * above a switch that was OFF — five lines of warning about something
       * not happening. A warning that is always on screen is furniture, and
       * furniture is what people stop reading. It appears when the thing it
       * warns about is actually switched on. */}
      <CreateSurfaceSection
        title="Isolation"
        icon={value.privileged ? ShieldAlert : ShieldCheck}
        accent={value.privileged ? "red" : "green"}
      >
        <CreateSurfaceToggleRow
          icon={ShieldAlert}
          accent="red"
          label={
            <label htmlFor="rc-privileged" className="flex cursor-pointer flex-wrap items-center gap-2 max-sm:min-h-12">
              Privileged mode
              {value.privileged && (
                <Badge variant="destructive" className="gap-1 text-[10px]">
                  <ShieldAlert className="h-3 w-3" />
                  Isolation reduced
                </Badge>
              )}
            </label>
          }
          hint="Full host device access. An agent in a privileged container can reach the host."
          control={
            <label
              htmlFor="rc-privileged"
              className="flex cursor-pointer items-center justify-center max-sm:min-h-12 max-sm:min-w-12"
            >
              <Switch
                id="rc-privileged"
                aria-label="Privileged mode"
                checked={value.privileged}
                disabled={!canEditPrivileged}
                onCheckedChange={(v) => patch({ privileged: v })}
              />
            </label>
          }
        />

        {value.privileged && (
          <CreateSurfaceNotice tone="error" icon={AlertTriangle}>
            <span className="block font-medium">This removes container isolation.</span>
            Privileged mode nulls <code className="font-mono">no-new-privileges</code>, drops the
            read-only rootfs, and grants essentially all Linux capabilities and host device access.
            Only for a crew you fully trust (e.g. Docker-in-Docker) — a single added capability
            below is almost always the smaller answer.
          </CreateSurfaceNotice>
        )}

        {!canEditPrivileged && (
          <p className="text-[11px] text-muted-foreground">
            Requires an admin and the workspace{" "}
            <code className="font-mono">allow_privileged_credentials</code> flag to change.
          </p>
        )}

        <CreateSurfaceToggleRow
          icon={HeartPulse}
          accent="slate"
          label={
            <label htmlFor="rc-init" className="flex cursor-pointer items-center max-sm:min-h-12">
              Init process (PID 1)
            </label>
          }
          hint="Runs a tiny init as PID 1 so orphaned processes get reaped (docker --init)."
          control={
            <label
              htmlFor="rc-init"
              className="flex cursor-pointer items-center justify-center max-sm:min-h-12 max-sm:min-w-12"
            >
              <Switch
                id="rc-init"
                aria-label="Init process"
                checked={value.init}
                onCheckedChange={(v) => patch({ init: v })}
              />
            </label>
          }
        />
      </CreateSurfaceSection>

      {/* Capabilities. */}
      <CreateSurfaceSection
        title="Linux capabilities"
        icon={KeyRound}
        accent="amber"
        hint="one permission instead of all of them"
      >
        <p className="text-[11px] leading-relaxed text-muted-foreground">
          Only <code className="font-mono">NET_BIND_SERVICE</code> can be granted directly. The
          save path refuses anything broader, because a per-capability grant of e.g.{" "}
          <code className="font-mono">SYS_ADMIN</code> is a bigger escalation than the privileged
          flag it would be bypassing — use privileged mode for those.
        </p>
        <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2">
          {shownCaps.map((cap) => {
            const checked = capSet.has(cap.name)
            const grantable = isServerGrantableCap(cap.name)
            // A non-grantable cap stays interactive only while it's already set:
            // that's a legacy config saved before the gate landed, and the
            // operator needs to be able to UNcheck it. Otherwise it's inert, so
            // nobody builds a config the server is certain to refuse.
            const locked = !grantable && !checked
            return (
              <label
                key={cap.name}
                className={cn(
                  "flex items-start gap-2 rounded-md px-2 py-1.5 text-xs",
                  locked ? "opacity-55 cursor-not-allowed" : "cursor-pointer hover:bg-accent/30",
                  checked && "bg-accent/20",
                )}
              >
                <Checkbox
                  checked={checked}
                  disabled={locked}
                  onCheckedChange={() => toggleCap(cap.name)}
                  aria-label={cap.name}
                  className="mt-0.5"
                />
                <span className="min-w-0">
                  <span className="font-mono font-medium flex items-center gap-1 flex-wrap">
                    {cap.name}
                    {cap.danger && (
                      <span className="text-[9px] px-1 rounded bg-destructive/15 text-destructive">
                        high-risk
                      </span>
                    )}
                    {!grantable && (
                      <span className="text-[9px] px-1 rounded bg-muted text-muted-foreground">
                        privileged only
                      </span>
                    )}
                  </span>
                  <span className="block text-[10px] text-muted-foreground">
                    {cap.description}
                  </span>
                  {!grantable && checked && (
                    <span className="block text-[10px] text-destructive">
                      Stored on this crew but no longer accepted — saving with it set
                      is rejected. Uncheck it, or switch to privileged mode.
                    </span>
                  )}
                </span>
              </label>
            )
          })}
        </div>

        {/* The other thirteen.
         *
         * Every capability except NET_BIND_SERVICE is refused by the save
         * path, so listing them all inline made this section thirteen rows of
         * greyed-out things you cannot do and one you can. They stay
         * reachable — knowing WHY SYS_ADMIN is not on offer is the point —
         * but folded, and any capability already stored on the crew is shown
         * above regardless, because it has to be possible to remove it. */}
        {hiddenCaps.length > 0 && (
          <details className="rounded-lg border border-hairline bg-foreground/[0.02]">
            <summary className="cursor-pointer px-3 py-2 text-[11px] text-muted-foreground marker:text-muted-foreground-soft">
              {hiddenCaps.length} more the save path refuses — privileged mode only
            </summary>
            <ul className="space-y-1 px-3 pb-2.5 text-[11px] text-muted-foreground">
              {hiddenCaps.map((cap) => (
                <li key={cap.name} className="flex flex-wrap items-baseline gap-x-1.5">
                  <span className="font-mono text-foreground/70">{cap.name}</span>
                  {cap.danger && (
                    <span className="rounded bg-destructive/15 px-1 text-[9px] text-destructive">high-risk</span>
                  )}
                  <span>{cap.description}</span>
                </li>
              ))}
            </ul>
          </details>
        )}
      </CreateSurfaceSection>

      {/* Mounts. */}
      <CreateSurfaceSection
        title="Extra mounts"
        icon={FolderCog}
        accent="blue"
        hint="/dev/fuse and named volumes only"
      >
        <div className="flex items-start justify-between gap-3">
          <p className="text-[11px] leading-relaxed text-muted-foreground">
            The Docker socket and host paths are rejected — they are a container-escape primitive.
          </p>
          <Button size="sm" variant="outline" className="h-7 shrink-0 text-xs" onClick={addMount}>
            <Plus className="mr-1 h-3 w-3" />
            Add mount
          </Button>
        </div>

        {value.mounts.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">No extra mounts.</p>
        ) : (
          <div className="space-y-2">
            {value.mounts.map((m, i) => {
              const invalid = m.source.trim() !== "" && !isAllowedMountSource(m.source.trim())
              return (
                <div key={i} className="space-y-1 rounded-md border border-border/40 p-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <Input
                      aria-label="Mount source"
                      placeholder="/dev/fuse or volume-name"
                      value={m.source}
                      onChange={(e) => updateMount(i, { source: e.target.value })}
                      className={cn("h-7 flex-1 min-w-[140px] text-xs font-mono", invalid && "border-destructive")}
                    />
                    <span className="text-muted-foreground text-xs">→</span>
                    <Input
                      aria-label="Mount target"
                      placeholder="/dev/fuse"
                      value={m.target}
                      onChange={(e) => updateMount(i, { target: e.target.value })}
                      className="h-7 flex-1 min-w-[140px] text-xs font-mono"
                    />
                    <label className="flex items-center gap-1 text-[11px]">
                      <Checkbox
                        checked={Boolean(m.readonly)}
                        onCheckedChange={(v) => updateMount(i, { readonly: Boolean(v) })}
                        aria-label={`Mount ${i} read-only`}
                      />
                      ro
                    </label>
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 w-7 p-0"
                      onClick={() => removeMount(i)}
                      aria-label={`Remove mount ${i}`}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                  {invalid && (
                    <p className="text-[10px] text-destructive">
                      Source <code>{m.source}</code> is not allowed — use{" "}
                      <code>/dev/fuse</code> or a named volume.
                    </p>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </CreateSurfaceSection>

      {/* Container env. The init toggle that used to sit here moved up into
          Isolation, beside the other switch that changes how the container
          runs rather than what is in it. */}
      <CreateSurfaceSection
        title="Container environment"
        icon={Terminal}
        accent="slate"
        hint="CREWSHIP_* keys are reserved"
      >
        <div className="flex items-start justify-between gap-3">
          <p className="text-[11px] leading-relaxed text-muted-foreground">
            Extra environment variables injected at container start.
          </p>
          <Button size="sm" variant="outline" className="h-7 shrink-0 text-xs" onClick={addEnv}>
            <Plus className="mr-1 h-3 w-3" />
            Add var
          </Button>
        </div>
        {envRows.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">No extra environment variables.</p>
        ) : (
          <div className="space-y-1.5">
            {envRows.map(([k, v], i) => (
              <div key={i} className="flex items-center gap-2">
                <Input
                  aria-label={`Env name ${i}`}
                  placeholder="NAME"
                  value={k}
                  onChange={(e) => updateEnv(k, e.target.value, v)}
                  className="h-7 w-40 text-xs font-mono"
                />
                <span className="text-muted-foreground text-xs">=</span>
                <Input
                  aria-label={`Env value ${i}`}
                  placeholder="value"
                  value={v}
                  onChange={(e) => updateEnv(k, k, e.target.value)}
                  className="h-7 flex-1 text-xs font-mono"
                />
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 w-7 p-0"
                  onClick={() => removeEnv(k)}
                  aria-label={`Remove env ${i}`}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </CreateSurfaceSection>

      {/* Start hook. */}
      <CreateSurfaceSection title="Start hook" icon={Terminal} accent="purple" hint="runs on every start">
        <Label htmlFor="rc-start-hook" className="sr-only">Start hook (init script)</Label>
        <p className="text-[11px] leading-relaxed text-muted-foreground">
          Shell commands run on every container start as the agent user. Note the
          crew&apos;s <code>/crew</code> directory is an agent-writable host bind that
          survives container removal — treat anything auto-executed there as code you
          wrote or audited.
        </p>
        <Textarea
          id="rc-start-hook"
          aria-label="Start hook init script"
          value={value.postStartCommand}
          onChange={(e) => patch({ postStartCommand: e.target.value })}
          placeholder={"npm ci\n./scripts/warm-cache.sh"}
          className="font-mono text-xs min-h-[80px] resize-y"
        />
      </CreateSurfaceSection>
    </div>
  )
}
