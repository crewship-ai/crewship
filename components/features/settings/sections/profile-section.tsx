"use client"

import { useCallback, useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import {
  LogOut, Copy, Check, Key, Plus, Trash2, Clock, Loader2,
  Terminal, Eye, EyeOff, AlertTriangle, Shield,
} from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent,
  AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
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
  // Patch J/K + M2 fields. Older API versions omit these; UI handles
  // both shapes — undefined tier renders as STANDARD, undefined
  // scopes means "unrestricted", undefined expires_at means "no
  // expiry".
  tier?: "STANDARD" | "ADMIN"
  scopes?: string[]
  expires_at?: string
}

// All scope strings the backend's knownScopes allowlist accepts.
// Keep in sync with internal/api/cli_token.go knownScopes — that's
// the source of truth at issue time, the UI just mirrors the list
// so users can pick from a menu instead of typing strings.
//
// Grouped by resource for the dialog's three-column layout. Each
// group's "*" wildcard is the top-of-group selector; selecting it
// disables the per-action checkboxes (the user has chosen "all
// actions on this resource").
const SCOPE_GROUPS: Array<{ resource: string; actions: string[] }> = [
  { resource: "agents", actions: ["read", "write", "run"] },
  { resource: "crews", actions: ["read", "write"] },
  { resource: "credentials", actions: ["read", "write"] },
  { resource: "skills", actions: ["read", "write"] },
  { resource: "webhooks", actions: ["read", "write"] },
  { resource: "workspace", actions: ["read", "admin"] },
]

// Token-tier visual marker. Distinct colour from workspace-role so
// a glance can't confuse "OWNER role on the workspace" with "ADMIN
// tier on this token". Admin tier uses a warning-tinted style to
// reinforce the "more dangerous, shorter lifetime" contract.
const tierCls: Record<string, string> = {
  STANDARD: "bg-muted text-muted-foreground border-border",
  ADMIN: "bg-destructive/10 text-destructive border-destructive/40",
}

