import React from "react"
import { RefreshCw, Radio, Shield } from "lucide-react"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import {
  Sheet, SheetContent, SheetHeader, SheetTitle,
} from "@/components/ui/sheet"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { SettingsCard } from "@/components/features/settings/shared"
import { KeeperGovernancePanel } from "@/components/features/admin/keeper-governance-panel"
import { KeeperJudgeCard } from "@/components/features/admin/keeper-judge-card"
import { JudgeModelsCard } from "@/components/features/admin/judge-models-card"
import { cn } from "@/lib/utils"
import { redactSecrets } from "../utils"
import type { KeeperStatus, KeeperLogEntry } from "../types"
import type { KeeperLiveEvent, KeeperWsStatus } from "../hooks/use-admin-websocket"

interface KeeperTabProps {
  workspaceId: string | null
  keeperLoading: boolean
  keeperStatus: KeeperStatus | null
  keeperLog: KeeperLogEntry[]
  keeperLiveEvents: KeeperLiveEvent[]
  keeperWsStatus: KeeperWsStatus
  selectedKeeperEntry: KeeperLogEntry | null
  onSelectKeeperEntry: (entry: KeeperLogEntry | null) => void
  onRefresh: () => void
}

// Map keeper decisions → canonical StatusBadge keys
function decisionStatusKey(decision: string | null | undefined): string {
  switch (decision) {
    case "ALLOW":    return "COMPLETED"
    case "DENY":     return "FAILED"
    case "ESCALATE": return "BLOCKED"
    case "PENDING":  return "IN_PROGRESS"
    default:         return "PENDING"
  }
}

