"use client"

import * as React from "react"
import { motion } from "motion/react"
import {
  Activity, Settings as SettingsIcon, Users,
  Loader2, FlaskConical, RefreshCw, AlertTriangle, Trash2,
  CheckCircle2, XCircle, Clock, Pencil, Eye, EyeOff,
} from "lucide-react"
import { Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription } from "@/components/ui/sheet"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { Input } from "@/components/ui/input"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { formatDate, formatRelativeTime } from "@/lib/time"
import { cn } from "@/lib/utils"

interface CredentialSummary {
  id: string
  name: string
  description: string | null
  type: string
  provider: string
  status: string
  scope: string
  account_label: string | null
  account_email: string | null
  token_expires_at: string | null
  last_checked_at: string | null
  last_used_at: string | null
  last_used_ips: string[]
  last_error: string | null
  tags: string[]
  created_at: string
  updated_at: string
  agent_names: string[]
  _count_agent_credentials: number
  mcp_used: boolean
}

interface AuditEvent {
  id: string
  event_type: string
  agent_id: string | null
  ip_address: string | null
  metadata: Record<string, unknown> | null
  occurred_at: string
}

interface RotationRow {
  id: string
  credential_id: string
  grace_seconds: number
  rotated_at: string
  expires_at: string
  rotated_by: string
  status: "ACTIVE" | "EXPIRED" | "CANCELLED"
  old_value_gone: boolean
}

export interface CredentialDetailSheetProps {
  workspaceId: string
  credential: CredentialSummary | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onRefresh: () => void
  onRotate: (cred: CredentialSummary) => void
  /** Optional handler — opens the full Edit dialog. When omitted the
   *  Edit button is hidden (legacy callers). */
  onEdit?: (cred: CredentialSummary) => void
}

