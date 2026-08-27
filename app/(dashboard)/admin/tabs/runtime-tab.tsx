import React from "react"
import { RefreshCw, AlertTriangle, ExternalLink } from "lucide-react"
import { StatusBadge } from "@/components/ui/status-badge"
import { Button } from "@/components/ui/button"
import { SettingsCard } from "@/components/features/settings/shared"
import { SecurityPostureCard } from "@/components/features/admin/security-posture-card"
import { MemoryConfigCard } from "@/components/features/admin/memory-config-card"
import { RuntimeIcon, runtimeBrand } from "@/components/icons/runtime-icons"
import { cn } from "@/lib/utils"

/** One crew hardening control the runtime in use is measured not to deliver. */
export interface RuntimeGap {
  /** The control that is dropped, e.g. "GroupAdd". */
  control: string
  /** What breaks because of it, in an operator's terms. */
  detail: string
}

/** One container runtime present on the host, as GET /api/v1/system/runtime reports it. */
export interface RuntimeEntry {
  runtime: string
  version: string
  socket: string
  /**
   * True for the single runtime this server actually connected to. No entry
   * carries it when the server has no container provider at all — a runtime
   * being installed and a runtime being used are different facts.
   */
  in_use: boolean
  /**
   * Controls this runtime will not honour (#1672). The server sends them on the
   * `in_use` entry only and omits the key entirely when there are none, so this
   * is optional twice over — an older server does not send it at all.
   */
  gaps?: RuntimeGap[]
}

interface RuntimeTabProps {
  runtimeChecking: boolean
  runtimeAvailable: boolean | null
  allRuntimes: RuntimeEntry[]
  runtimeInstallLinks: Record<string, string>
  onCheckRuntime: () => void
  /** Scope for the two workspace-scoped cards below. */
  workspaceId: string | null
}

// This panel deliberately does NOT poll. Every request behind it re-probes
// every candidate socket — there is no cache — and while that is cheap
// (single-digit milliseconds, concurrent, bounded by the request context) it is
// a cost per open admin tab for information that changes when someone installs
// a runtime, i.e. never on its own. It loads with the tab and refreshes when
// the operator asks. See the note on SystemHandler.inventory.