export const KeeperTab = React.memo(function KeeperTab({
  workspaceId,
  keeperLoading,
  keeperStatus,
  keeperLog,
  keeperLiveEvents,
  keeperWsStatus,
  selectedKeeperEntry,
  onSelectKeeperEntry,
  onRefresh,
}: KeeperTabProps) {
  return (
    <div className="space-y-5">
      {/* Order: what is happening → what has happened → how it is configured.
          It used to be the reverse, so an operator opening this page to check
          whether Keeper had denied anything overnight scrolled past six
          settings cards to find out. Configuration is the rarer visit; it now
          lives under a divider, below the monitoring it explains. */}

      {keeperLoading && <Skeleton className="h-[92px] rounded-xl" />}

      {!keeperLoading && keeperStatus && (
        <>
          {/* ── Live state: three facts, one line each ──
              Engine, judge reachability, gatekeeper. The endpoint and model
              used to be repeated here as rows; they belong to the judge card
              below, which is also where they can be changed. */}
          <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
            <div className="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
              <StatusFact
                label="Engine"
                ok={keeperStatus.enabled}
                text={keeperStatus.enabled ? "Running" : "Off"}
              />
              {/* Three states. "Not answering" for an endpoint the server never
                  dialled is the most misleading thing this strip can say to
                  somebody configuring Keeper before switching it on — which is
                  the normal order, since the engine ships off. */}
              <StatusFact
                label="Judge"
                ok={keeperStatus.ollama_online}
                text={
                  keeperStatus.ollama_online
                    ? `Answering · ${keeperStatus.model || "no model"}`
                    : !keeperStatus.ollama_url
                      ? "No endpoint set"
                      : keeperStatus.ollama_probed === false
                        ? "Not checked yet"
                        : `Not answering · ${keeperStatus.ollama_url}`
                }
              />
              <StatusFact
                label="Gatekeeper"
                ok={keeperStatus.gatekeeper_configured}
                text={keeperStatus.gatekeeper_configured ? "In the credential path" : "Not enforcing"}
              />
              <Button
                variant="outline"
                size="sm"
                className="h-7 px-2.5 text-xs ml-auto"
                onClick={onRefresh}
                disabled={keeperLoading}
              >
                <RefreshCw className={cn("mr-1.5 h-3 w-3", keeperLoading && "animate-spin")} />
                Refresh
              </Button>
            </div>
            {!keeperStatus.enabled && (
              <div className="px-4 py-2.5 bg-warn/[0.04] border-t border-warn/20">
                <p className="text-[11px] text-muted-foreground leading-relaxed">
                  Keeper is off, so SECRET credentials are injected into agents directly and no
                  credential request is judged. Turn it on under{" "}
                  <span className="text-foreground/80">Configuration → Credential access judge</span>{" "}
                  below — no restart, no env editing. A local Ollama judge costs nothing per
                  decision.
                </p>
              </div>
            )}
          </div>

          {/* ── Decision stats KPIs ── */}
          <div className="grid gap-3 grid-cols-2 lg:grid-cols-4">
            <KpiCard
              label="Total requests"
              value={keeperStatus.total_requests}
              subtitle="lifetime"
            />
            <KpiCard
              label="Allowed"
              value={keeperStatus.allow_count}
              valueColor="rgb(52, 211, 153)"
              subtitle="decisions"
            />
            <KpiCard
              label="Denied"
              value={keeperStatus.deny_count}
              valueColor={keeperStatus.deny_count > 0 ? "rgb(248, 113, 113)" : undefined}
              subtitle="decisions"
            />
            <KpiCard
              label="Escalated"
              value={keeperStatus.escalate_count}
              valueColor={keeperStatus.escalate_count > 0 ? "rgb(251, 191, 36)" : undefined}
              subtitle="to human"
            />
          </div>

          {/* ── Live activity stream ── */}
          <SettingsCard
            title="Live activity"
            description="Real-time keeper decisions as they happen"
            actions={
              <span className={cn(
                "inline-flex items-center gap-1.5 h-6 px-2 rounded-md text-[10px] font-semibold uppercase tracking-wide border",
                keeperWsStatus === "connected"
                  ? "text-success border-success/30 bg-success/10"
                  : keeperWsStatus === "connecting"
                    ? "text-warn border-warn/30 bg-warn/10"
                    : "text-muted-foreground border-border bg-muted/20",
              )}>
                <Radio className="h-2.5 w-2.5" />
                {keeperWsStatus === "connected" ? "Streaming" : keeperWsStatus === "connecting" ? "Connecting" : "Disconnected"}
              </span>
            }
          >
            {keeperLiveEvents.length === 0 ? (
              <div className="flex items-center justify-center py-10 text-center">
                <div className="text-[11px] text-muted-foreground max-w-sm">
                  Waiting for keeper events. Send a credential request from an agent to see it here in real time.
                </div>
              </div>
            ) : (
              <div className="max-h-[260px] overflow-y-auto">
                {keeperLiveEvents.map((evt, i) => (
                  <div
                    key={evt.request_id}
                    className={cn(
                      "flex items-start gap-2.5 px-4 py-2.5",
                      i < keeperLiveEvents.length - 1 && "border-b border-border/40",
                    )}
                  >
                    <StatusBadge
                      status={decisionStatusKey(evt.decision)}
                      label={evt.decision ?? "PENDING"}
                      className="shrink-0 mt-0.5 text-[10px]"
                    />
                    <div className="min-w-0 flex-1">
                      <div className="text-xs leading-tight">
                        <span className="font-medium">{evt.agent_name}</span>
                        <span className="text-muted-foreground"> requested </span>
                        <span className="font-mono text-[11px]">{evt.credential_name}</span>
                        {evt.request_type === "execute" && (
                          <span className="ml-1 text-[10px] text-muted-foreground">(exec)</span>
                        )}
                      </div>
                      <div className="text-[10px] text-muted-foreground truncate mt-0.5">
                        {evt.intent}
                      </div>
                      {evt.reason && (
                        <div className="text-[10px] text-muted-foreground truncate italic mt-0.5">
                          {evt.reason}
                        </div>
                      )}
                    </div>
                    <div className="text-[10px] font-mono text-muted-foreground shrink-0 tabular-nums">
                      {evt.risk_score}/10
                    </div>
                  </div>
                ))}
              </div>
            )}
          </SettingsCard>

          {/* ── Recent requests table ── */}
          <SettingsCard
            title="Recent requests"
            description={
              keeperLog.length === 0
                ? "No keeper requests yet"
                : `${keeperLog.length} most recent request${keeperLog.length === 1 ? "" : "s"}`
            }
          >
            {keeperLog.length === 0 ? (
              <div className="flex items-center justify-center py-10 text-[11px] text-muted-foreground">
                No keeper requests yet
              </div>
            ) : (
              <>
                {/* Desktop header */}
                <div className="hidden md:grid md:grid-cols-[minmax(0,1.2fr)_minmax(0,1.4fr)_70px_90px_60px_120px] items-center gap-3 px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground border-b border-border/60">
                  <div>Agent</div>
                  <div>Credential</div>
                  <div>Type</div>
                  <div>Decision</div>
                  <div className="text-right">Risk</div>
                  <div>Time</div>
                </div>
                {/* Rows */}
                {keeperLog.map((entry, idx) => (
                  <button
                    key={entry.id}
                    type="button"
                    onClick={() => onSelectKeeperEntry(entry)}
                    className={cn(
                      "flex flex-col gap-1 md:grid md:grid-cols-[minmax(0,1.2fr)_minmax(0,1.4fr)_70px_90px_60px_120px] md:items-center md:gap-3 w-full px-4 py-2.5 text-left hover:bg-white/[0.02] transition-colors",
                      idx < keeperLog.length - 1 && "border-b border-border/40",
                    )}
                  >
                    <div className="text-xs font-medium truncate">{entry.agent_name}</div>
                    <div className="text-[11px] text-muted-foreground font-mono truncate">
                      {entry.credential_name}
                    </div>
                    <div className="text-[11px] text-muted-foreground">
                      <span className="md:hidden text-muted-foreground">Type: </span>
                      {entry.request_type === "execute" ? "Execute" : "Access"}
                    </div>
                    <div className="flex items-center gap-2">
                      <span className="md:hidden text-[11px] text-muted-foreground">Decision:</span>
                      <StatusBadge
                        status={decisionStatusKey(entry.decision)}
                        label={entry.decision ?? "PENDING"}
                        className="text-[10px]"
                      />
                    </div>
                    <div className="text-[11px] text-muted-foreground font-mono md:text-right tabular-nums">
                      <span className="md:hidden text-muted-foreground">Risk: </span>
                      {entry.risk_score != null ? `${entry.risk_score}/10` : "—"}
                    </div>
                    <div className="text-[11px] text-muted-foreground font-mono truncate">
                      {new Date(entry.created_at).toLocaleString()}
                    </div>
                  </button>
                ))}
              </>
            )}
          </SettingsCard>
        </>
      )}

      {/* ── Configuration ──
          Below the monitoring, behind a divider, in dependency order: what
          decides → can every judge run → what else is watched → where findings
          go → how long access lasts → this workspace's own override. */}
      <div className="flex items-center gap-3 pt-2">
        <h3 className="text-body font-medium text-foreground/80 leading-none shrink-0">Configuration</h3>
        <div className="h-px flex-1 bg-border/60" />
      </div>

      <KeeperJudgeCard workspaceId={workspaceId} />

      {/* Which model each judge actually uses, and whether it can run.
          Deliberately OUTSIDE the `keeperStatus &&` guard above: a null status
          means the keeper status endpoint failed, which is exactly when an
          operator needs to know whether the judges can run — hiding it then
          removes the diagnosis along with the symptom. */}
      <JudgeModelsCard workspaceId={workspaceId} />

      {!keeperLoading && keeperStatus && (
        <>
          {/* ── Watchdog / findings / leases / workspace model (#1001 M0) ── */}
          <KeeperGovernancePanel
            workspaceId={workspaceId}
            serverEnabled={keeperStatus.enabled}
          />

          {/* ── Detail sheet ── */}
          <Sheet
            open={!!selectedKeeperEntry}
            onOpenChange={(open) => { if (!open) onSelectKeeperEntry(null) }}
          >
            <SheetContent side="right" className="sm:max-w-2xl w-full overflow-y-auto">
              <SheetHeader>
                <SheetTitle className="flex items-center gap-2 text-sm">
                  <Shield className="h-3.5 w-3.5" />
                  Keeper decision detail
                </SheetTitle>
              </SheetHeader>
              {selectedKeeperEntry && (
                <div className="space-y-4 px-1 mt-4">
                  {/* Summary grid */}
                  <div className="grid grid-cols-2 gap-3">
                    <DetailField label="Agent" value={selectedKeeperEntry.agent_name} />
                    <DetailField label="Credential" value={selectedKeeperEntry.credential_name} mono />
                    <div>
                      <FieldLabel>Decision</FieldLabel>
                      <StatusBadge
                        status={decisionStatusKey(selectedKeeperEntry.decision)}
                        label={selectedKeeperEntry.decision ?? "PENDING"}
                        className="mt-1 text-[10px]"
                      />
                    </div>
                    <DetailField
                      label="Risk score"
                      value={selectedKeeperEntry.risk_score != null ? `${selectedKeeperEntry.risk_score}/10` : "—"}
                    />
                    <DetailField
                      label="Type"
                      value={selectedKeeperEntry.request_type === "execute" ? "Execute" : "Access"}
                    />
                    <DetailField
                      label="Time"
                      value={new Date(selectedKeeperEntry.created_at).toLocaleString()}
                    />
                  </div>

                  <DetailBlock label="Intent">
                    <div className="text-[11px] bg-muted/40 border border-border/60 rounded-md p-2.5">
                      {redactSecrets(selectedKeeperEntry.intent)}
                    </div>
                  </DetailBlock>

                  {selectedKeeperEntry.reason && (
                    <DetailBlock label="Reason">
                      <div className="text-[11px] bg-muted/40 border border-border/60 rounded-md p-2.5">
                        {redactSecrets(selectedKeeperEntry.reason)}
                      </div>
                    </DetailBlock>
                  )}

                  {selectedKeeperEntry.command && (
                    <DetailBlock label="Command">
                      <pre className="text-[10px] bg-muted/60 border border-border/60 rounded-md p-2.5 overflow-x-auto font-mono">
                        {redactSecrets(selectedKeeperEntry.command)}
                      </pre>
                    </DetailBlock>
                  )}

                  <DetailBlock label="Ollama prompt">
                    {selectedKeeperEntry.ollama_prompt ? (
                      <pre className="text-[10px] bg-muted/60 border border-border/60 rounded-md p-2.5 overflow-x-auto whitespace-pre-wrap font-mono max-h-[240px] overflow-y-auto">
                        {redactSecrets(selectedKeeperEntry.ollama_prompt)}
                      </pre>
                    ) : (
                      <div className="text-[11px] text-muted-foreground italic bg-muted/40 border border-border/60 rounded-md p-2.5">
                        Not available (L1 auto-allow or pre-observability request)
                      </div>
                    )}
                  </DetailBlock>

                  <DetailBlock label="Ollama raw response">
                    {selectedKeeperEntry.ollama_raw_response ? (
                      <pre className="text-[10px] bg-muted/60 border border-border/60 rounded-md p-2.5 overflow-x-auto whitespace-pre-wrap font-mono max-h-[240px] overflow-y-auto">
                        {redactSecrets(selectedKeeperEntry.ollama_raw_response)}
                      </pre>
                    ) : (
                      <div className="text-[11px] text-muted-foreground italic bg-muted/40 border border-border/60 rounded-md p-2.5">
                        Not available (L1 auto-allow or pre-observability request)
                      </div>
                    )}
                  </DetailBlock>

                  <div className="pt-3 border-t border-border/60">
                    <div className="text-[10px] text-muted-foreground">
                      Request ID:{" "}
                      <span className="font-mono">{selectedKeeperEntry.id}</span>
                    </div>
                  </div>
                </div>
              )}
            </SheetContent>
          </Sheet>
        </>
      )}
    </div>
  )
})