// Expiry presets — labels the user picks, mapped to seconds the
// API accepts. 0 means "no expiry" (only valid for STANDARD); ADMIN
// tier silently rounds up to the 60s floor if 0 is sent.
const EXPIRY_PRESETS: Array<{ label: string; value: number }> = [
  { label: "1 hour", value: 60 * 60 },
  { label: "24 hours", value: 24 * 60 * 60 },
  { label: "7 days", value: 7 * 24 * 60 * 60 },
  { label: "30 days", value: 30 * 24 * 60 * 60 },
  { label: "90 days", value: 90 * 24 * 60 * 60 },
  { label: "Never", value: 0 },
]

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
  const [newToken, setNewToken] = useState<{ token: string; name: string; tier: string } | null>(null)
  const [tokenCopied, setTokenCopied] = useState(false)
  const [tokenVisible, setTokenVisible] = useState(false)
  const [revokeTarget, setRevokeTarget] = useState<CLIToken | null>(null)
  const [revoking, setRevoking] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  // Patch J/K + M2: tier + scopes + expiry form state.
  //
  // Default tier='STANDARD' because the dialog opens for everyone;
  // OWNER-only ADMIN tier is gated server-side, and selecting it
  // when the caller isn't OWNER returns 403 with a clear "ADMIN
  // tier requires OWNER role" message that we surface inline.
  //
  // Default expiry='Never' for STANDARD (matches GitHub PAT
  // default), but the form auto-switches to 24h when the user
  // flips tier to ADMIN (since ADMIN requires non-zero expiry).
  const [tokenTier, setTokenTier] = useState<"STANDARD" | "ADMIN">("STANDARD")
  const [tokenExpirySeconds, setTokenExpirySeconds] = useState<number>(0)
  const [tokenScopes, setTokenScopes] = useState<Set<string>>(new Set())

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
    setCreateError(null)
    try {
      // Body shape matches createTokenRequest in internal/api/cli_token.go.
      // Omit fields the backend treats as defaults — empty scopes is
      // unrestricted (no scope-vs-role check fires), expires_in_seconds=0
      // is "default" (no expiry for STANDARD, 24h for ADMIN).
      const body: Record<string, unknown> = { name: tokenName.trim() }
      if (tokenTier === "ADMIN") body.tier = "ADMIN"
      if (tokenScopes.size > 0) body.scopes = Array.from(tokenScopes)
      if (tokenExpirySeconds > 0) body.expires_in_seconds = tokenExpirySeconds

      const res = await fetch("/api/v1/auth/cli-token", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (res.ok) {
        const data = await res.json()
        setNewToken({ token: data.token, name: data.name, tier: data.tier ?? "STANDARD" })
        setTokenName("")
        setTokenTier("STANDARD")
        setTokenExpirySeconds(0)
        setTokenScopes(new Set())
        setShowCreateForm(false)
        setTokenVisible(false)
        setTokenCopied(false)
        fetchTokens()
      } else {
        // Surface backend error messages (unknown scope, scope-exceeds-
        // role, OWNER-only ADMIN, missing HMAC key) so the user sees
        // why instead of a silent fail.
        const errBody = await res.json().catch(() => ({}))
        setCreateError(errBody.error ?? `Request failed (${res.status})`)
      }
    } catch (e) {
      setCreateError(e instanceof Error ? e.message : "Network error")
    }
    finally { setCreating(false) }
  }

  // Auto-pick 24h expiry when tier flips to ADMIN. The backend
  // requires non-zero expiry for ADMIN; defaulting to 24h matches
  // adminTokenDefaultLifetime in cli_token.go.
  useEffect(() => {
    if (tokenTier === "ADMIN" && tokenExpirySeconds === 0) {
      setTokenExpirySeconds(24 * 60 * 60)
    }
  }, [tokenTier, tokenExpirySeconds])

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
          <div className="h-7 w-7 rounded-full bg-primary ring-2 ring-border flex items-center justify-center text-primary-foreground text-[10px] font-semibold">
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
                  <h4 className="text-xs font-semibold text-foreground flex items-center gap-1.5">
                    Token created
                    {newToken.tier && (
                      <Badge
                        variant="outline"
                        className={cn("text-[9px] font-medium px-1.5 py-0 leading-none h-4", tierCls[newToken.tier])}
                      >
                        {newToken.tier === "ADMIN" && <Shield className="h-2.5 w-2.5 mr-0.5" />}
                        {newToken.tier}
                      </Badge>
                    )}
                  </h4>
                  <p className="text-[11px] text-muted-foreground">
                    {newToken.tier === "ADMIN"
                      ? "Admin-tier token — short-lived (≤7d), per-use audited. Treat as a single-use disposable."
                      : "Copy now — it won’t be shown again."}
                  </p>
                </div>
                <Button variant="ghost" size="sm" className="h-6 text-[10px] text-muted-foreground" onClick={() => setNewToken(null)}>Dismiss</Button>
              </div>
              <div className="bg-muted/60 border border-border/60 rounded-md p-2 flex items-center gap-2">
                <Terminal className="h-3 w-3 text-muted-foreground shrink-0" />
                <code className="flex-1 text-[11px] font-mono text-foreground break-all select-all leading-relaxed">
                  {tokenVisible ? newToken.token : newToken.token.slice(0, 18) + "•".repeat(24)}
                </code>
                <Button variant="ghost" size="icon" className="h-6 w-6 text-muted-foreground hover:text-foreground" onClick={() => setTokenVisible(!tokenVisible)} aria-label={tokenVisible ? "Hide token" : "Show token"}>
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
        {/* Create form — full token issuance dialog with tier, scopes,
            expiry. Animates open inline rather than via modal so the
            generated token reveal (below) sits next to the form. */}
        <AnimatePresence>
          {showCreateForm && (
            <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="overflow-hidden">
              <div className="px-4 py-4 border-b border-border/40 space-y-3">
                {/* Name */}
                <div className="space-y-1">
                  <Label htmlFor="token-name" className="text-[11px]">Token name</Label>
                  <Input
                    id="token-name"
                    value={tokenName} onChange={(e) => setTokenName(e.target.value)}
                    placeholder="e.g. MacBook Pro, ci-deploy-bot"
                    className="h-8 text-xs"
                    onKeyDown={(e) => e.key === "Enter" && !e.shiftKey && handleCreateToken()}
                    autoFocus
                  />
                </div>

                {/* Tier + expiry on one row — both are short-list selects */}
                <div className="grid grid-cols-2 gap-3">
                  <div className="space-y-1">
                    <Label className="text-[11px] flex items-center gap-1.5">
                      Tier
                      <TooltipProvider delayDuration={0}>
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <span className="cursor-help text-muted-foreground">ⓘ</span>
                          </TooltipTrigger>
                          <TooltipContent side="top" className="max-w-xs text-[11px]">
                            <strong>STANDARD</strong> is the normal CLI token — your full
                            workspace role. <strong>ADMIN</strong> is HMAC-keyed,
                            short-lived (≤7d), per-use audited; OWNER role required to
                            issue.
                          </TooltipContent>
                        </Tooltip>
                      </TooltipProvider>
                    </Label>
                    <Select value={tokenTier} onValueChange={(v) => setTokenTier(v as "STANDARD" | "ADMIN")}>
                      <SelectTrigger className="h-8 text-xs">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="STANDARD" className="text-xs">Standard</SelectItem>
                        <SelectItem value="ADMIN" className="text-xs">
                          <span className="flex items-center gap-1.5">
                            <Shield className="h-3 w-3 text-destructive" />
                            Admin (OWNER only)
                          </span>
                        </SelectItem>
                      </SelectContent>
                    </Select>
                  </div>

                  <div className="space-y-1">
                    <Label className="text-[11px]">Expires</Label>
                    <Select
                      value={String(tokenExpirySeconds)}
                      onValueChange={(v) => setTokenExpirySeconds(Number(v))}
                    >
                      <SelectTrigger className="h-8 text-xs">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {EXPIRY_PRESETS
                          // ADMIN can't choose Never — backend rejects with 400.
                          .filter((p) => tokenTier !== "ADMIN" || p.value !== 0)
                          // ADMIN can't exceed 7d ceiling.
                          .filter((p) => tokenTier !== "ADMIN" || p.value <= 7 * 24 * 60 * 60)
                          .map((p) => (
                            <SelectItem key={p.value} value={String(p.value)} className="text-xs">
                              {p.label}
                            </SelectItem>
                          ))}
                      </SelectContent>
                    </Select>
                  </div>
                </div>

                {/* Scopes — three-column grid of checkboxes grouped by resource.
                    Empty selection = unrestricted (full role inherited).
                    Selecting <resource>:* implicitly grants every action under
                    that resource; the per-action checkboxes stay enabled so the
                    user can mix-and-match (backend's canScope handles both shapes). */}
                <div className="space-y-1.5">
                  <Label className="text-[11px] flex items-center justify-between">
                    <span>Scopes {tokenScopes.size > 0 && <span className="text-muted-foreground">({tokenScopes.size} selected)</span>}</span>
                    {tokenScopes.size > 0 && (
                      <button
                        type="button"
                        onClick={() => setTokenScopes(new Set())}
                        className="text-[10px] text-muted-foreground hover:text-foreground transition-colors"
                      >
                        Clear
                      </button>
                    )}
                  </Label>
                  <p className="text-[10px] text-muted-foreground">
                    Leave empty for unrestricted token (full role permissions).
                    Pick scopes to narrow — e.g. <code className="font-mono">agents:run</code> for a CI runner that only spawns agents.
                  </p>
                  <div className="grid grid-cols-3 gap-x-3 gap-y-1.5 border border-border/40 rounded-md p-2.5 bg-muted/20">
                    {SCOPE_GROUPS.map((group) => (
                      <div key={group.resource} className="space-y-1">
                        <div className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wide">
                          {group.resource}
                        </div>
                        <label className="flex items-center gap-1.5 text-[11px] cursor-pointer">
                          <Checkbox
                            checked={tokenScopes.has(`${group.resource}:*`)}
                            onCheckedChange={(checked) => {
                              const next = new Set(tokenScopes)
                              const wild = `${group.resource}:*`
                              if (checked) next.add(wild)
                              else next.delete(wild)
                              setTokenScopes(next)
                            }}
                            className="h-3 w-3"
                          />
                          <span className="font-mono text-[10px]">*</span>
                          <span className="text-muted-foreground text-[10px]">(all)</span>
                        </label>
                        {group.actions.map((action) => {
                          const scope = `${group.resource}:${action}`
                          return (
                            <label key={scope} className="flex items-center gap-1.5 text-[11px] cursor-pointer">
                              <Checkbox
                                checked={tokenScopes.has(scope)}
                                onCheckedChange={(checked) => {
                                  const next = new Set(tokenScopes)
                                  if (checked) next.add(scope)
                                  else next.delete(scope)
                                  setTokenScopes(next)
                                }}
                                className="h-3 w-3"
                              />
                              <span className="font-mono text-[10px]">{action}</span>
                            </label>
                          )
                        })}
                      </div>
                    ))}
                  </div>
                </div>

                {/* Inline error from backend (unknown scope, OWNER-only, etc.) */}
                {createError && (
                  <div className="text-[11px] text-destructive bg-destructive/10 border border-destructive/30 rounded-md px-2.5 py-1.5 flex items-start gap-1.5">
                    <AlertTriangle className="h-3 w-3 mt-0.5 shrink-0" />
                    <span>{createError}</span>
                  </div>
                )}

                <div className="flex items-center justify-end gap-2 pt-1">
                  <Button variant="ghost" size="sm" className="h-7 px-2 text-xs text-muted-foreground" onClick={() => {
                    setShowCreateForm(false)
                    setTokenName("")
                    setTokenTier("STANDARD")
                    setTokenExpirySeconds(0)
                    setTokenScopes(new Set())
                    setCreateError(null)
                  }}>Cancel</Button>
                  <Button size="sm" className="h-7 px-3 text-xs gap-1" onClick={handleCreateToken} disabled={creating || !tokenName.trim()}>
                    {creating && <Loader2 className="h-3 w-3 animate-spin" />}Create token
                  </Button>
                </div>
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
            {activeTokens.map((token) => <TokenListItem key={token.id} token={token} onRevoke={() => setRevokeTarget(token)} />)}
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

// ── TokenListItem ─────────────────────────────────────────────────────
//
// Renders a single active CLI token row with the full v99/M2 picture:
// tier badge (visual differentiator between STANDARD and the more-
// dangerous ADMIN), scope pill chips (so the user remembers what the
// token can do without opening DB), and an expires countdown that
// turns destructive-red when ≤24h remain. Pre-M2 tokens (no tier in
// the response) render exactly like STANDARD/unrestricted so the
// list stays backwards-compatible with older API versions.
//
// Lives inside profile-section.tsx (not a separate file) so the
// shared hooks (useTimeUntil, timeAgo) and styling primitives stay
// co-located — moving it out would require a public types module
// that nothing else in the tree needs yet.
function TokenListItem({
  token,
  onRevoke,
}: {
  token: CLIToken
  onRevoke: () => void
}) {
  const expiresIn = useTimeUntil(token.expires_at)
  const tier = token.tier ?? "STANDARD"
  const scopes = token.scopes ?? []

  // ≤24h = visual warning; ≤0 = already expired (validator will
  // refuse on next use but the row may not have been auto-revoked
  // yet, so the UI still flags it).
  const expiresDestructive = token.expires_at
    ? new Date(token.expires_at).getTime() - Date.now() <= 24 * 60 * 60 * 1000
    : false

  return (
    <div className="px-4 py-2.5 border-b border-border/40 last:border-b-0 space-y-1.5">
      <div className="flex items-center gap-2">
        <span className="text-xs font-medium text-foreground truncate">{token.name}</span>
        <Badge
          variant="outline"
          className={cn("text-[9px] font-medium px-1.5 py-0 leading-none h-4", tierCls[tier])}
        >
          {tier === "ADMIN" && <Shield className="h-2.5 w-2.5 mr-0.5" />}
          {tier}
        </Badge>
        <div className="flex-1" />
        <span className="text-[10px] text-muted-foreground font-mono flex items-center gap-1 shrink-0">
          <Clock className="h-2.5 w-2.5" />{timeAgo(token.created_at)}
        </span>
        {token.last_used_at && (
          <span className="text-[10px] text-muted-foreground font-mono hidden sm:inline shrink-0">
            used {timeAgo(token.last_used_at)}
          </span>
        )}
        <span className="h-1.5 w-1.5 rounded-full bg-emerald-400 shrink-0" />
        <TooltipProvider delayDuration={0}>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button variant="ghost" size="icon" className="h-6 w-6 text-muted-foreground hover:text-destructive hover:bg-destructive/10" onClick={onRevoke} aria-label="Revoke token">
                <Trash2 className="h-3 w-3" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="left" className="text-[11px]">Revoke</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </div>

      {/* Scope pills + expiry — second line, only when there's
          actually something to show. Empty scopes + no expiry =
          token inherits full role and lives forever; no need to
          stamp empty pills there. */}
      {(scopes.length > 0 || token.expires_at) && (
        <div className="flex items-center gap-1.5 flex-wrap pl-0.5">
          {scopes.map((s) => (
            <span
              key={s}
              className="text-[9px] font-mono bg-muted text-muted-foreground border border-border/60 rounded-sm px-1 py-0 leading-tight"
            >
              {s}
            </span>
          ))}
          {token.expires_at && expiresIn && (
            <span
              className={cn(
                "text-[9px] font-mono flex items-center gap-0.5",
                expiresDestructive ? "text-destructive" : "text-muted-foreground"
              )}
            >
              <Clock className="h-2 w-2" />
              expires {expiresIn === "Expired" ? "now" : `in ${expiresIn}`}
            </span>
          )}
        </div>
      )}
    </div>
  )
}