export const RuntimeTab = React.memo(function RuntimeTab({
  runtimeChecking,
  runtimeAvailable,
  allRuntimes,
  runtimeInstallLinks,
  onCheckRuntime,
  workspaceId,
}: RuntimeTabProps) {
  // The runtime in use goes first, wherever the server listed it. The server's
  // order is candidate-probe order, which is not a ranking and is not what an
  // operator opening this panel is looking for.
  const ordered = React.useMemo(
    () => [...allRuntimes].sort((a, b) => Number(b.in_use) - Number(a.in_use)),
    [allRuntimes],
  )
  const anyInUse = ordered.some((rt) => rt.in_use)
  const present = React.useMemo(
    () => new Set(allRuntimes.map((rt) => rt.runtime)),
    [allRuntimes],
  )
  const missing = Object.entries(runtimeInstallLinks).filter(([key]) => !present.has(key))

  return (
    <div className="space-y-4">
      <SettingsCard
        title="Container runtimes"
        description="Every container runtime detected on this host, and the one Crewship is driving."
        actions={
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={onCheckRuntime}
            disabled={runtimeChecking}
          >
            <RefreshCw className={cn("mr-1.5 h-3 w-3", runtimeChecking && "animate-spin")} />
            Re-detect
          </Button>
        }
        padded
      >
        <div className="space-y-3">
          {runtimeChecking && (
            <div className="flex items-center gap-2">
              <RefreshCw className="h-3 w-3 animate-spin text-muted-foreground" />
              <span className="text-xs text-muted-foreground">Detecting runtimes…</span>
            </div>
          )}

          {!runtimeChecking && runtimeAvailable && ordered.length > 0 && (
            <>
              <div className="flex flex-col gap-2">
                {ordered.map((rt) => {
                  const brand = runtimeBrand(rt.runtime)
                  return (
                    <div key={rt.runtime + rt.socket} className="flex flex-col gap-1">
                      <div
                        data-testid={`runtime-row-${rt.runtime}`}
                        className={cn(
                          "flex items-center gap-3 rounded-lg border px-3 py-2",
                          rt.in_use
                            ? "border-border bg-white/[0.04]"
                            : "border-border/60 bg-white/[0.02]",
                        )}
                      >
                        <span
                          data-testid="runtime-icon"
                          className="flex h-6 w-6 shrink-0 items-center justify-center"
                        >
                          <RuntimeIcon runtime={rt.runtime} className="h-4 w-4" />
                        </span>
                        <div className="min-w-0 flex-1">
                          <div className="flex items-baseline gap-2 text-xs">
                            <span className="font-medium">{brand.label}</span>
                            {rt.version && (
                              <span className="font-mono text-muted-foreground">{rt.version}</span>
                            )}
                          </div>
                          {rt.socket && (
                            <p className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground">
                              {rt.socket}
                            </p>
                          )}
                        </div>
                        <StatusBadge
                          status={rt.in_use ? "COMPLETED" : "PENDING"}
                          label={rt.in_use ? "In use" : "Detected"}
                          className="text-[10px]"
                        />
                      </div>
                      {/*
                        Attached to the row rather than collected into one
                        notice at the foot of the card: a gap is a property of a
                        specific daemon at a specific version, and detaching it
                        from the row that names them is how it stops being
                        actionable. Only the in_use entry ever carries any.
                      */}
                      {rt.gaps && rt.gaps.length > 0 && (
                        <div
                          data-testid={`runtime-gaps-${rt.runtime}`}
                          className="rounded-lg border border-warn/40 bg-warn/5 px-3 py-2 text-[11px] text-warn"
                        >
                          {rt.gaps.map((gap) => (
                            <p key={gap.control} className="flex items-start gap-1.5">
                              <AlertTriangle className="mt-[1px] h-3 w-3 shrink-0" />
                              <span>
                                <span className="font-mono font-medium">{gap.control}</span> is not
                                honoured — {gap.detail}
                              </span>
                            </p>
                          ))}
                        </div>
                      )}
                    </div>
                  )
                })}
              </div>

              {!anyInUse && (
                <p
                  data-testid="runtime-none-in-use"
                  className="rounded-lg border border-warn/40 bg-warn/5 px-3 py-2 text-[11px] text-warn"
                >
                  No container runtime in use. These are installed, but this server started
                  without a container provider — with <code>--no-docker</code>, or because the
                  provider failed to start. Agents cannot run until it has one.
                </p>
              )}

              {/*
                The honesty requirement (#1690). `container.provider` accepts
                only docker, apple or auto — there is no value for orbstack,
                colima, rancher or podman, so nothing above is a choice the
                operator can make here. Saying "the rest are what you could
                switch to" is the mistake the CLI made (#1689) and it is not
                repeated in pixels. These two levers are the real ones.
              */}
              <p
                data-testid="runtime-switch-note"
                className="text-[11px] leading-relaxed text-muted-foreground"
              >
                Crewship drives one runtime at a time, and this is a report rather than a
                setting. The Docker-compatible runtimes above all answer the same API, and
                Crewship uses whichever socket answers first: to change that, point{" "}
                <code className="font-mono">DOCKER_HOST</code> at the daemon you want, or stop
                the one currently winning. Apple Containers is a separate provider, selected
                with <code className="font-mono">container.provider</code>.
              </p>
            </>
          )}

          {!runtimeChecking && !runtimeAvailable && (
            <div className="flex items-center gap-3">
              <AlertTriangle className="h-4 w-4 shrink-0 text-warn" />
              <div className="min-w-0">
                <div className="text-xs font-medium">No runtime detected</div>
                <p className="text-[11px] text-muted-foreground">
                  Install a container runtime to enable agent containers.
                </p>
              </div>
            </div>
          )}

          {!runtimeChecking && missing.length > 0 && (
            <div className="space-y-2">
              {runtimeAvailable && ordered.length > 0 && (
                <p className="text-[11px] text-muted-foreground">
                  Also supported, not installed here:
                </p>
              )}
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                {missing.map(([key, url]) => (
                  <a
                    key={key}
                    data-testid={`runtime-install-${key}`}
                    href={url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex items-center gap-2 rounded-lg border border-border/60 bg-white/[0.02] px-3 py-2 text-xs transition-colors hover:border-border hover:bg-white/[0.04]"
                  >
                    <RuntimeIcon runtime={key} className="h-3.5 w-3.5 shrink-0" />
                    <span className="font-medium">{runtimeBrand(key).label}</span>
                    <ExternalLink className="ml-auto h-2.5 w-2.5 text-muted-foreground" />
                  </a>
                ))}
              </div>
            </div>
          )}
        </div>
      </SettingsCard>

      {/* #1379 — the env-driven instance flags an admin otherwise had to SSH in
        to read. Read-only: these are deploy decisions, not app settings. */}
      <SecurityPostureCard workspaceId={workspaceId} />

      {/* #1379 — the memory retention window. The endpoint existed so this
        wouldn't need a SQLite edit; nothing rendered it until now. */}
      <MemoryConfigCard workspaceId={workspaceId} />
    </div>
  )
})