// ── Helpers ─────────────────────────────────────────────────────────

/**
 * StatusFact is one fact in the top strip: a dot, a label, a short value.
 *
 * It replaces five SettingsRows in a "System status" card that sat below the
 * settings. Three facts an operator checks at a glance do not need five rows
 * and their own heading — and two of those rows (endpoint, model) were the
 * judge card's values repeated where they could not be edited.
 */
function StatusFact({ label, ok, text }: { label: string; ok: boolean; text: string }) {
  return (
    <span className="inline-flex items-center gap-2 min-w-0">
      <StatusDot status={ok ? "COMPLETED" : "FAILED"} />
      <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
        {label}
      </span>
      <span className="text-xs text-foreground/80 truncate">{text}</span>
    </span>
  )
}

function FieldLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
      {children}
    </div>
  )
}

function DetailField({ label, value, mono }: { label: string; value: React.ReactNode; mono?: boolean }) {
  return (
    <div>
      <FieldLabel>{label}</FieldLabel>
      <div className={cn("text-xs text-foreground/80 mt-1 truncate", mono && "font-mono")}>
        {value}
      </div>
    </div>
  )
}

function DetailBlock({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <FieldLabel>{label}</FieldLabel>
      <div className="mt-1">{children}</div>
    </div>
  )
}
