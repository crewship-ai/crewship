"use client"

import { useCallback, useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  LogOut, Copy, Check, Key, Plus, Trash2, Clock, Loader2,
  Terminal, Eye, EyeOff, AlertTriangle,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { cn } from "@/lib/utils"
import { SettingsCard, SettingsRow, SettingsEmpty } from "../shared"

// ── Helpers ─────────────────────────────────────────────────────────

const roleCls: Record<string, string> = {
  OWNER: "bg-muted text-foreground border-border",
  ADMIN: "bg-muted text-foreground border-border",
  MANAGER: "bg-muted text-foreground border-border",
  MEMBER: "bg-muted text-muted-foreground border-border",
  VIEWER: "bg-muted text-muted-foreground border-border",
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

function CopyableText({ value, mono }: { value: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false)
  return (
    <TooltipProvider delayDuration={0}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            onClick={() => { navigator.clipboard.writeText(value); setCopied(true); setTimeout(() => setCopied(false), 1500) }}
            className={cn("text-xs text-muted-foreground hover:text-foreground transition-colors truncate text-right", mono && "font-mono")}
          >
            {value}
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" className="text-[11px]">
          {copied ? <span className="flex items-center gap-1"><Check className="h-3 w-3" /> Copied</span> : <span className="flex items-center gap-1"><Copy className="h-3 w-3" /> Copy</span>}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

// ── Token types ─────────────────────────────────────────────────────

interface CLIToken {
  id: string
  name: string
  created_at: string
  last_used_at?: string
  revoked_at?: string
}

// ── Props ───────────────────────────────────────────────────────────

interface ProfileSectionProps {
  userName?: string | null
  userEmail?: string | null
  role?: string | null
  workspaceName?: string | null
  joinedAt?: string | null
  sessionExpires?: string | null
  onSignOut?: () => void
}

// ── Component ───────────────────────────────────────────────────────

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
      {/* ── Account ── */}
      <SettingsCard title="Account" description="Your identity on this instance">
        <SettingsRow label="Profile picture">
          <div className="h-7 w-7 rounded-full bg-primary/80 ring-2 ring-border flex items-center justify-center text-primary-foreground text-[10px] font-semibold">
            {initials}
          </div>
        </SettingsRow>
        <SettingsRow label="Email">
          {userEmail ? <CopyableText value={userEmail} mono /> : <span className="text-xs text-muted-foreground">Not set</span>}
        </SettingsRow>
        <SettingsRow label="Full name">
          <span className="text-xs text-muted-foreground">{userName ?? "Not set"}</span>
        </SettingsRow>
        <SettingsRow label="Password">
          <span className="text-xs text-muted-foreground tracking-[0.2em]">••••••••</span>
        </SettingsRow>
      </SettingsCard>

      {/* ── Workspace ── */}
      <SettingsCard title="Workspace" description="Your current organization and role">
        <SettingsRow label="Role">
          {role ? (
            <Badge variant="outline" className={cn("text-[10px] font-medium", roleCls[role] ?? "")}>
              {role}
            </Badge>
          ) : (
            <span className="text-xs text-muted-foreground">Not assigned</span>
          )}
        </SettingsRow>
        {workspaceName && (
          <SettingsRow label="Organization">
            <span className="text-xs text-muted-foreground">{workspaceName}</span>
          </SettingsRow>
        )}
        {joinedAt && (
          <SettingsRow label="Joined">
            <span className="text-[11px] text-muted-foreground font-mono tabular-nums">
              {new Date(joinedAt).toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })}
            </span>
          </SettingsRow>
        )}
      </SettingsCard>

      {/* ── Session ── */}
      <SettingsCard title="Session" description="Your current login session on this device">
        <SettingsRow label="Status">
          <span className="inline-flex items-center gap-1.5 text-xs text-foreground">
            <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" />
            Active
          </span>
        </SettingsRow>
        {expiresIn && (
          <SettingsRow label="Expires">
            <span className="text-[11px] text-muted-foreground font-mono tabular-nums">{expiresIn}</span>
          </SettingsRow>
        )}
        <SettingsRow label="Sign out of this device" border={false}>
          <Button
            variant="ghost"
            size="sm"
            className="h-7 px-2.5 text-xs text-destructive hover:text-destructive hover:bg-destructive/10"
            onClick={onSignOut}
          >
            <LogOut className="h-3 w-3 mr-1.5" />
            Sign out
          </Button>
        </SettingsRow>
      </SettingsCard>

      {/* ── New token reveal ── */}
      <AnimatePresence>
        {newToken && (
          <motion.div initial={{ opacity: 0, y: -6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -6 }} transition={{ duration: 0.15 }}>
            <div className="rounded-xl border border-primary/30 bg-primary/[0.04] p-4">
              <div className="flex items-start gap-2.5 mb-3">
                <div className="h-6 w-6 rounded-md bg-primary/15 flex items-center justify-center shrink-0">
                  <Key className="h-3 w-3 text-primary" />
                </div>
                <div className="min-w-0 flex-1">
                  <h4 className="text-xs font-semibold text-foreground">Token created</h4>
                  <p className="text-[11px] text-muted-foreground">Copy now — it won&apos;t be shown again.</p>
                </div>
                <Button variant="ghost" size="sm" className="h-6 text-[10px] text-muted-foreground" onClick={() => setNewToken(null)}>Dismiss</Button>
              </div>
              <div className="bg-muted/60 border border-border/60 rounded-md p-2 flex items-center gap-2">
                <Terminal className="h-3 w-3 text-muted-foreground shrink-0" />
                <code className="flex-1 text-[11px] font-mono text-foreground break-all select-all leading-relaxed">
                  {tokenVisible ? newToken.token : newToken.token.slice(0, 18) + "•".repeat(24)}
                </code>
                <Button variant="ghost" size="icon" className="h-6 w-6 text-muted-foreground hover:text-foreground" onClick={() => setTokenVisible(!tokenVisible)}>
                  {tokenVisible ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                </Button>
                <Button variant="ghost" size="sm" className={cn("h-6 gap-1 text-[10px]", tokenCopied ? "text-foreground" : "text-muted-foreground")} onClick={() => handleCopyToken(newToken.token)}>
                  {tokenCopied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
                  {tokenCopied ? "Copied" : "Copy"}
                </Button>
              </div>
              <p className="mt-2 text-[10px] text-muted-foreground font-mono">
                $ crewship auth login --token &lt;token&gt;
              </p>
            </div>
          </motion.div>
        )}
      </AnimatePresence>

      {/* ── CLI Tokens ── */}
      <SettingsCard
        title="CLI Tokens"
        description="Authenticate the crewship CLI against this workspace"
        actions={
          !showCreateForm && (
            <Button size="sm" variant="outline" className="h-7 px-2.5 gap-1.5 text-xs" onClick={() => setShowCreateForm(true)}>
              <Plus className="h-3 w-3" />New token
            </Button>
          )
        }
      >
        {/* Create form */}
        <AnimatePresence>
          {showCreateForm && (
            <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="overflow-hidden">
              <div className="px-4 py-2.5 border-b border-border/40 flex items-center gap-2">
                <Input
                  value={tokenName} onChange={(e) => setTokenName(e.target.value)}
                  placeholder="Token name, e.g. MacBook Pro"
                  className="h-7 flex-1 text-xs"
                  onKeyDown={(e) => e.key === "Enter" && handleCreateToken()}
                  autoFocus
                />
                <Button size="sm" className="h-7 px-2.5 text-xs gap-1" onClick={handleCreateToken} disabled={creating || !tokenName.trim()}>
                  {creating && <Loader2 className="h-3 w-3 animate-spin" />}Create
                </Button>
                <Button variant="ghost" size="sm" className="h-7 px-2 text-xs text-muted-foreground" onClick={() => { setShowCreateForm(false); setTokenName("") }}>Cancel</Button>
              </div>
            </motion.div>
          )}
        </AnimatePresence>

        {/* Token list */}
        {tokensLoading ? (
          <div className="px-4 py-6 text-center">
            <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground mx-auto" />
          </div>
        ) : tokens.length === 0 && !showCreateForm ? (
          <SettingsEmpty>No tokens yet</SettingsEmpty>
        ) : (
          <>
            {activeTokens.map((token) => (
              <SettingsRow key={token.id} label={token.name}>
                <span className="text-[10px] text-muted-foreground font-mono flex items-center gap-1">
                  <Clock className="h-2.5 w-2.5" />{timeAgo(token.created_at)}
                </span>
                {token.last_used_at && (
                  <span className="text-[10px] text-muted-foreground font-mono hidden sm:inline">
                    used {timeAgo(token.last_used_at)}
                  </span>
                )}
                <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 shrink-0" />
                <TooltipProvider delayDuration={0}>
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Button variant="ghost" size="icon" className="h-6 w-6 text-muted-foreground hover:text-destructive hover:bg-destructive/10" onClick={() => setRevokeTarget(token)}>
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent side="left" className="text-[11px]">Revoke</TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              </SettingsRow>
            ))}
            {revokedTokens.map((token) => (
              <div key={token.id} className="flex items-center justify-between px-4 py-2 border-b border-border/40 last:border-b-0 opacity-40">
                <span className="text-xs text-muted-foreground line-through">{token.name}</span>
                <span className="text-[10px] text-muted-foreground font-mono">revoked</span>
              </div>
            ))}
          </>
        )}
      </SettingsCard>

      {/* Revoke dialog */}
      <AlertDialog open={!!revokeTarget} onOpenChange={(open) => !open && setRevokeTarget(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="flex items-center gap-2 text-sm">
              <AlertTriangle className="h-4 w-4 text-destructive" />Revoke token
            </AlertDialogTitle>
            <AlertDialogDescription className="text-xs">
              Revoke <strong>{revokeTarget?.name}</strong>? CLI sessions using this token will be disconnected immediately.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel className="h-7 text-xs">Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={handleRevoke} className="h-7 text-xs bg-destructive text-destructive-foreground hover:bg-destructive/90" disabled={revoking}>
              {revoking && <Loader2 className="h-3 w-3 animate-spin mr-1.5" />}Revoke
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
