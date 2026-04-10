import React from "react"
import {
  RefreshCw, Radio, Shield, CheckCircle2, XCircle, AlertTriangle,
} from "lucide-react"
import { Card } from "@/components/ui/card"
import { SectionCard } from "@/components/ui/section-card"
import { StatCard } from "@/components/layout/stat-card"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
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
    case "ALLOW":
      return "COMPLETED"
    case "DENY":
      return "FAILED"
    case "ESCALATE":
      return "BLOCKED"
    case "PENDING":
      return "IN_PROGRESS"
    default:
      return "PENDING"
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
    <div className="space-y-6">
      <div className="pb-3 border-b border-border">
        <h3 className="text-body font-medium">Keeper — Credential Access Control</h3>
        <p className="text-label text-muted-foreground">
          Keeper evaluates credential access requests using a local AI model (Ollama).
          Agents never see raw credentials — Keeper decides ALLOW / DENY / ESCALATE.
        </p>
      </div>

      {keeperLoading && <Skeleton className="h-[200px] rounded-xl" />}

      {!keeperLoading && keeperStatus && (
        <>
          {/* Status card */}
          <SectionCard title="System Status" description="Keeper subsystem health">
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <StatusDot status={keeperStatus.enabled ? "COMPLETED" : "BLOCKED"} />
                  <span className="text-body">Keeper</span>
                </div>
                <span className="text-label text-muted-foreground">
                  {keeperStatus.enabled ? "Enabled" : "Disabled"}
                </span>
              </div>
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <StatusDot
                    status={keeperStatus.gatekeeper_configured ? "COMPLETED" : "FAILED"}
                  />
                  <span className="text-body">Gatekeeper</span>
                </div>
                <span className="text-label text-muted-foreground">
                  {keeperStatus.gatekeeper_configured ? "Configured" : "Not configured"}
                </span>
              </div>
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <StatusDot status={keeperStatus.ollama_online ? "COMPLETED" : "FAILED"} />
                  <span className="text-body">Ollama LLM</span>
                </div>
                <span className="text-label text-muted-foreground">
                  {keeperStatus.ollama_online
                    ? `Online — ${keeperStatus.model}`
                    : keeperStatus.enabled
                      ? "Offline"
                      : "Not configured"}
                </span>
              </div>
            </div>

            {keeperStatus.enabled && (
              <div className="mt-4 pt-3 border-t border-border text-label text-muted-foreground space-y-1">
                <div>
                  Ollama URL:{" "}
                  <span className="font-mono">{redactUrl(keeperStatus.ollama_url)}</span>
                </div>
                <div>
                  Model: <span className="font-mono">{keeperStatus.model}</span>
                </div>
              </div>
            )}

            {!keeperStatus.enabled && (
              <div className="mt-4 pt-3 border-t border-border">
                <p className="text-label text-muted-foreground">
                  To enable Keeper, set{" "}
                  <code className="bg-muted px-1 py-0.5 rounded text-micro">
                    KEEPER_OLLAMA_URL=http://localhost:11434
                  </code>{" "}
                  in your{" "}
                  <code className="bg-muted px-1 py-0.5 rounded text-micro">.env.local</code> and
                  restart the server.
                </p>
              </div>
            )}

            <Button
              variant="outline"
              size="sm"
              onClick={onRefresh}
              disabled={keeperLoading}
              className="mt-4"
            >
              <RefreshCw className={cn("mr-2 h-3.5 w-3.5", keeperLoading && "animate-spin")} />
              Refresh
            </Button>
          </SectionCard>

          {/* Stats */}
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
            <StatCard
              title="Total Requests"
              value={keeperStatus.total_requests}
              subtitle="lifetime"
              icon={Shield}
            />
            <StatCard
              title="Allowed"
              value={keeperStatus.allow_count}
              subtitle="decisions"
              icon={CheckCircle2}
            />
            <StatCard
              title="Denied"
              value={keeperStatus.deny_count}
              subtitle="decisions"
              icon={XCircle}
            />
            <StatCard
              title="Escalated"
              value={keeperStatus.escalate_count}
              subtitle="to human"
              icon={AlertTriangle}
            />
          </div>

          {/* Live keeper events */}
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <Radio
                className={cn(
                  "h-3.5 w-3.5",
                  keeperWsStatus === "connected" ? "text-foreground" : "text-muted-foreground"
                )}
              />
              <h4 className="text-label font-medium">Live Activity</h4>
              <StatusBadge
                status={
                  keeperWsStatus === "connected"
                    ? "COMPLETED"
                    : keeperWsStatus === "connecting"
                      ? "IN_PROGRESS"
                      : "PENDING"
                }
                label={
                  keeperWsStatus === "connected"
                    ? "Streaming"
                    : keeperWsStatus === "connecting"
                      ? "Connecting..."
                      : "Disconnected"
                }
              />
            </div>
            <Card className="p-3 max-h-[240px] overflow-y-auto">
              {keeperLiveEvents.length === 0 ? (
                <div className="text-center text-label text-muted-foreground py-6">
                  Waiting for keeper events... Send a credential request from an agent to see it
                  here in real time.
                </div>
              ) : (
                <div className="space-y-2">
                  {keeperLiveEvents.map((evt) => (
                    <div
                      key={evt.request_id}
                      className="flex items-start gap-2 py-1.5 border-b border-border last:border-0"
                    >
                      <StatusBadge
                        status={decisionStatusKey(evt.decision)}
                        label={evt.decision ?? "PENDING"}
                        className="shrink-0 mt-0.5"
                      />
                      <div className="min-w-0 flex-1">
                        <div className="text-label">
                          <span className="font-medium">{evt.agent_name}</span>
                          <span className="text-muted-foreground"> requested </span>
                          <span className="font-mono text-micro">{evt.credential_name}</span>
                          {evt.request_type === "execute" && (
                            <span className="ml-1 text-micro text-muted-foreground">(exec)</span>
                          )}
                        </div>
                        <div className="text-micro text-muted-foreground truncate">
                          {evt.intent}
                        </div>
                        {evt.reason && (
                          <div className="text-micro text-muted-foreground/70 truncate italic">
                            {evt.reason}
                          </div>
                        )}
                      </div>
                      <div className="text-micro text-muted-foreground shrink-0">
                        {evt.risk_score}/10
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </Card>
          </div>

          {/* Request log */}
          <div className="space-y-3">
            <h4 className="text-label font-medium">Recent Requests</h4>
            <Card className="overflow-hidden p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Agent</TableHead>
                    <TableHead>Credential</TableHead>
                    <TableHead>Type</TableHead>
                    <TableHead>Decision</TableHead>
                    <TableHead>Risk</TableHead>
                    <TableHead>Time</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {keeperLog.length === 0 && (
                    <TableRow>
                      <TableCell
                        colSpan={6}
                        className="text-center text-label text-muted-foreground py-8"
                      >
                        No keeper requests yet
                      </TableCell>
                    </TableRow>
                  )}
                  {keeperLog.map((entry) => (
                    <TableRow
                      key={entry.id}
                      className="cursor-pointer hover:bg-muted/50"
                      onClick={() => onSelectKeeperEntry(entry)}
                    >
                      <TableCell className="text-body font-medium">{entry.agent_name}</TableCell>
                      <TableCell className="text-body text-muted-foreground">
                        {entry.credential_name}
                      </TableCell>
                      <TableCell>
                        <span className="text-label text-muted-foreground">
                          {entry.request_type === "execute" ? "Execute" : "Access"}
                        </span>
                      </TableCell>
                      <TableCell>
                        <StatusBadge
                          status={decisionStatusKey(entry.decision)}
                          label={entry.decision ?? "PENDING"}
                        />
                      </TableCell>
                      <TableCell className="text-body text-muted-foreground">
                        {entry.risk_score != null ? `${entry.risk_score}/10` : "—"}
                      </TableCell>
                      <TableCell className="text-body text-muted-foreground">
                        {new Date(entry.created_at).toLocaleString()}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </Card>
          </div>

          {/* Keeper request detail sheet */}
          <Sheet
            open={!!selectedKeeperEntry}
            onOpenChange={(open) => {
              if (!open) onSelectKeeperEntry(null)
            }}
          >
            <SheetContent side="right" className="sm:max-w-2xl w-full overflow-y-auto">
              <SheetHeader>
                <SheetTitle className="flex items-center gap-2 text-body">
                  <Shield className="h-4 w-4" />
                  Keeper Decision Detail
                </SheetTitle>
              </SheetHeader>
              {selectedKeeperEntry && (
                <div className="space-y-5 px-1">
                  {/* Summary */}
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">
                        Agent
                      </div>
                      <div className="text-body font-medium mt-0.5">
                        {selectedKeeperEntry.agent_name}
                      </div>
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">
                        Credential
                      </div>
                      <div className="text-body font-mono mt-0.5">
                        {selectedKeeperEntry.credential_name}
                      </div>
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">
                        Decision
                      </div>
                      <StatusBadge
                        status={decisionStatusKey(selectedKeeperEntry.decision)}
                        label={selectedKeeperEntry.decision ?? "PENDING"}
                        className="mt-0.5"
                      />
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">
                        Risk Score
                      </div>
                      <div className="text-body font-medium mt-0.5">
                        {selectedKeeperEntry.risk_score != null
                          ? `${selectedKeeperEntry.risk_score}/10`
                          : "—"}
                      </div>
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">
                        Type
                      </div>
                      <div className="text-body mt-0.5">
                        {selectedKeeperEntry.request_type === "execute" ? "Execute" : "Access"}
                      </div>
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">
                        Time
                      </div>
                      <div className="text-body text-muted-foreground mt-0.5">
                        {new Date(selectedKeeperEntry.created_at).toLocaleString()}
                      </div>
                    </div>
                  </div>

                  {/* Intent */}
                  <div>
                    <div className="text-micro text-muted-foreground uppercase font-medium mb-1">
                      Intent
                    </div>
                    <div className="text-body bg-muted/50 rounded-md p-3">
                      {redactSecrets(selectedKeeperEntry.intent)}
                    </div>
                  </div>

                  {/* Reason */}
                  {selectedKeeperEntry.reason && (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">
                        Reason
                      </div>
                      <div className="text-body bg-muted/50 rounded-md p-3">
                        {redactSecrets(selectedKeeperEntry.reason)}
                      </div>
                    </div>
                  )}

                  {/* Command (execute requests) */}
                  {selectedKeeperEntry.command && (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">
                        Command
                      </div>
                      <pre className="text-micro bg-muted/80 rounded-md p-3 overflow-x-auto font-mono">
                        {redactSecrets(selectedKeeperEntry.command)}
                      </pre>
                    </div>
                  )}

                  {/* Ollama Prompt */}
                  {selectedKeeperEntry.ollama_prompt ? (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">
                        Ollama Prompt (sent to LLM)
                      </div>
                      <pre className="text-micro bg-muted/80 rounded-md p-3 overflow-x-auto whitespace-pre-wrap font-mono max-h-[300px] overflow-y-auto">
                        {redactSecrets(selectedKeeperEntry.ollama_prompt)}
                      </pre>
                    </div>
                  ) : (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">
                        Ollama Prompt
                      </div>
                      <div className="text-body text-muted-foreground italic bg-muted/50 rounded-md p-3">
                        Not available (L1 auto-allow or pre-observability request)
                      </div>
                    </div>
                  )}

                  {/* Ollama Raw Response */}
                  {selectedKeeperEntry.ollama_raw_response ? (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">
                        Ollama Raw Response
                      </div>
                      <pre className="text-micro bg-muted/80 rounded-md p-3 overflow-x-auto whitespace-pre-wrap font-mono max-h-[300px] overflow-y-auto">
                        {redactSecrets(selectedKeeperEntry.ollama_raw_response)}
                      </pre>
                    </div>
                  ) : (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">
                        Ollama Raw Response
                      </div>
                      <div className="text-body text-muted-foreground italic bg-muted/50 rounded-md p-3">
                        Not available (L1 auto-allow or pre-observability request)
                      </div>
                    </div>
                  )}

                  {/* Request ID */}
                  <div className="pt-3 border-t border-border">
                    <div className="text-micro text-muted-foreground">
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
