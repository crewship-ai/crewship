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
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { cn } from "@/lib/utils"

// ── Helpers ──

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

/** A single row inside a card: label left, content right */
function Row({ label, description, children, border = true }: {
  label: string
  description?: string
  children: React.ReactNode
  border?: boolean
}) {
  return (
    <div className={cn("flex items-center justify-between gap-4 px-5 py-3.5 min-h-[48px]", border && "border-b border-white/[0.04] last:border-b-0")}>
      <div className="shrink-0">
        <div className="text-[13px] text-foreground">{label}</div>
        {description && <div className="text-[11px] text-muted-foreground/30 mt-0.5">{description}</div>}
      </div>
      <div className="flex items-center gap-2 min-w-0 justify-end">{children}</div>
    </div>
  )
}

function CopyableText({ value, mono }: { value: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false)
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            onClick={() => { navigator.clipboard.writeText(value); setCopied(true); setTimeout(() => setCopied(false), 1500) }}
            className={cn("text-[13px] text-muted-foreground/60 hover:text-foreground transition-colors truncate text-right", mono && "font-mono")}
          >
            {value}
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" className="text-xs">
          {copied ? <span className="flex items-center gap-1"><Check className="h-3 w-3 text-emerald-400" /> Copied</span> : <span className="flex items-center gap-1"><Copy className="h-3 w-3" /> Copy</span>}
        </TooltipContent>
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
    <div className="space-y-6">
      {/* ── Account ── */}
      <div>
        <h3 className="text-[14px] font-medium text-foreground mb-3">Account</h3>
        <Card className="border-white/[0.06]">
          <CardContent className="p-0">
            <Row label="Profile picture">
              <div className="w-9 h-9 rounded-full bg-primary/80 ring-2 ring-white/[0.08] flex items-center justify-center text-primary-foreground text-[11px] font-semibold">
                {initials}
              </div>
            </Row>
            <Row label="Email">
              {userEmail ? <CopyableText value={userEmail} mono /> : <span className="text-[13px] text-muted-foreground/30">Not set</span>}
            </Row>
            <Row label="Full name">
              <span className="text-[13px] text-muted-foreground/60">{userName ?? "Not set"}</span>
            </Row>
            <Row label="Password">
              <span className="text-[13px] text-muted-foreground/25 tracking-[0.15em]">{"••••••••"}</span>
            </Row>
          </CardContent>
        </Card>
      </div>

      {/* ── Workspace ── */}
      <div>
        <h3 className="text-[14px] font-medium text-foreground mb-3">Workspace</h3>
        <Card className="border-white/[0.06]">
          <CardContent className="p-0">
            <Row label="Role">
              {role ? (
                <Badge variant="outline" className={cn("text-[10px] font-medium", roleCls[role] ?? "")}>
                  {role}
                </Badge>
              ) : (
                <span className="text-[13px] text-muted-foreground/30">Not assigned</span>
              )}
            </Row>
            {workspaceName && (
              <Row label="Organization">
                <span className="text-[13px] text-muted-foreground/60">{workspaceName}</span>
              </Row>
            )}
            {joinedAt && (
              <Row label="Joined">
                <span className="text-[13px] text-muted-foreground/40 font-mono tabular-nums">
                  {new Date(joinedAt).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })}
                </span>
              </Row>
            )}
          </CardContent>
        </Card>
      </div>

      {/* ── Session ── */}
      <div>
        <h3 className="text-[14px] font-medium text-foreground mb-3">Session</h3>
        <Card className="border-white/[0.06]">
          <CardContent className="p-0">
            <Row label="Status">
              <span className="flex items-center gap-1.5 text-[13px] text-foreground">
                <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 animate-pulse" />
                Active
              </span>
            </Row>
            {expiresIn && (
              <Row label="Expires">
                <span className="text-[13px] text-muted-foreground/40 font-mono tabular-nums">{expiresIn}</span>
              </Row>
            )}
            <Row label="Sign out of this device" border={false}>
              <Button
                variant="ghost"
                size="sm"
                className="h-7 px-2.5 text-[12px] text-red-400/60 hover:text-red-400 hover:bg-red-500/10"
                onClick={onSignOut}
              >
                <LogOut className="h-3 w-3 mr-1.5" />
                Sign out
              </Button>
            </Row>
          </CardContent>
        </Card>
      </div>

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

      {/* ── CLI Tokens ── */}
      <div>
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-[14px] font-medium text-foreground">CLI Tokens</h3>
          {!showCreateForm && (
            <Button size="sm" variant="outline" className="h-7 gap-1.5 text-[11px] border-white/[0.08]" onClick={() => setShowCreateForm(true)}>
              <Plus className="h-3 w-3" />New
            </Button>
          )}
        </div>
        <Card className="border-white/[0.06]">
          <CardContent className="p-0">
            {/* Create form */}
            <AnimatePresence>
              {showCreateForm && (
                <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="overflow-hidden">
                  <div className="px-5 py-3.5 border-b border-white/[0.04] flex items-end gap-2">
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
              <div className="px-5 py-6 text-center"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground/20 mx-auto" /></div>
            ) : tokens.length === 0 && !showCreateForm ? (
              <div className="px-5 py-6 text-center">
                <p className="text-[12px] text-muted-foreground/25">No tokens yet</p>
              </div>
            ) : (
              <>
                {activeTokens.map((token) => (
                  <Row key={token.id} label={token.name}>
                    <span className="text-[10px] text-muted-foreground/25 font-mono flex items-center gap-1">
                      <Clock className="h-2.5 w-2.5" />{timeAgo(token.created_at)}
                    </span>
                    {token.last_used_at && (
                      <span className="text-[10px] text-muted-foreground/20 font-mono hidden sm:inline">
                        used {timeAgo(token.last_used_at)}
                      </span>
                    )}
                    <span className="w-1.5 h-1.5 rounded-full bg-emerald-500 shrink-0" />
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
                  </Row>
                ))}
                {revokedTokens.map((token) => (
                  <div key={token.id} className="flex items-center justify-between px-5 py-2.5 border-b border-white/[0.04] last:border-b-0 opacity-35">
                    <span className="text-[13px] text-muted-foreground/50 line-through">{token.name}</span>
                    <span className="text-[10px] text-muted-foreground/20 font-mono">revoked</span>
                  </div>
                ))}
              </>
            )}
          </CardContent>
        </Card>
      </div>

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