export function CredentialDetailSheet({
  workspaceId, credential, open, onOpenChange, onRefresh, onRotate, onEdit,
}: CredentialDetailSheetProps) {
  const [tab, setTab] = React.useState<"overview" | "used-by" | "audit" | "settings">("overview")
  const [audit, setAudit] = React.useState<AuditEvent[]>([])
  const [auditLoading, setAuditLoading] = React.useState(false)
  const [rotations, setRotations] = React.useState<RotationRow[]>([])
  const [confirmDelete, setConfirmDelete] = React.useState(false)
  const [testing, setTesting] = React.useState(false)
  const [testResult, setTestResult] = React.useState<{ valid: boolean; error?: string } | null>(null)
  // Inline value rewrite — Vercel-parity manual rotation. Lives in the
  // Settings tab next to the full grace-overlap rotation flow.
  const [valueDraft, setValueDraft] = React.useState("")
  const [showValueDraft, setShowValueDraft] = React.useState(false)
  const [savingValue, setSavingValue] = React.useState(false)
  const [valueSaved, setValueSaved] = React.useState(false)

  React.useEffect(() => {
    if (!open || !credential) {
      setTab("overview")
      setAudit([])
      setRotations([])
      setTestResult(null)
      setValueDraft("")
      setShowValueDraft(false)
      setValueSaved(false)
    }
  }, [open, credential])

  React.useEffect(() => {
    if (!open || !credential) return
    if (tab === "audit") {
      setAuditLoading(true)
      fetch(`/api/v1/credentials/${credential.id}/audit?workspace_id=${workspaceId}&limit=50`)
        .then((r) => r.ok ? r.json() : [])
        .then((data: AuditEvent[]) => setAudit(Array.isArray(data) ? data : []))
        .catch(() => setAudit([]))
        .finally(() => setAuditLoading(false))
    }
    if (tab === "settings") {
      fetch(`/api/v1/credentials/${credential.id}/rotations?workspace_id=${workspaceId}`)
        .then((r) => r.ok ? r.json() : [])
        .then((data: RotationRow[]) => setRotations(Array.isArray(data) ? data : []))
        .catch(() => setRotations([]))
    }
  }, [tab, open, credential, workspaceId])

  if (!credential) return null

  const handleTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await fetch(`/api/v1/credentials/${credential.id}/test?workspace_id=${workspaceId}`, {
        method: "POST",
      })
      if (!res.ok) {
        setTestResult({ valid: false, error: "Test request failed" })
        return
      }
      const data = await res.json()
      setTestResult({ valid: data.valid, error: data.error })
    } catch {
      setTestResult({ valid: false, error: "Network error" })
    } finally {
      setTesting(false)
    }
  }

  const handleDelete = async () => {
    const res = await fetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
      method: "DELETE",
    })
    if (res.ok) {
      onRefresh()
      onOpenChange(false)
    }
    setConfirmDelete(false)
  }

  return (
    <>
      <Sheet open={open} onOpenChange={onOpenChange}>
        <SheetContent side="right" className="sm:max-w-[480px] p-0 flex flex-col">
          <SheetHeader className="px-5 pt-4 pb-3 border-b border-white/10">
            <div className="flex items-start justify-between gap-2">
              <div className="min-w-0">
                <SheetTitle className="text-base font-mono truncate">{credential.name}</SheetTitle>
                <SheetDescription className="text-xs truncate">
                  {credential.account_label || credential.description || credential.provider}
                </SheetDescription>
              </div>
              {onEdit && (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => onEdit(credential)}
                  className="shrink-0"
                >
                  <Pencil className="h-3 w-3 mr-1.5" />
                  Edit
                </Button>
              )}
            </div>
            {credential.tags && credential.tags.length > 0 && (
              <div className="flex flex-wrap gap-1 pt-1">
                {credential.tags.map((t) => (
                  <Badge key={t} variant="outline" className="text-[10px] px-1 font-mono">{t}</Badge>
                ))}
              </div>
            )}
          </SheetHeader>

          <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)} className="flex-1 flex flex-col">
            <TabsList className="px-3 mt-2 justify-start bg-transparent border-b border-white/10 rounded-none h-9">
              <TabsTrigger value="overview" className="text-xs">Overview</TabsTrigger>
              <TabsTrigger value="used-by" className="text-xs">
                Used by{credential._count_agent_credentials > 0 && (
                  <Badge variant="secondary" className="ml-1.5 h-4 text-[10px] px-1.5">{credential._count_agent_credentials}</Badge>
                )}
              </TabsTrigger>
              <TabsTrigger value="audit" className="text-xs"><Activity className="h-3 w-3 mr-1" />Audit</TabsTrigger>
              <TabsTrigger value="settings" className="text-xs"><SettingsIcon className="h-3 w-3 mr-1" />Settings</TabsTrigger>
            </TabsList>

            <div className="flex-1 overflow-y-auto p-4">
              <TabsContent value="overview" className="m-0 space-y-3">
                <Field label="Type">{credential.type.replace(/_/g, " ")}</Field>
                <Field label="Provider">{credential.provider}</Field>
                <Field label="Scope">{credential.scope}</Field>
                <Field label="Created">{formatDate(credential.created_at)}</Field>
                {credential.token_expires_at && (
                  <Field label="Expires">{formatDate(credential.token_expires_at)}</Field>
                )}
                <Field label="Last used">
                  {credential.last_used_at ? (
                    <span className="inline-flex items-center gap-1.5">
                      <Clock className="h-3 w-3 opacity-60" />
                      {formatRelativeTime(credential.last_used_at)}
                    </span>
                  ) : (
                    <span className="text-muted-foreground/60">never</span>
                  )}
                </Field>
                {credential.last_error && (
                  <div className="rounded-md border border-red-500/30 bg-red-500/[0.05] p-3">
                    <div className="flex items-center gap-1.5 text-xs text-red-400 font-medium">
                      <AlertTriangle className="h-3.5 w-3.5" />
                      Last error
                    </div>
                    <p className="text-xs text-foreground/80 mt-1 font-mono">{credential.last_error}</p>
                  </div>
                )}

                {credential.last_used_ips.length > 0 && (
                  <div className="space-y-1.5">
                    <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                      Last 5 IPs
                    </div>
                    <ul className="space-y-1">
                      {credential.last_used_ips.map((ip) => (
                        <li key={ip} className="text-xs font-mono text-foreground/80 flex items-center gap-2">
                          <span className="h-1 w-1 rounded-full bg-emerald-500/60" />
                          {ip}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                <div className="pt-3 border-t border-white/10 flex gap-2">
                  <Button size="sm" variant="outline" onClick={handleTest} disabled={testing}>
                    {testing ? <Loader2 className="h-3.5 w-3.5 mr-1.5 animate-spin" /> : <FlaskConical className="h-3.5 w-3.5 mr-1.5" />}
                    Test now
                  </Button>
                  {testResult && (
                    <span className={cn("text-xs inline-flex items-center gap-1.5", testResult.valid ? "text-emerald-400" : "text-red-400")}>
                      {testResult.valid ? <CheckCircle2 className="h-3.5 w-3.5" /> : <XCircle className="h-3.5 w-3.5" />}
                      {testResult.valid ? "Valid" : (testResult.error || "Invalid")}
                    </span>
                  )}
                </div>
              </TabsContent>

              <TabsContent value="used-by" className="m-0">
                {credential.agent_names.length > 0 ? (
                  <ul className="space-y-1.5">
                    {credential.agent_names.map((name) => (
                      <li key={name} className="rounded-md border border-white/10 bg-zinc-950 px-3 py-2 text-sm flex items-center gap-2">
                        <Users className="h-3.5 w-3.5 text-muted-foreground" />
                        {name}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="text-xs text-muted-foreground py-6 text-center">
                    Not yet used by any agent.
                  </p>
                )}
                {credential.mcp_used && (
                  <div className="mt-3 rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2 text-xs">
                    Also referenced by one or more MCP server integrations.
                  </div>
                )}
              </TabsContent>

              <TabsContent value="audit" className="m-0">
                {auditLoading ? (
                  <div className="text-center py-8"><Loader2 className="inline h-4 w-4 animate-spin text-muted-foreground" /></div>
                ) : audit.length === 0 ? (
                  <p className="text-xs text-muted-foreground py-6 text-center">No audit events yet.</p>
                ) : (
                  <ul className="space-y-2">
                    {audit.map((e, idx) => (
                      <motion.li
                        key={e.id}
                        initial={{ opacity: 0, y: 4 }}
                        animate={{ opacity: 1, y: 0 }}
                        transition={{ duration: 0.12, delay: idx * 0.015 }}
                        className="rounded-md border border-white/10 bg-zinc-950 px-3 py-2 text-xs"
                      >
                        <div className="flex items-center justify-between gap-2">
                          <Badge variant="outline" className="text-[10px] px-1.5 font-mono">{e.event_type}</Badge>
                          <span className="text-muted-foreground">{formatRelativeTime(e.occurred_at)}</span>
                        </div>
                        {e.ip_address && (
                          <div className="text-[10px] text-muted-foreground font-mono mt-1">
                            from {e.ip_address}
                          </div>
                        )}
                      </motion.li>
                    ))}
                  </ul>
                )}
              </TabsContent>

              <TabsContent value="settings" className="m-0 space-y-4">
                {/* Inline value rewrite — quick manual rotation without
                    grace overlap. For users who just need to paste a
                    new key and move on (Vercel pattern). The real
                    rotation flow with overlap lives in onRotate. */}
                <div className="space-y-1.5">
                  <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                    Update value
                  </div>
                  <div className="relative">
                    <Input
                      type={showValueDraft ? "text" : "password"}
                      placeholder="Paste new secret value"
                      value={valueDraft}
                      onChange={(e) => { setValueDraft(e.target.value); setValueSaved(false) }}
                      className="pr-10 font-mono text-xs"
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-xs"
                      className="absolute right-1.5 top-1/2 -translate-y-1/2"
                      onClick={() => setShowValueDraft((s) => !s)}
                      aria-label={showValueDraft ? "Hide value" : "Show value"}
                    >
                      {showValueDraft ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                    </Button>
                  </div>
                  <div className="flex items-center gap-2">
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={!valueDraft.trim() || savingValue}
                      onClick={async () => {
                        setSavingValue(true)
                        setValueSaved(false)
                        try {
                          const res = await fetch(`/api/v1/credentials/${credential.id}?workspace_id=${workspaceId}`, {
                            method: "PATCH",
                            headers: { "Content-Type": "application/json" },
                            body: JSON.stringify({ value: valueDraft }),
                          })
                          if (res.ok) {
                            setValueDraft("")
                            setShowValueDraft(false)
                            setValueSaved(true)
                            onRefresh()
                          }
                        } finally {
                          setSavingValue(false)
                        }
                      }}
                    >
                      {savingValue && <Loader2 className="h-3 w-3 mr-1.5 animate-spin" />}
                      Save value
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => onRotate(credential)}
                      className="text-[11px] text-muted-foreground hover:text-foreground"
                    >
                      <RefreshCw className="h-3 w-3 mr-1.5" />
                      Rotate with grace overlap…
                    </Button>
                    {valueSaved && (
                      <span className="text-[11px] text-emerald-400 inline-flex items-center gap-1">
                        <CheckCircle2 className="h-3 w-3" />
                        Saved
                      </span>
                    )}
                  </div>
                  <p className="text-[10px] text-muted-foreground">
                    Save replaces the value immediately. Use rotate-with-grace if agents are
                    currently running and need a 24h overlap.
                  </p>
                </div>

                {rotations.length > 0 && (
                  <div className="space-y-1.5">
                    <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">
                      Rotation history
                    </div>
                    <ul className="space-y-1">
                      {rotations.slice(0, 5).map((r) => (
                        <li key={r.id} className="text-xs flex items-center gap-2 px-2 py-1 rounded border border-white/10 bg-zinc-950">
                          <Badge
                            variant="outline"
                            className={cn(
                              "text-[10px] px-1.5",
                              r.status === "ACTIVE" && "border-blue-400/40 text-blue-300",
                              r.status === "EXPIRED" && "border-emerald-400/30 text-emerald-300",
                              r.status === "CANCELLED" && "border-amber-400/30 text-amber-300",
                            )}
                          >
                            {r.status}
                          </Badge>
                          <span className="text-muted-foreground">{formatRelativeTime(r.rotated_at)}</span>
                          <span className="ml-auto text-[10px] text-muted-foreground/70 font-mono">
                            {Math.round(r.grace_seconds / 3600)}h grace
                          </span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                <div className="pt-3 border-t border-white/10">
                  <Button
                    size="sm"
                    variant="outline"
                    className="w-full justify-start text-red-400 border-red-500/30 hover:bg-red-500/[0.05]"
                    onClick={() => setConfirmDelete(true)}
                  >
                    <Trash2 className="h-3.5 w-3.5 mr-1.5" />
                    Delete credential
                  </Button>
                </div>
              </TabsContent>
            </div>
          </Tabs>
        </SheetContent>
      </Sheet>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete credential?</AlertDialogTitle>
            <AlertDialogDescription>
              <span className="font-mono">{credential.name}</span> will be permanently deleted.
              Agents that use this credential will start failing immediately. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <Cancel>Cancel</Cancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={handleDelete}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[100px_1fr] gap-2 text-xs">
      <span className="text-muted-foreground">{label}</span>
      <span className="text-foreground/90 font-mono">{children}</span>
    </div>
  )
}

// Inline alias so we don't have to import AlertDialogCancel everywhere — saves a line.
function Cancel({ children }: { children: React.ReactNode }) {
  return <AlertDialogCancel>{children}</AlertDialogCancel>
}

