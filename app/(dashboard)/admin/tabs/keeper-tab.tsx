import React from "react"
import { RefreshCw, Radio, Shield } from "lucide-react"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import {
  Sheet, SheetContent, SheetHeader, SheetTitle,
} from "@/components/ui/sheet"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { SettingsCard, SettingsRow } from "@/components/features/settings/shared"
import { cn } from "@/lib/utils"
import { redactSecrets, redactUrl } from "../utils"
import type { KeeperStatus, KeeperLogEntry } from "../types"
import type { KeeperLiveEvent, KeeperWsStatus } from "../hooks/use-admin-websocket"

interface KeeperTabProps {
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
      {/* ── Intro ── */}
      <div>
        <h3 className="text-body font-medium text-foreground/80 leading-none">
          Keeper — credential access control
        </h3>
        <p className="text-[11px] text-muted-foreground mt-1 leading-snug max-w-2xl">
          Keeper evaluates credential access requests using a local AI model (Ollama).
          Agents never see raw credentials — Keeper decides ALLOW, DENY, or ESCALATE.
        </p>
      </div>

      {keeperLoading && <Skeleton className="h-[240px] rounded-xl" />}

      {!keeperLoading && keeperStatus && (
        <>
          {/* ── System status card ── */}
          <SettingsCard
            title="System status"
            description="Keeper subsystem health"
            actions={
              <Button
                variant="outline"
                size="sm"
                className="h-7 px-2.5 text-xs"
                onClick={onRefresh}
                disabled={keeperLoading}
              >
                <RefreshCw className={cn("mr-1.5 h-3 w-3", keeperLoading && "animate-spin")} />
                Refresh
              </Button>
            }
          >
            <SettingsRow label="Keeper">
              <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <StatusDot status={keeperStatus.enabled ? "COMPLETED" : "BLOCKED"} />
                {keeperStatus.enabled ? "Enabled" : "Disabled"}
              </span>
            </SettingsRow>
            <SettingsRow label="Gatekeeper">
              <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <StatusDot status={keeperStatus.gatekeeper_configured ? "COMPLETED" : "FAILED"} />
                {keeperStatus.gatekeeper_configured ? "Configured" : "Not configured"}
              </span>
            </SettingsRow>
            <SettingsRow label="Ollama LLM" border={keeperStatus.enabled}>
              <span className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <StatusDot status={keeperStatus.ollama_online ? "COMPLETED" : "FAILED"} />
                {keeperStatus.ollama_online
                  ? `Online · ${keeperStatus.model}`
                  : keeperStatus.enabled ? "Offline" : "Not configured"}
              </span>
            </SettingsRow>
            {keeperStatus.enabled && (
              <>
                <SettingsRow label="Ollama URL">
                  <span className="text-[11px] font-mono text-muted-foreground truncate">
                    {redactUrl(keeperStatus.ollama_url)}
                  </span>
                </SettingsRow>
                <SettingsRow label="Model" border={false}>
                  <span className="text-[11px] font-mono text-muted-foreground">
                    {keeperStatus.model}
                  </span>
                </SettingsRow>
              </>
            )}
            {!keeperStatus.enabled && (
              <div className="px-4 py-2.5 bg-amber-500/[0.04] border-t border-amber-500/20">
                <p className="text-[11px] text-muted-foreground leading-relaxed">
                  To enable Keeper, set{" "}
                  <code className="bg-muted/60 border border-border/60 px-1 py-0.5 rounded text-[10px] font-mono">
                    KEEPER_OLLAMA_URL=http://localhost:11434
                  </code>{" "}
                  in your{" "}
                  <code className="bg-muted/60 border border-border/60 px-1 py-0.5 rounded text-[10px] font-mono">
                    .env.local
                  </code>{" "}
                  and restart the server.
                </p>
              </div>
            )}
          </SettingsCard>

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
                  ? "text-emerald-400 border-emerald-500/30 bg-emerald-500/10"
                  : keeperWsStatus === "connecting"
                    ? "text-amber-400 border-amber-500/30 bg-amber-500/10"
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
