"use client"

import { useCallback, useEffect, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { toast } from "@/lib/toast"
import { Copy, Check, Key, Plus, Terminal, Eye, EyeOff, AlertTriangle, Shield, ChevronRight } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { Spinner } from "@/components/ui/spinner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { StatusPill } from "@/components/ui/status-pill"
import { Checkbox } from "@/components/ui/checkbox"
import { cn } from "@/lib/utils"
import { SettingsCard, SettingsCardActions, SettingsEmpty, SettingsRow, SettingsSegmented, controlHeight } from "../shared"
import { DeviceSessions } from "./device-sessions"

// Settings › Account › Sessions & tokens: who can act as you right now.
//
// Split out of profile-section.tsx so the user menu can link straight to it
// (/settings?tab=sessions) and Profile is about the profile again. The card,
// its state and its copy moved unchanged; the restricted account-security
// view (settings-layout.tsx) renders this section under ProfileSection, so a
// restricted session still gets password, sessions and token revoke on one
// page exactly as before.

// ── Helpers ─────────────────────────────────────────────────────────
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
// One checkbox per scope (Harbor 2 draws them flat). Every resource has a
// single gated action today, so a per-resource "*" said the same thing as
// the one action beside it and is not offered.
//
// Only actions that actually gate a route are offered — reads are not
// scope-gated yet and there is no route-mapped agents:run, so those would
// mint tokens that grant/gate nothing. This mirrors the backend allowlist
// (and the reverse invariant TestEveryMintableScopeMapsToARoute). They come
// back here when a route requires them.
const SCOPES = [
  "agents:write",
  "crews:write",
  "credentials:write",
  "skills:write",
  "webhooks:write",
  "workspace:admin",
] as const

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

interface SessionsSectionProps {
  sessionExpires?: string | null
  onSignOut?: () => void
  /** A restricted session (#2861): own token creation is allowed only
   *  from a verified interactive session; elevated tier/scopes are not offered. */
  restricted?: boolean
}

// ── Component ───────────────────────────────────────────────────────

export function SessionsSection({ sessionExpires, onSignOut, restricted = false }: SessionsSectionProps) {
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
  const [showRevokedTokens, setShowRevokedTokens] = useState(false)
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
      const res = await apiFetch("/api/v1/auth/cli-tokens")
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
      if (!restricted && tokenTier === "ADMIN") body.tier = "ADMIN"
      if (!restricted && tokenScopes.size > 0) body.scopes = Array.from(tokenScopes)
      if (tokenExpirySeconds > 0) body.expires_in_seconds = tokenExpirySeconds

      const res = await apiFetch("/api/v1/auth/cli-token", {
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

  // ConfirmDialog waits for this: a refusal throws, so the dialog stays open
  // where the person can retry or cancel, and the reason is the error toast.
  async function handleRevoke() {
    if (!revokeTarget) return
    const name = revokeTarget.name
    let res: Response
    try {
      res = await apiFetch(`/api/v1/auth/cli-tokens/${revokeTarget.id}`, { method: "DELETE" })
    } catch (e) {
      toast.error(`Couldn't revoke ${name}`, { description: e instanceof Error ? e.message : undefined })
      throw e
    }
    // apiFetch RESOLVES on a 4xx/5xx — it does not throw. A version that
    // ignored res.ok closed the dialog and refreshed the list exactly like a
    // successful revoke; the token stayed and nothing said why.
    if (!res.ok) {
      const body = await res.json().catch(() => null)
      const detail = typeof body?.error === "string" ? body.error : `HTTP ${res.status}`
      toast.error(`Couldn't revoke ${name}`, { description: detail })
      throw new Error(detail)
    }
    toast.success(`Revoked ${name}`)
    fetchTokens()
  }

  function handleCopyToken(text: string) {
    navigator.clipboard.writeText(text)
    setTokenCopied(true)
    setTimeout(() => setTokenCopied(false), 2000)
  }

  function cancelCreate() {
    setShowCreateForm(false)
    setTokenName("")
    setTokenTier("STANDARD")
    setTokenExpirySeconds(0)
    setTokenScopes(new Set())
    setCreateError(null)
  }

  const activeTokens = tokens.filter((t) => !t.revoked_at)
  const revokedTokens = tokens.filter((t) => t.revoked_at)
  // ADMIN tokens never live forever and at most seven days (cli_token.go).
  const expiryOptions = EXPIRY_PRESETS
    .filter((p) => restricted || tokenTier !== "ADMIN" || (p.value !== 0 && p.value <= 7 * 24 * 60 * 60))
    .map((p) => ({ value: p.value, label: p.label }))

  return (
    <div className="space-y-4">
      {/* ── Browsers & devices ── (its own card, device-sessions.tsx) */}
      <DeviceSessions onSignOut={onSignOut} currentExpiresIn={expiresIn} />

      {/* ── CLI tokens ──
          A session is discovered (unnamed, carries UA + IP, expires on its
          own); a token is minted (you named it, chose a tier and scopes, saw
          the secret once). Two cards, one per kind, under one section head
          that asks the one question: who can act as me right now? */}
      <SettingsCard
        icon={Terminal}
        title="CLI tokens"
        description="For the crewship CLI, scripts and CI"
        actions={!showCreateForm ? (
          <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setShowCreateForm(true)}>
            <Plus className="size-3" />New token
          </Button>
        ) : undefined}
      >
        {/* ── New token reveal ── shown once, right where it was made. */}
        <AnimatePresence>
          {newToken && (
            <motion.div initial={{ opacity: 0, y: -6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -6 }} transition={{ duration: 0.15 }}>
              <div className="mx-4 my-3 rounded-card border border-primary/30 bg-primary/[0.04] p-3" data-slot="new-token">
                <div className="mb-2.5 flex items-start gap-2.5">
                  <Key className="mt-0.5 h-3.5 w-3.5 shrink-0 text-primary-hover" aria-hidden />
                  <div className="min-w-0 flex-1">
                    <h4 className="flex items-center gap-1.5 text-control font-semibold text-foreground">
                      Token created
                      <TokenTier tier={newToken.tier} />
                    </h4>
                    <p className="text-label text-muted-foreground">
                      {newToken.tier === "ADMIN"
                        ? "Admin-tier token — short-lived (≤7d), per-use audited. Copy now — it won’t be shown again."
                        : "Copy now — it won’t be shown again."}
                    </p>
                  </div>
                  <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={() => setNewToken(null)}>Dismiss</Button>
                </div>
                <div className="flex items-center gap-2 rounded-md border border-border bg-surface-subtle p-2">
                  <Terminal className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden />
                  <code className="flex-1 select-all break-all font-mono text-label leading-relaxed text-foreground">
                    {tokenVisible ? newToken.token : newToken.token.slice(0, 18) + "•".repeat(24)}
                  </code>
                  <Button variant="ghost" size="icon-sm" className="text-muted-foreground hover:text-foreground" onClick={() => setTokenVisible(!tokenVisible)} aria-label={tokenVisible ? "Hide token" : "Show token"}>
                    {tokenVisible ? <EyeOff className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                  </Button>
                  <Button variant="ghost" size="sm" className={cn("gap-1", tokenCopied ? "text-foreground" : "text-muted-foreground")} onClick={() => handleCopyToken(newToken.token)}>
                    {tokenCopied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
                    {tokenCopied ? "Copied" : "Copy"}
                  </Button>
                </div>
                <p className="mt-2 font-mono text-label text-muted-foreground">
                  $ crewship auth login --token &lt;token&gt;
                </p>
              </div>
            </motion.div>
          )}
        </AnimatePresence>

        {/* Create form — inline (the mockup and the product), so the reveal
            above sits where the token was made. */}
        <AnimatePresence>
          {showCreateForm && (
            <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} className="overflow-hidden">
              <form
                className="mx-4 my-3 grid gap-3 rounded-card border border-border bg-surface-subtle p-3"
                onSubmit={(e) => { e.preventDefault(); void handleCreateToken() }}
                aria-label="New CLI token"
              >
                <div className={restricted ? "grid gap-3" : "grid gap-3 sm:grid-cols-2"}>
                  <div className="space-y-1">
                    <Label htmlFor="token-name" className="text-label">Token name</Label>
                    <Input
                      id="token-name"
                      value={tokenName} onChange={(e) => setTokenName(e.target.value)}
                      placeholder="e.g. MacBook Pro, ci-deploy-bot"
                      className={controlHeight}
                      autoFocus
                    />
                  </div>
                  {!restricted && (
                    <div className="space-y-1">
                      <span className="block text-label font-medium">Tier</span>
                      <SettingsSegmented
                        label="Tier"
                        value={tokenTier}
                        onChange={setTokenTier}
                        options={[{ value: "STANDARD", label: "Standard" }, { value: "ADMIN", label: "Admin (OWNER only)" }]}
                      />
                    </div>
                  )}
                </div>

                <div className="space-y-1">
                  <span className="block text-label font-medium">Expires</span>
                  <SettingsSegmented label="Expires" value={tokenExpirySeconds} onChange={setTokenExpirySeconds} options={expiryOptions} />
                  {!restricted && <span className="block text-label text-muted-foreground">Admin tokens last 7 days at most</span>}
                </div>

                {/* Scopes — one checkbox per scope the backend can gate.
                    Empty selection = unrestricted (full role inherited). */}
                {!restricted && (
                  <fieldset className="space-y-1">
                    <legend className="flex w-full items-center justify-between text-label font-medium">
                      <span>Scopes {tokenScopes.size > 0 && <span className="font-normal text-muted-foreground">({tokenScopes.size} selected)</span>}</span>
                      {tokenScopes.size > 0 && (
                        <button type="button" onClick={() => setTokenScopes(new Set())} className="text-label font-normal text-muted-foreground hover:text-foreground">
                          Clear
                        </button>
                      )}
                    </legend>
                    <div className="grid grid-cols-1 gap-x-3 gap-y-1 sm:grid-cols-3">
                      {SCOPES.map((scope) => (
                        <label key={scope} className="kit-tap flex cursor-pointer items-center gap-2 text-label">
                          <Checkbox
                            checked={tokenScopes.has(scope)}
                            onCheckedChange={(checked) => {
                              const next = new Set(tokenScopes)
                              if (checked) next.add(scope)
                              else next.delete(scope)
                              setTokenScopes(next)
                            }}
                            aria-label={scope}
                          />
                          <span className="font-mono">{scope}</span>
                        </label>
                      ))}
                    </div>
                    <span className="block text-label text-muted-foreground">Leave empty for unrestricted token (full role permissions).</span>
                  </fieldset>
                )}
                {restricted && <p className="text-xs text-muted-foreground">This token uses your current Guest grants. It cannot open resources your account cannot access.</p>}

                {/* Inline error from backend (unknown scope, OWNER-only, etc.) */}
                {createError && (
                  <div role="alert" className="flex items-start gap-1.5 rounded-md border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-label text-destructive">
                    <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" />
                    <span>{createError}</span>
                  </div>
                )}

                <div className="flex items-center justify-end gap-2">
                  <Button type="button" variant="ghost" size="sm" className="text-muted-foreground" onClick={cancelCreate}>Cancel</Button>
                  <Button type="submit" size="sm" className="gap-1" disabled={creating || !tokenName.trim()}>
                    {creating && <Spinner className="h-3 w-3" />}Create token
                  </Button>
                </div>
              </form>
            </motion.div>
          )}
        </AnimatePresence>

        {/* Token list */}
        {tokensLoading ? (
          <div className="px-4 py-6 text-center" role="status" aria-label="Loading CLI tokens">
            <Spinner className="h-3.5 w-3.5 text-muted-foreground mx-auto" />
          </div>
        ) : tokens.length === 0 && !showCreateForm ? (
          <SettingsEmpty>No CLI tokens. Create one for the crewship CLI, scripts or CI.</SettingsEmpty>
        ) : (
          <>
            {activeTokens.map((token) => <TokenListItem key={token.id} token={token} onRevoke={() => setRevokeTarget(token)} />)}

            {/* Revoked tokens are collapsed. The card says what can sign in as
                you right now — a revoked token cannot, and on a real account
                a run of struck-through rows pushed the live ones out of view.
                Still reachable, because "did my revoke actually stick?"
                should not need the audit log. */}
            {revokedTokens.length > 0 && (
              <>
                {showRevokedTokens && revokedTokens.map((token) => (
                  <div key={token.id} className="flex items-center justify-between border-b border-border px-4 py-2 opacity-60">
                    <span className="font-mono text-control text-muted-foreground line-through">{token.name}</span>
                    <span className="text-label text-muted-foreground">revoked</span>
                  </div>
                ))}
                <SettingsCardActions className="pt-2.5">
                  <Button
                    variant="link"
                    size="sm"
                    className="gap-1 px-0 text-primary-hover"
                    onClick={() => setShowRevokedTokens((v) => !v)}
                    aria-expanded={showRevokedTokens}
                  >
                    <ChevronRight className={cn("size-3 transition-transform", showRevokedTokens && "rotate-90")} />
                    {revokedTokens.length} revoked
                  </Button>
                </SettingsCardActions>
              </>
            )}
          </>
        )}
      </SettingsCard>

      <ConfirmDialog
        open={!!revokeTarget}
        onOpenChange={(open) => { if (!open) setRevokeTarget(null) }}
        title={`Revoke ${revokeTarget?.name ?? "token"}?`}
        description="Terminals, scripts and CI using this token are disconnected at once."
        consequences={[
          { tone: "lost", text: "The token stops working everywhere it is used" },
          { tone: "kept", text: "Your other tokens and browser sessions" },
        ]}
        confirmLabel="Revoke"
        destructive
        onConfirm={handleRevoke}
      />
    </div>
  )
}

/** The token's tier, as the one status pill: Admin in the warning tone. */
function TokenTier({ tier }: { tier: string }) {
  return tier === "ADMIN"
    ? <StatusPill tone="warn" label="Admin" />
    : <StatusPill tone="muted" label="Standard" />
}

// ── TokenListItem ─────────────────────────────────────────────────────
//
// One active CLI token as a settings row: the name in mono with its tier,
// and on the right what it can do, when it expires and when it was last
// used — then Revoke. Pre-M2 tokens (no tier in the response) render
// exactly like STANDARD/unrestricted.
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

  // ≤24h = visual warning; ≤0 = already expired (validator will refuse on
  // next use but the row may not have been auto-revoked yet).
  const expiresSoon = token.expires_at
    ? new Date(token.expires_at).getTime() - Date.now() <= 24 * 60 * 60 * 1000
    : false

  return (
    <SettingsRow
      lead={tier === "ADMIN" ? Shield : Key}
      label={
        <span className="inline-flex flex-wrap items-center gap-2">
          <span className="truncate font-mono">{token.name}</span>
          <TokenTier tier={tier} />
        </span>
      }
      description={`Created ${timeAgo(token.created_at)}`}
    >
      <span className="text-label text-muted-foreground">
        {scopes.length > 0 ? scopes.join(" · ") : "unrestricted"}
        {" · "}
        <span className={cn(expiresSoon && "text-destructive")}>
          {token.expires_at && expiresIn
            ? `expires ${expiresIn === "Expired" ? "now" : `in ${expiresIn}`}`
            : "expires never"}
        </span>
        {token.last_used_at && ` · used ${timeAgo(token.last_used_at)}`}
      </span>
      {/* Named after the token, not "Revoke token": a screen-reader user
          tabbing a list of five identical buttons has no other way to tell
          which one they are on. */}
      <Button
        variant="ghost"
        size="sm"
        className="text-destructive hover:text-destructive hover:bg-destructive/10"
        onClick={onRevoke}
        aria-label={`Revoke ${token.name}`}
      >
        Revoke
      </Button>
    </SettingsRow>
  )
}
