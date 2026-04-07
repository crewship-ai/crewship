"use client"

import { useCallback, useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  LogOut, Copy, Check, Key, Plus, Trash2, Clock, Loader2,
  Terminal, Eye, EyeOff, AlertTriangle,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Separator } from "@/components/ui/separator"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { cn } from "@/lib/utils"

// ── Shared utilities ──

const roleCls: Record<string, string> = {
  OWNER: "bg-amber-500/20 text-amber-400 border-amber-500/30",
  ADMIN: "bg-blue-500/20 text-blue-400 border-blue-500/30",
  MANAGER: "bg-teal-500/20 text-teal-400 border-teal-500/30",
  MEMBER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
  VIEWER: "bg-white/[0.06] text-muted-foreground border-white/[0.08]",
}

function useTimeUntil(dateStr: string | null | undefined) {
  const [text, setText] = useState("")
  useEffect(() => {
    if (!dateStr) return
    function update() {
      const diff = new Date(dateStr!).getTime() - Date.now()
      if (diff <= 0) { setText("Expired"); return }
      const h = Math.floor(diff / 3600000)
      const m = Math.floor((diff % 3600000) / 60000)
      setText(h > 24 ? `${Math.floor(h / 24)}d ${h % 24}h` : h > 0 ? `${h}h ${m}m` : `${m}m`)
    }
    update()
    const id = setInterval(update, 60000)
    return () => clearInterval(id)
  }, [dateStr])
  return text
}

function timeAgo(dateStr: string): string {
  const diff = Date.now() - new Date(dateStr).getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return "just now"
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return new Date(dateStr).toLocaleDateString("en-US", { month: "short", day: "numeric" })
}

function FieldRow({ label, children, className }: {
  label: string; children: React.ReactNode; className?: string
}) {
  return (
    <div className={cn("flex flex-col sm:flex-row sm:items-center gap-1 sm:gap-0 py-2.5", className)}>
      <span className="text-[13px] text-muted-foreground/50 sm:w-[140px] shrink-0">{label}</span>
      <div className="flex-1 min-w-0 flex items-center gap-2">{children}</div>
    </div>
  )
}

