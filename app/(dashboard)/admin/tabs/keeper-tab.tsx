import React from "react"
import {
  RefreshCw, Radio, Shield,
} from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
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
      <div className="pb-3 border-b">
        <h3 className="text-sm font-medium">Keeper — Credential Access Control</h3>
        <p className="text-xs text-muted-foreground">
          Keeper evaluates credential access requests using a local AI model (Ollama).
          Agents never see raw credentials — Keeper decides ALLOW / DENY / ESCALATE.
        </p>
      </div>

      {keeperLoading && <Skeleton className="h-[200px] rounded-xl" />}

      {!keeperLoading && keeperStatus && (
        <>
          {/* Status card */}
          <Card>
            <CardContent className="p-5 space-y-4">
              <div className="text-xs font-medium">System Status</div>
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <span className={cn("w-2 h-2 rounded-full", keeperStatus.enabled ? "bg-emerald-500" : "bg-amber-400")} />
                    <span className="text-xs">Keeper</span>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {keeperStatus.enabled ? "Enabled" : "Disabled"}
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <span className={cn("w-2 h-2 rounded-full", keeperStatus.gatekeeper_configured ? "bg-emerald-500" : "bg-red-400")} />
                    <span className="text-xs">Gatekeeper</span>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {keeperStatus.gatekeeper_configured ? "Configured" : "Not configured"}
                  </span>
                </div>
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    <span className={cn("w-2 h-2 rounded-full", keeperStatus.ollama_online ? "bg-emerald-500" : "bg-red-400")} />
                    <span className="text-xs">Ollama LLM</span>
                  </div>
                  <span className="text-xs text-muted-foreground">
                    {keeperStatus.ollama_online
                      ? `Online — ${keeperStatus.model}`
                      : keeperStatus.enabled
                        ? "Offline"
                        : "Not configured"}
                  </span>
                </div>
              </div>

              {keeperStatus.enabled && (
                <div className="pt-3 border-t text-xs text-muted-foreground space-y-1">
                  <div>Ollama URL: <span className="font-mono">{redactUrl(keeperStatus.ollama_url)}</span></div>
                  <div>Model: <span className="font-mono">{keeperStatus.model}</span></div>
                </div>
              )}

              {!keeperStatus.enabled && (
                <div className="pt-3 border-t">
                  <p className="text-xs text-muted-foreground">
                    To enable Keeper, set <code className="bg-muted px-1 py-0.5 rounded text-[10px]">KEEPER_OLLAMA_URL=http://localhost:11434</code> in
                    your <code className="bg-muted px-1 py-0.5 rounded text-[10px]">.env.local</code> and restart the server.
                  </p>
                </div>
              )}

              <Button variant="outline" size="sm" onClick={onRefresh} disabled={keeperLoading}>
                <RefreshCw className={cn("mr-2 h-3.5 w-3.5", keeperLoading && "animate-spin")} />
                Refresh
              </Button>
            </CardContent>
          </Card>

          {/* Stats */}
          <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
            {[
              { label: "Total Requests", value: keeperStatus.total_requests },
              { label: "Allowed", value: keeperStatus.allow_count, color: "text-emerald-600" },
              { label: "Denied", value: keeperStatus.deny_count, color: "text-red-600" },
              { label: "Escalated", value: keeperStatus.escalate_count, color: "text-amber-600" },
            ].map((s) => (
              <Card key={s.label}>
                <CardContent className="p-4">
                  <div className="text-micro text-muted-foreground uppercase font-medium">{s.label}</div>
                  <div className={cn("text-2xl font-bold mt-1", s.color)}>{s.value}</div>
                </CardContent>
              </Card>
            ))}
          </div>

          {/* Live keeper events */}
          <div className="space-y-3">
            <div className="flex items-center gap-2">
              <Radio className={cn("h-3.5 w-3.5", keeperWsStatus === "connected" ? "text-emerald-500" : "text-muted-foreground")} />
              <h4 className="text-xs font-medium">Live Activity</h4>
              <span className={cn("text-micro",
                keeperWsStatus === "connected" ? "text-emerald-600" : "text-muted-foreground"
              )}>
                {keeperWsStatus === "connected" ? "Streaming" : keeperWsStatus === "connecting" ? "Connecting..." : "Disconnected"}
              </span>
            </div>
            <Card>
              <CardContent className="p-3 max-h-[240px] overflow-y-auto">
                {keeperLiveEvents.length === 0 ? (
                  <div className="text-center text-xs text-muted-foreground py-6">
                    Waiting for keeper events... Send a credential request from an agent to see it here in real time.
                  </div>
                ) : (
                  <div className="space-y-2">
                    {keeperLiveEvents.map((evt) => (
                      <div key={evt.request_id} className="flex items-start gap-2 py-1.5 border-b last:border-0">
                        <Badge
                          variant="outline"
                          className={cn("text-micro shrink-0 mt-0.5",
                            evt.decision === "ALLOW" && "bg-emerald-50 text-emerald-700 border-emerald-200",
                            evt.decision === "DENY" && "bg-red-50 text-red-700 border-red-200",
                            evt.decision === "ESCALATE" && "bg-amber-50 text-amber-700 border-amber-200",
                          )}
                        >
                          {evt.decision}
                        </Badge>
                        <div className="min-w-0 flex-1">
                          <div className="text-xs">
                            <span className="font-medium">{evt.agent_name}</span>
                            <span className="text-muted-foreground"> requested </span>
                            <span className="font-mono text-[10px]">{evt.credential_name}</span>
                            {evt.request_type === "execute" && (
                              <Badge variant="outline" className="ml-1 text-micro py-0">exec</Badge>
                            )}
                          </div>
                          <div className="text-micro text-muted-foreground truncate">{evt.intent}</div>
                          {evt.reason && (
                            <div className="text-micro text-muted-foreground/70 truncate italic">{evt.reason}</div>
                          )}
                        </div>
                        <div className="text-micro text-muted-foreground shrink-0">
                          {evt.risk_score}/10
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          </div>

          {/* Request log */}
          <div className="space-y-3">
            <h4 className="text-xs font-medium">Recent Requests</h4>
            <Card>
              <CardContent className="p-0">
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
                        <TableCell colSpan={6} className="text-center text-xs text-muted-foreground py-8">
                          No keeper requests yet
                        </TableCell>
                      </TableRow>
                    )}
                    {keeperLog.map((entry) => (
                      <TableRow key={entry.id} className="cursor-pointer hover:bg-muted/50" onClick={() => onSelectKeeperEntry(entry)}>
                        <TableCell className="text-xs font-medium">{entry.agent_name}</TableCell>
                        <TableCell className="text-xs text-muted-foreground">{entry.credential_name}</TableCell>
                        <TableCell>
                          <Badge variant="outline" className="text-micro">
                            {entry.request_type === "execute" ? "Execute" : "Access"}
                          </Badge>
                        </TableCell>
                        <TableCell>
                          <Badge
                            variant="outline"
                            className={cn("text-micro",
                              entry.decision === "ALLOW" && "bg-emerald-50 text-emerald-700 border-emerald-200",
                              entry.decision === "DENY" && "bg-red-50 text-red-700 border-red-200",
                              entry.decision === "ESCALATE" && "bg-amber-50 text-amber-700 border-amber-200",
                              entry.decision === "PENDING" && "bg-blue-50 text-blue-700 border-blue-200",
                            )}
                          >
                            {entry.decision ?? "PENDING"}
                          </Badge>
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {entry.risk_score != null ? `${entry.risk_score}/10` : "—"}
                        </TableCell>
                        <TableCell className="text-xs text-muted-foreground">
                          {new Date(entry.created_at).toLocaleString()}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </CardContent>
            </Card>
          </div>

          {/* Keeper request detail sheet */}
          <Sheet open={!!selectedKeeperEntry} onOpenChange={(open) => { if (!open) onSelectKeeperEntry(null) }}>
            <SheetContent side="right" className="sm:max-w-2xl w-full overflow-y-auto">
              <SheetHeader>
                <SheetTitle className="flex items-center gap-2 text-sm">
                  <Shield className="h-4 w-4" />
                  Keeper Decision Detail
                </SheetTitle>
              </SheetHeader>
              {selectedKeeperEntry && (
                <div className="space-y-5 px-1">
                  {/* Summary */}
                  <div className="grid grid-cols-2 gap-3">
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">Agent</div>
                      <div className="text-xs font-medium mt-0.5">{selectedKeeperEntry.agent_name}</div>
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">Credential</div>
                      <div className="text-xs font-mono mt-0.5">{selectedKeeperEntry.credential_name}</div>
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">Decision</div>
                      <Badge
                        variant="outline"
                        className={cn("text-micro mt-0.5",
                          selectedKeeperEntry.decision === "ALLOW" && "bg-emerald-50 text-emerald-700 border-emerald-200",
                          selectedKeeperEntry.decision === "DENY" && "bg-red-50 text-red-700 border-red-200",
                          selectedKeeperEntry.decision === "ESCALATE" && "bg-amber-50 text-amber-700 border-amber-200",
                        )}
                      >
                        {selectedKeeperEntry.decision ?? "PENDING"}
                      </Badge>
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">Risk Score</div>
                      <div className="text-xs font-medium mt-0.5">{selectedKeeperEntry.risk_score != null ? `${selectedKeeperEntry.risk_score}/10` : "—"}</div>
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">Type</div>
                      <div className="text-xs mt-0.5">{selectedKeeperEntry.request_type === "execute" ? "Execute" : "Access"}</div>
                    </div>
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium">Time</div>
                      <div className="text-xs text-muted-foreground mt-0.5">{new Date(selectedKeeperEntry.created_at).toLocaleString()}</div>
                    </div>
                  </div>

                  {/* Intent */}
                  <div>
                    <div className="text-micro text-muted-foreground uppercase font-medium mb-1">Intent</div>
                    <div className="text-xs bg-muted/50 rounded-md p-3">{redactSecrets(selectedKeeperEntry.intent)}</div>
                  </div>

                  {/* Reason */}
                  {selectedKeeperEntry.reason && (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">Reason</div>
                      <div className="text-xs bg-muted/50 rounded-md p-3">{redactSecrets(selectedKeeperEntry.reason)}</div>
                    </div>
                  )}

                  {/* Command (execute requests) */}
                  {selectedKeeperEntry.command && (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">Command</div>
                      <pre className="text-[11px] bg-zinc-900 text-zinc-100 rounded-md p-3 overflow-x-auto font-mono">{redactSecrets(selectedKeeperEntry.command)}</pre>
                    </div>
                  )}

                  {/* Ollama Prompt */}
                  {selectedKeeperEntry.ollama_prompt ? (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">Ollama Prompt (sent to LLM)</div>
                      <pre className="text-[11px] bg-zinc-900 text-zinc-100 rounded-md p-3 overflow-x-auto whitespace-pre-wrap font-mono max-h-[300px] overflow-y-auto">{redactSecrets(selectedKeeperEntry.ollama_prompt)}</pre>
                    </div>
                  ) : (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">Ollama Prompt</div>
                      <div className="text-xs text-muted-foreground italic bg-muted/50 rounded-md p-3">Not available (L1 auto-allow or pre-observability request)</div>
                    </div>
                  )}

                  {/* Ollama Raw Response */}
                  {selectedKeeperEntry.ollama_raw_response ? (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">Ollama Raw Response</div>
                      <pre className="text-[11px] bg-zinc-900 text-zinc-100 rounded-md p-3 overflow-x-auto whitespace-pre-wrap font-mono max-h-[300px] overflow-y-auto">{redactSecrets(selectedKeeperEntry.ollama_raw_response)}</pre>
                    </div>
                  ) : (
                    <div>
                      <div className="text-micro text-muted-foreground uppercase font-medium mb-1">Ollama Raw Response</div>
                      <div className="text-xs text-muted-foreground italic bg-muted/50 rounded-md p-3">Not available (L1 auto-allow or pre-observability request)</div>
                    </div>
                  )}

                  {/* Request ID */}
                  <div className="pt-3 border-t">
                    <div className="text-micro text-muted-foreground">Request ID: <span className="font-mono">{selectedKeeperEntry.id}</span></div>
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
