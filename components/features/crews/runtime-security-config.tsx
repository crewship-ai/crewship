"use client"

import { useMemo } from "react"
import { AlertTriangle, Plus, ShieldAlert, Trash2 } from "lucide-react"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { cn } from "@/lib/utils"
import {
  KNOWN_CAPS,
  isAllowedMountSource,
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

  function toggleCap(name: string) {
    const next = new Set(capSet)
    if (next.has(name)) next.delete(name)
    else next.add(name)
    // Preserve KNOWN_CAPS declaration order for a stable, diff-friendly JSON.
    patch({ capAdd: KNOWN_CAPS.filter((c) => next.has(c.name)).map((c) => c.name) })
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
    <div className="space-y-5">
      {/* ---- Privileged ---- */}
      <div className="space-y-2">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0">
            <Label htmlFor="rc-privileged" className="text-xs font-medium">
              Privileged mode
            </Label>
            <p className="text-[11px] text-muted-foreground">
              Runs the container with full host device access.
            </p>
          </div>
          <div className="flex items-center gap-2 shrink-0">
            {value.privileged && (
              <Badge variant="destructive" className="gap-1 text-[10px]">
                <ShieldAlert className="h-3 w-3" />
                Isolation reduced
              </Badge>
            )}
            <Switch
              id="rc-privileged"
              aria-label="Privileged mode"
              checked={value.privileged}
              disabled={!canEditPrivileged}
              onCheckedChange={(v) => patch({ privileged: v })}
            />
          </div>
        </div>

        <Alert variant="destructive" className="py-2">
          <AlertTriangle className="h-4 w-4" />
          <AlertTitle className="text-xs">Danger — this removes container isolation</AlertTitle>
          <AlertDescription className="text-[11px]">
            Privileged mode nulls <code>no-new-privileges</code>, drops the read-only
            rootfs, and grants essentially all Linux capabilities and host device
            access. An agent in a privileged container can escape to the host. Only
            enable it for a crew you fully trust (e.g. Docker-in-Docker), and prefer
            adding a single capability below instead.
          </AlertDescription>
        </Alert>

        {!canEditPrivileged && (
          <p className="text-[11px] text-muted-foreground">
            Requires an admin and the workspace{" "}
            <code>allow_privileged_credentials</code> flag to change.
          </p>
        )}
      </div>

      {/* ---- Capabilities ---- */}
      <div className="space-y-2">
        <div>
          <Label className="text-xs font-medium">Added Linux capabilities</Label>
          <p className="text-[11px] text-muted-foreground">
            Re-add a single capability instead of going privileged. Only{" "}
            <code>NET_BIND_SERVICE</code> is auto-allowed for community features;
            the rest are operator-only escape hatches.
          </p>
        </div>
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-1.5 rounded-md border border-border/40 p-2">
          {KNOWN_CAPS.map((cap) => {
            const checked = capSet.has(cap.name)
            return (
              <label
                key={cap.name}
                className={cn(
                  "flex items-start gap-2 rounded-md px-2 py-1.5 text-xs cursor-pointer hover:bg-accent/30",
                  checked && "bg-accent/20",
                )}
              >
                <Checkbox
                  checked={checked}
                  onCheckedChange={() => toggleCap(cap.name)}
                  aria-label={cap.name}
                  className="mt-0.5"
                />
                <span className="min-w-0">
                  <span className="font-mono font-medium flex items-center gap-1">
                    {cap.name}
                    {cap.danger && (
                      <span className="text-[9px] px-1 rounded bg-destructive/15 text-destructive">
                        high-risk
                      </span>
                    )}
                  </span>
                  <span className="block text-[10px] text-muted-foreground">
                    {cap.description}
                  </span>
                </span>
              </label>
            )
          })}
        </div>
      </div>

      {/* ---- Mounts ---- */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <div>
            <Label className="text-xs font-medium">Extra mounts</Label>
            <p className="text-[11px] text-muted-foreground">
              Only <code>/dev/fuse</code> and named volumes are allowed. The Docker
              socket and host paths are rejected — they are a container-escape
              primitive.
            </p>
          </div>
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={addMount}>
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
      </div>

      {/* ---- Init (PID 1) ---- */}
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <Label htmlFor="rc-init" className="text-xs font-medium">
            Init process (PID 1)
          </Label>
          <p className="text-[11px] text-muted-foreground">
            Run a tiny init as PID 1 to reap zombie processes (docker <code>--init</code>).
          </p>
        </div>
        <Switch
          id="rc-init"
          aria-label="Init process"
          checked={value.init}
          onCheckedChange={(v) => patch({ init: v })}
        />
      </div>

      {/* ---- Container env ---- */}
      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <div>
            <Label className="text-xs font-medium">Container environment</Label>
            <p className="text-[11px] text-muted-foreground">
              Extra env vars injected at container start. <code>CREWSHIP_*</code> keys
              are reserved and ignored.
            </p>
          </div>
          <Button size="sm" variant="outline" className="h-7 text-xs" onClick={addEnv}>
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
      </div>

      {/* ---- Start hook (init script) ---- */}
      <div className="space-y-2">
        <Label htmlFor="rc-start-hook" className="text-xs font-medium">
          Start hook (init script)
        </Label>
        <p className="text-[11px] text-muted-foreground">
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
      </div>
    </div>
  )
}