function CopyableValue({ value, mono }: { value: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false)
  function handleCopy() {
    navigator.clipboard.writeText(value)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button onClick={handleCopy} className={cn("text-[13px] text-foreground truncate text-left hover:text-foreground/80 transition-colors", mono && "font-mono")}>
            {value}
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" className="text-xs">
          {copied ? <span className="flex items-center gap-1"><Check className="h-3 w-3 text-emerald-400" /> Copied</span> : <span className="flex items-center gap-1"><Copy className="h-3 w-3" /> Click to copy</span>}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

function P2Chip() {
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Badge variant="outline" className="text-[8px] px-1 py-0 h-4 border-white/[0.06] text-muted-foreground/25 cursor-default shrink-0">EDIT</Badge>
        </TooltipTrigger>
        <TooltipContent side="left" className="text-xs">Coming in Phase 2</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

// ── Token types ──

interface CLIToken {
  id: string
  name: string
  created_at: string
  last_used_at?: string
  revoked_at?: string
}

// ── Props ──

interface ProfileSectionProps {
  userName?: string | null
  userEmail?: string | null
  role?: string | null
  workspaceName?: string | null
  joinedAt?: string | null
  sessionExpires?: string | null
  onSignOut?: () => void
}

// ── Component ──

export function ProfileSection({
  userName, userEmail, role, workspaceName, joinedAt, sessionExpires, onSignOut,
}: ProfileSectionProps) {
  const initials = (userName ?? "U").split(" ").map((n) => n[0]).join("").slice(0, 2).toUpperCase()
  const expiresIn = useTimeUntil(sessionExpires)

  // ── Token state ──
  const [tokens, setTokens] = useState<CLIToken[]>([])
  const [tokensLoading, setTokensLoading] = useState(true)
  const [creating, setCreating] = useState(false)
  const [tokenName, setTokenName] = useState("")
  const [showCreateForm, setShowCreateForm] = useState(false)
  const [newToken, setNewToken] = useState<{ token: string; name: string } | null>(null)
  const [tokenCopied, setTokenCopied] = useState(false)
  const [tokenVisible, setTokenVisible] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<CLIToken | null>(null)
  const [revoking, setRevoking] = useState(false)

  const fetchTokens = useCallback(async () => {
    try {
      const res = await fetch("/api/v1/auth/cli-tokens")
      if (res.ok) { const data = await res.json(); setTokens(data.data ?? []) }
    } catch { /* ignore */ }
    finally { setTokensLoading(false) }
  }, [])

  useEffect(() => { fetchTokens() }, [fetchTokens])

  async function handleCreateToken() {
    if (!tokenName.trim()) return
    setCreating(true)
    try {
      const res = await fetch("/api/v1/auth/cli-token", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: tokenName.trim() }),
      })
      if (res.ok) {
        const data = await res.json()
        setNewToken({ token: data.token, name: data.name })
        setTokenName(""); setShowCreateForm(false); setTokenVisible(false); setTokenCopied(false)
        fetchTokens()
      }
    } catch { /* ignore */ }
    finally { setCreating(false) }
  }

  async function handleRevoke() {
    if (!revokeTarget) return
    setRevoking(true)
    try {
      await fetch(`/api/v1/auth/cli-tokens/${revokeTarget.id}`, { method: "DELETE" })
      fetchTokens()
    } catch { /* ignore */ }
    finally { setRevoking(false); setRevokeTarget(null) }
  }

  function handleCopyToken(text: string) {
    navigator.clipboard.writeText(text)
    setTokenCopied(true)
    setTimeout(() => setTokenCopied(false), 2000)
  }

  const activeTokens = tokens.filter((t) => !t.revoked_at)
  const revokedTokens = tokens.filter((t) => t.revoked_at)

  return (
    <div className="space-y-5">
      {/* ── Profile card ── */}
      <Card className="border-white/[0.06]">
        <CardContent className="p-0">
          {/* Header */}
          <div className="p-5 sm:p-6">
            <div className="flex items-start sm:items-center gap-4 flex-col sm:flex-row">
              <div className="w-14 h-14 rounded-full bg-primary/80 ring-2 ring-white/[0.08] flex items-center justify-center text-primary-foreground text-[15px] font-semibold shrink-0">
                {initials}
              </div>
              <div className="min-w-0 flex-1">
                <h3 className="text-[16px] font-semibold text-foreground truncate">{userName ?? "User"}</h3>
                <p className="text-[13px] text-muted-foreground/40 mt-0.5 truncate font-mono">{userEmail ?? ""}</p>
                <div className="flex flex-wrap items-center gap-2 mt-2">
                  {role && <Badge variant="outline" className={cn("text-[10px] font-medium", roleCls[role] ?? "")}>{role}</Badge>}
                  {workspaceName && <span className="text-[11px] text-muted-foreground/25">{workspaceName}</span>}
                </div>
              </div>
            </div>
          </div>

          <Separator className="bg-white/[0.06]" />

          {/* Account */}
          <div className="px-5 sm:px-6 py-3">
            <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-0.5">Account</div>
            <FieldRow label="Full Name">
              <span className="text-[13px] text-foreground truncate">{userName ?? "Not set"}</span>
              <div className="ml-auto"><P2Chip /></div>
            </FieldRow>
            <FieldRow label="Email">
              {userEmail ? <CopyableValue value={userEmail} mono /> : <span className="text-[13px] text-foreground">Not set</span>}
              <div className="ml-auto"><P2Chip /></div>
            </FieldRow>
            <FieldRow label="Password">
              <span className="text-[13px] text-muted-foreground/40 tracking-wider">{"•".repeat(10)}</span>
              <div className="ml-auto"><P2Chip /></div>
            </FieldRow>
          </div>

          <Separator className="bg-white/[0.06]" />

          {/* Workspace */}
          <div className="px-5 sm:px-6 py-3">
            <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-0.5">Workspace</div>
            <FieldRow label="Role">
              {role ? <Badge variant="outline" className={cn("text-[10px] font-medium", roleCls[role] ?? "")}>{role}</Badge> : <span className="text-[13px] text-muted-foreground/40">Not assigned</span>}
            </FieldRow>
            {workspaceName && <FieldRow label="Organization"><span className="text-[13px] text-foreground">{workspaceName}</span></FieldRow>}
            {joinedAt && <FieldRow label="Joined"><span className="text-[13px] text-muted-foreground/60">{new Date(joinedAt).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })}</span></FieldRow>}
          </div>

          <Separator className="bg-white/[0.06]" />

          {/* Session */}
          <div className="px-5 sm:px-6 py-3">
            <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-0.5">Session</div>
            <FieldRow label="Status">
              <span className="flex items-center gap-1.5 text-[13px] text-foreground">
                <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse" />Active
              </span>
            </FieldRow>
            {expiresIn && <FieldRow label="Expires"><span className="text-[13px] text-muted-foreground/40">{expiresIn}</span></FieldRow>}
            <div className="pt-2 pb-1">
              <Button variant="ghost" size="sm" className="h-7 px-2 text-[12px] text-red-400/60 hover:text-red-400 hover:bg-red-500/10 gap-1.5" onClick={onSignOut}>
                <LogOut className="h-3 w-3" />Sign out
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* ── New token reveal ── */}
      <AnimatePresence>
        {newToken && (
          <motion.div initial={{ opacity: 0, y: -8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -8 }} transition={{ duration: 0.15 }}>
            <Card className="border-emerald-500/30 bg-emerald-500/[0.04]">
              <CardContent className="p-4 sm:p-5">
                <div className="flex items-start gap-3 mb-3">
                  <div className="w-7 h-7 rounded-md bg-emerald-500/15 flex items-center justify-center shrink-0">
                    <Key className="h-3.5 w-3.5 text-emerald-400" />
                  </div>
                  <div className="min-w-0 flex-1">
                    <h4 className="text-[13px] font-semibold text-emerald-400">Token created</h4>
                    <p className="text-[11px] text-emerald-400/50">Copy now — won&apos;t be shown again.</p>
                  </div>
                  <Button variant="ghost" size="sm" className="h-6 text-[10px] text-muted-foreground/40" onClick={() => setNewToken(null)}>Dismiss</Button>
                </div>
                <div className="bg-black/40 border border-emerald-500/20 rounded-md p-2.5 flex items-center gap-2">
                  <Terminal className="h-3 w-3 text-emerald-500/40 shrink-0" />
                  <code className="flex-1 text-[11px] font-mono text-emerald-300/90 break-all select-all leading-relaxed">
                    {tokenVisible ? newToken.token : newToken.token.slice(0, 18) + "•".repeat(24)}
                  </code>
                  <Button variant="ghost" size="icon" className="h-6 w-6 text-emerald-400/50 hover:text-emerald-400" onClick={() => setTokenVisible(!tokenVisible)}>
                    {tokenVisible ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                  </Button>
                  <Button variant="ghost" size="sm" className={cn("h-6 gap-1 text-[10px]", tokenCopied ? "text-emerald-400" : "text-emerald-400/50")} onClick={() => handleCopyToken(newToken.token)}>
                    {tokenCopied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
                    {tokenCopied ? "Copied" : "Copy"}
                  </Button>
                </div>
                <p className="mt-2 text-[10px] text-muted-foreground/30 font-mono">
                  $ crewship auth login --token &lt;token&gt;
                </p>
              </CardContent>
            </Card>
          </motion.div>
        )}
      </AnimatePresence>

      {/* ── CLI Tokens card ── */}
      <Card className="border-white/[0.06]">
        <CardContent className="p-0">
          <div className="flex items-center justify-between px-5 sm:px-6 py-4">
            <div>
              <div className="flex items-center gap-2">
                <Key className="h-3.5 w-3.5 text-muted-foreground/40" />
                <h4 className="text-[13px] font-medium text-foreground">CLI Tokens</h4>
              </div>
              <p className="text-[11px] text-muted-foreground/30 mt-0.5 ml-[22px]">Authenticate the Crewship CLI.</p>
            </div>
            {!showCreateForm && (
              <Button size="sm" variant="outline" className="h-7 gap-1.5 text-[11px] border-white/[0.08]" onClick={() => setShowCreateForm(true)}>
                <Plus className="h-3 w-3" />New
              </Button>
            )}
          </div>

          {/* Create form */}
          <AnimatePresence>
            {showCreateForm && (
              <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="overflow-hidden">
                <div className="px-5 sm:px-6 pb-4 flex items-end gap-2">
                  <Input
                    value={tokenName} onChange={(e) => setTokenName(e.target.value)}
                    placeholder="Token name, e.g. MacBook Pro"
                    className="h-8 flex-1 bg-white/[0.03] border-white/[0.08] text-[12px]"
                    onKeyDown={(e) => e.key === "Enter" && handleCreateToken()}
                    autoFocus
                  />
                  <Button size="sm" className="h-8 text-[11px] gap-1" onClick={handleCreateToken} disabled={creating || !tokenName.trim()}>
                    {creating && <Loader2 className="h-3 w-3 animate-spin" />}Create
                  </Button>
                  <Button variant="ghost" size="sm" className="h-8 text-[11px] text-muted-foreground/50" onClick={() => { setShowCreateForm(false); setTokenName("") }}>Cancel</Button>
                </div>
              </motion.div>
            )}
          </AnimatePresence>

          {/* Token list */}
          {tokensLoading ? (
            <div className="px-6 pb-4 text-center"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground/20 mx-auto" /></div>
          ) : tokens.length === 0 ? (
            <div className="px-6 pb-5 text-center">
              <p className="text-[12px] text-muted-foreground/25">No tokens yet</p>
            </div>
          ) : (
            <div>
              {activeTokens.map((token) => (
                <div key={token.id} className="flex items-center gap-3 px-5 sm:px-6 py-2.5 border-t border-white/[0.04] hover:bg-white/[0.01] transition-colors">
                  <Key className="h-3 w-3 text-emerald-400/50 shrink-0" />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-[12px] font-medium text-foreground truncate">{token.name}</span>
                      <span className="w-1 h-1 rounded-full bg-emerald-500 shrink-0" />
                    </div>
                    <div className="flex items-center gap-2 mt-0.5">
                      <span className="text-[10px] text-muted-foreground/25 font-mono flex items-center gap-1">
                        <Clock className="h-2.5 w-2.5" />{timeAgo(token.created_at)}
                      </span>
                      {token.last_used_at && (
                        <span className="text-[10px] text-muted-foreground/25 font-mono">used {timeAgo(token.last_used_at)}</span>
                      )}
                    </div>
                  </div>
                  <TooltipProvider delayDuration={0}>
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Button variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground/20 hover:text-red-400 hover:bg-red-500/10" onClick={() => setRevokeTarget(token)}>
                          <Trash2 className="h-3 w-3" />
                        </Button>
                      </TooltipTrigger>
                      <TooltipContent side="left" className="text-xs">Revoke</TooltipContent>
                    </Tooltip>
                  </TooltipProvider>
                </div>
              ))}
              {revokedTokens.map((token) => (
                <div key={token.id} className="flex items-center gap-3 px-5 sm:px-6 py-2 border-t border-white/[0.04] opacity-35">
                  <Key className="h-3 w-3 text-muted-foreground/30 shrink-0" />
                  <span className="text-[12px] text-muted-foreground/50 line-through truncate">{token.name}</span>
                  <span className="text-[10px] text-muted-foreground/20 font-mono ml-auto">revoked</span>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>

      {/* Revoke dialog */}
      <AlertDialog open={!!revokeTarget} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2">
              <AlertTriangle className="h-4 w-4 text-red-400" />Revoke token
            </AlertDialogTitle>
            <AlertDialogDescription>
              Revoke <strong>{revokeTarget?.name}</strong>? CLI sessions using this token will be disconnected immediately.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={handleRevoke} className="bg-destructive text-destructive-foreground hover:bg-destructive/90" disabled={revoking}>
              {revoking && <Loader2 className="h-3.5 w-3.5 animate-spin mr-1.5" />}Revoke
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
