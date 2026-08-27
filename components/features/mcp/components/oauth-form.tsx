"use client"

import { useCallback, useState, useEffect, useRef } from "react"
import {
  Cloud,
  CreditCard,
  ExternalLink,
  FileText,
  GitBranch,
  GitMerge,
  Globe,
  LayoutGrid,
  MessageSquare,
  Route,
  Wrench,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import { toast } from "sonner"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"
import type { AccentName } from "@/lib/concept-accents"
import {
  CREATE_SURFACE_INPUT,
  CreateSurfaceTile,
  type SurfaceIcon as SurfaceIconComponent,
} from "@/components/layout/create-surface"
import type { Credential, OAuthProvider } from "../types"
import { deriveCredentialName } from "../lib/credential-helpers"

// ---------------------------------------------------------------------------
// Provider shortcuts
//
// Rendered as pills inline (the MCP credential picker, where this is one field
// among many) and as tiles in a CreateSurface (Credentials → Connect via
// OAuth, where picking the provider IS the surface). The glyph and accent are
// only read by the tile layout; the pills have never carried either.
// ---------------------------------------------------------------------------

interface OAuthShortcut {
  key: string
  label: string
  icon: SurfaceIconComponent
  accent: AccentName
}

const OAUTH_PROVIDER_SHORTCUTS: OAuthShortcut[] = [
  { key: "google", label: "Google", icon: Globe, accent: "blue" },
  { key: "github", label: "GitHub", icon: GitBranch, accent: "slate" },
  { key: "slack", label: "Slack", icon: MessageSquare, accent: "red" },
  { key: "microsoft", label: "Microsoft", icon: LayoutGrid, accent: "sky" },
  { key: "linear", label: "Linear", icon: Route, accent: "purple" },
  { key: "gitlab", label: "GitLab", icon: GitMerge, accent: "amber" },
  { key: "notion", label: "Notion", icon: FileText, accent: "slate" },
  { key: "stripe", label: "Stripe", icon: CreditCard, accent: "purple" },
  { key: "cloudflare", label: "Cloudflare", icon: Cloud, accent: "gold" },
]

/**
 * The scope list as a tile subtitle.
 *
 * Google states its scopes as full URLs — three of them run to 130 characters
 * and wrap a 480px tile to three lines, which buries the provider name they
 * are meant to annotate. The trailing segment is the part that carries the
 * meaning (`.../auth/drive` → `drive`), and the full string is still what gets
 * sent: this shortens the label, not the request.
 */
function formatScopes(raw: string): string {
  return raw
    .split(/[\s,]+/)
    .filter(Boolean)
    .map((s) => (s.startsWith("http") ? s.replace(/\/+$/, "").split("/").pop() || s : s))
    .join(" · ")
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

/** What the form's primary action is, right now. */
export interface OAuthFormAction {
  authorize: () => void
  disabled: boolean
  busy: boolean
  label: string
}

export interface OAuthFormProps {
  envKey: string
  workspaceId: string
  onAddCredential: (cred: Credential) => void
  onSelectCredential: (credName: string) => void
  onCancel: () => void
  /**
   * Hand the primary action to the caller instead of drawing it.
   *
   * The MCP credential picker renders this form inline, where an action row
   * at the bottom of the form is right. `ConnectOAuthDialog` renders it in a
   * CreateSurface, where the primary belongs in the footer — outside the
   * scrollport, next to Cancel, reachable by ⌘↵ — and a second Authorize
   * button halfway up the body is the thing the shell exists to stop.
   *
   * Supplying this suppresses the in-form row. Called on mount and whenever
   * the action's state changes; the callback itself must be stable.
   */
  onActionChange?: (action: OAuthFormAction) => void
  /**
   * How the provider shortcuts are drawn.
   *
   * `inline` (default) is the pill row the MCP credential picker has always
   * shown, where this form is one control inside a larger config panel.
   * `surface` is the tile list /design specifies for Credentials → Connect via
   * OAuth: glyph, name, and the scopes the provider will be asked for, which
   * is the fact a person needs before handing over access and the one a pill
   * has no room to carry.
   */
  variant?: "inline" | "surface"
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function OAuthForm({
  envKey,
  workspaceId,
  onAddCredential,
  onSelectCredential,
  onCancel,
  onActionChange,
  variant = "inline",
}: OAuthFormProps) {
  const [providers, setProviders] = useState<Record<string, OAuthProvider>>({})
  const [providersFetched, setProvidersFetched] = useState(false)
  const [clientId, setClientId] = useState("")
  const [clientSecret, setClientSecret] = useState("")
  const [authUrl, setAuthUrl] = useState("")
  const [tokenUrl, setTokenUrl] = useState("")
  const [scopes, setScopes] = useState("")
  const [selectedProvider, setSelectedProvider] = useState<string | null>(null)
  const [authorizing, setAuthorizing] = useState(false)
  const [polling, setPolling] = useState(false)
  const [showCodeInput, setShowCodeInput] = useState(false)
  const [manualCode, setManualCode] = useState("")
  const [pendingCredId, setPendingCredId] = useState<string | null>(null)
  const [pendingCredName, setPendingCredName] = useState("")
  const [pendingRedirectUri, setPendingRedirectUri] = useState("")
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  // Fetch available providers on mount
  useEffect(() => {
    let cancelled = false

    async function fetchProviders() {
      try {
        const res = await apiFetch(`/api/v1/oauth/providers?workspace_id=${workspaceId}`)
        if (res.ok) {
          const data = await res.json()
          if (!cancelled) setProviders(data)
        }
      } catch {
        // Non-critical — user can still use Custom
      } finally {
        if (!cancelled) setProvidersFetched(true)
      }
    }

    fetchProviders()
    return () => {
      cancelled = true
    }
  }, [workspaceId])

  // Always clear pollRef on unmount (handleAuthorize also sets pollRef)
  useEffect(() => {
    return () => {
      if (pollRef.current) clearInterval(pollRef.current)
    }
  }, [])

  function handleProviderSelect(key: string) {
    setSelectedProvider(key)
    const provider = providers[key]
    if (provider) {
      setAuthUrl(provider.auth_url)
      setTokenUrl(provider.token_url)
      setScopes(provider.default_scopes)
    }
  }

  function handleCustom() {
    setSelectedProvider("custom")
    setAuthUrl("")
    setTokenUrl("")
    setScopes("")
  }

  // ── The primary, published rather than drawn ───────────────────────────
  //
  // `handleAuthorize` is redeclared every render, so it cannot go in the
  // effect's deps without looping. The ref holds the current one and the
  // callback stays stable; the effect then depends only on the four things a
  // caller's footer actually renders from.
  const canAuthorize =
    !authorizing &&
    clientId.trim() !== "" &&
    clientSecret.trim() !== "" &&
    !(selectedProvider === "custom" && (!authUrl.trim() || !tokenUrl.trim()))
  const primaryLabel = polling ? "Waiting for authorization..." : "Authorize"

  const authorizeRef = useRef<() => void>(() => {})
  const authorize = useCallback(() => authorizeRef.current(), [])

  /**
   * Publish the current handler AFTER commit, not during render.
   *
   * This was a bare `authorizeRef.current = handleAuthorize` down in the
   * render body. React may replay or discard a render — StrictMode does it on
   * every one — so a discarded render could leave the committed footer's
   * stable `authorize` pointing at a closure over state the UI never showed.
   * The footer's primary creates a credential, so "runs against state nobody
   * saw" is not a theoretical cost.
   *
   * No dependency array: the point is that it tracks every commit.
   * `handleAuthorize` is a function declaration, so it is hoisted and this
   * effect can be declared above it — deliberately, because effects fire in
   * declaration order and the ref must be current before the effect below
   * hands `authorize` to the caller.
   */
  useEffect(() => {
    authorizeRef.current = handleAuthorize
  })

  useEffect(() => {
    onActionChange?.({
      authorize,
      disabled: !canAuthorize,
      busy: authorizing || polling,
      label: primaryLabel,
    })
  }, [onActionChange, authorize, canAuthorize, authorizing, polling, primaryLabel])

  async function handleAuthorize() {
    if (!clientId.trim() || !clientSecret.trim() || !authUrl.trim() || !tokenUrl.trim()) {
      toast.error("Client ID, Client Secret, Auth URL, and Token URL are required")
      return
    }

    setAuthorizing(true)

    try {
      // Step 1: Create OAUTH2 credential (timestamp suffix avoids name collisions)
      const baseName = envKey
        ? deriveCredentialName(envKey) + "-oauth"
        : (selectedProvider ?? "custom") + "-oauth"
      const credName = baseName + "-" + Date.now().toString(36)

      const createRes = await apiFetch(`/api/v1/credentials?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: credName,
          type: "OAUTH2",
          value: "",
          scope: "WORKSPACE",
          oauth_client_id: clientId.trim(),
          oauth_client_secret: clientSecret.trim(),
          oauth_auth_url: authUrl.trim(),
          oauth_token_url: tokenUrl.trim(),
          oauth_scopes: scopes.trim(),
        }),
      })

      if (!createRes.ok) {
        const data = await createRes.json().catch(() => ({ error: "Failed to create OAuth credential" }))
        toast.error(typeof data.error === "string" ? data.error : "Failed to create OAuth credential")
        setAuthorizing(false)
        return
      }

      const created: Credential = await createRes.json()
      onAddCredential(created)
      setPendingCredId(created.id)
      setPendingCredName(credName)

      // Step 2: Pick the right OAuth mechanism based on deployment topology
      const hostname = window.location.hostname
      const hasPublicDomain = hostname !== "localhost"
        && hostname !== "127.0.0.1"
        && !/^(10\.|172\.(1[6-9]|2\d|3[01])\.|192\.168\.)/.test(hostname)
      const isLocalhost = hostname === "localhost" || hostname === "127.0.0.1"

      let oauthRedirectUrl: string

      if (isLocalhost) {
        // LOCALHOST: loopback server (same as gh auth login, gcloud auth login)
        const res = await apiFetch(`/api/v1/oauth/loopback?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ credential_id: created.id }),
        })
        if (!res.ok) {
          const data = await res.json().catch(() => ({ error: "Failed to start OAuth" }))
          toast.error(typeof data.error === "string" ? data.error : "Failed to start OAuth flow")
          setAuthorizing(false)
          return
        }
        const result = await res.json()
        oauthRedirectUrl = result.auth_url
        try {
          const authParams = new URL(oauthRedirectUrl)
          setPendingRedirectUri(authParams.searchParams.get("redirect_uri") ?? "")
        } catch { /* ignore */ }
      } else if (hasPublicDomain) {
        // PUBLIC DOMAIN: standard redirect callback
        const redirectUri = `${window.location.origin}/api/v1/oauth/callback`
        setPendingRedirectUri(redirectUri)
        const res = await apiFetch(`/api/v1/oauth/initiate?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ credential_id: created.id, redirect_uri: redirectUri }),
        })
        if (!res.ok) {
          const data = await res.json().catch(() => ({ error: "Failed to initiate OAuth" }))
          toast.error(typeof data.error === "string" ? data.error : "Failed to initiate OAuth flow")
          setAuthorizing(false)
          return
        }
        const result = await res.json()
        oauthRedirectUrl = result.auth_url
      } else {
        // PRIVATE IP: loopback + manual paste (callback won't reach browser)
        const res = await apiFetch(`/api/v1/oauth/loopback?workspace_id=${workspaceId}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ credential_id: created.id }),
        })
        if (!res.ok) {
          const data = await res.json().catch(() => ({ error: "Failed to start OAuth" }))
          toast.error(typeof data.error === "string" ? data.error : "Failed to start OAuth flow")
          setAuthorizing(false)
          return
        }
        const result = await res.json()
        oauthRedirectUrl = result.auth_url
        try {
          const authParams = new URL(oauthRedirectUrl)
          setPendingRedirectUri(authParams.searchParams.get("redirect_uri") ?? "")
        } catch { /* ignore */ }
        setShowCodeInput(true)
        toast.info(
          "After authorizing, copy the URL from your browser and paste it below.",
          { duration: 8000 },
        )
      }

      // Step 3: Open auth URL in popup and start polling
      const popup = window.open(oauthRedirectUrl, "oauth_popup", "width=600,height=700,popup=yes")
      if (!popup) {
        toast.error("Popup blocked — please allow popups for this site and try again")
        setAuthorizing(false)
        return
      }
      setPolling(true)

      if (!showCodeInput) {
        setTimeout(() => setShowCodeInput(true), 5000)
      }

      let elapsed = 0
      const POLL_INTERVAL = 2000
      const MAX_WAIT = 120000

      pollRef.current = setInterval(async () => {
        elapsed += POLL_INTERVAL
        if (elapsed > MAX_WAIT) {
          if (pollRef.current) clearInterval(pollRef.current)
          pollRef.current = null
          setPolling(false)
          setAuthorizing(false)
          toast.error("OAuth authorization timed out")
          return
        }

        try {
          const statusRes = await apiFetch(
            `/api/v1/credentials/${created.id}?workspace_id=${workspaceId}`,
          )
          if (statusRes.ok) {
            const statusData = await statusRes.json()
            if (statusData.status === "ACTIVE") {
              if (pollRef.current) clearInterval(pollRef.current)
              pollRef.current = null
              setPolling(false)
              setAuthorizing(false)
              setShowCodeInput(false)
              if (popup && !popup.closed) popup.close()
              toast.success("OAuth authorization successful")
              onSelectCredential(credName)
            }
          }
        } catch {
          // Continue polling
        }
      }, POLL_INTERVAL)
    } catch {
      toast.error("Network error during OAuth setup")
      setAuthorizing(false)
    }
  }

  async function handleManualCodeExchange() {
    if (!manualCode.trim() || !pendingCredId) return
    setAuthorizing(true)

    // Extract code from URL or raw code
    let code = manualCode.trim()
    try {
      const url = new URL(code)
      code = url.searchParams.get("code") ?? code
    } catch {
      // Not a URL, use as-is (raw code)
    }

    try {
      const res = await apiFetch(`/api/v1/oauth/exchange?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          credential_id: pendingCredId,
          code,
          redirect_uri: pendingRedirectUri,
        }),
      })

      if (res.ok) {
        if (pollRef.current) clearInterval(pollRef.current)
        pollRef.current = null
        setPolling(false)
        setAuthorizing(false)
        setShowCodeInput(false)
        toast.success("OAuth authorization successful")
        onSelectCredential(pendingCredName)
      } else {
        const data = await res.json().catch(() => ({ error: "Code exchange failed" }))
        toast.error(typeof data.error === "string" ? data.error : "Failed to exchange code")
        setAuthorizing(false)
      }
    } catch {
      toast.error("Network error during code exchange")
      setAuthorizing(false)
    }
  }

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className={cn("space-y-3", variant === "surface" ? "p-0" : "p-3")}>
      {/* In a CreateSurface the header two rows up already says "Connect via
          OAuth"; repeating it is the duplicated-title shape the shell exists
          to remove. Inline, this label is the only thing naming the form. */}
      {variant === "inline" && <div className="text-xs font-medium">Connect with OAuth</div>}

      {variant === "surface" ? (
        <div className="space-y-2">
          {OAUTH_PROVIDER_SHORTCUTS.map((p) => {
            const provider = providers[p.key]
            const unavailable = !providersFetched || (providersFetched && !provider)
            return (
              <CreateSurfaceTile
                key={p.key}
                icon={p.icon}
                accent={p.accent}
                title={p.label}
                // Before the providers land there is nothing truthful to say
                // about scopes, so the tile says nothing rather than guessing.
                description={
                  provider?.default_scopes
                    ? formatScopes(provider.default_scopes)
                    : providersFetched
                      ? "Not configured on this server"
                      : undefined
                }
                selected={selectedProvider === p.key}
                onClick={() => handleProviderSelect(p.key)}
                disabled={unavailable || authorizing}
              />
            )
          })}
          <CreateSurfaceTile
            icon={Wrench}
            accent="slate"
            title="Custom"
            description="Any OAuth2 endpoint — you supply the authorise and token URLs."
            selected={selectedProvider === "custom"}
            onClick={handleCustom}
            disabled={authorizing}
          />
        </div>
      ) : (
        /* Provider shortcuts. `max-sm:h-12`: this form is also used
           un-migrated inline in the MCP credential picker, so unlike the rest
           of the create surfaces these pills never picked up a phone size —
           measured at 24.15px tall (`h-6`) on an iPhone 13, well short of the
           44px floor either place this renders. */
        <div className="flex items-center gap-1.5 flex-wrap">
          {OAUTH_PROVIDER_SHORTCUTS.map((p) => (
            <Button
              key={p.key}
              type="button"
              variant={selectedProvider === p.key ? "default" : "outline"}
              size="sm"
              className="h-6 text-[10px] px-2 max-sm:h-12 max-sm:px-3 max-sm:text-sm"
              onClick={() => handleProviderSelect(p.key)}
              disabled={!providersFetched || authorizing || (providersFetched && !providers[p.key])}
            >
              {p.label}
            </Button>
          ))}
          <Button
            type="button"
            variant={selectedProvider === "custom" ? "default" : "outline"}
            size="sm"
            className="h-6 text-[10px] px-2 max-sm:h-12 max-sm:px-3 max-sm:text-sm"
            onClick={handleCustom}
            disabled={authorizing}
          >
            Custom
          </Button>
        </div>
      )}

      {selectedProvider && (
        <div className="space-y-2">
          <div className="space-y-1">
            <Label htmlFor="oauth-client-id" className="text-xs text-muted-foreground">Client ID</Label>
            <Input
              id="oauth-client-id"
              value={clientId}
              onChange={(e) => setClientId(e.target.value)}
              placeholder="your-client-id"
              className={CREATE_SURFACE_INPUT}
              disabled={authorizing}
            />
          </div>
          <div className="space-y-1">
            <Label htmlFor="oauth-client-secret" className="text-xs text-muted-foreground">Client Secret</Label>
            <Input
              id="oauth-client-secret"
              type="password"
              value={clientSecret}
              onChange={(e) => setClientSecret(e.target.value)}
              placeholder="your-client-secret"
              className={cn(CREATE_SURFACE_INPUT, "font-mono")}
              disabled={authorizing}
            />
          </div>
          {selectedProvider === "custom" && (
            <>
              <div className="space-y-1">
                <Label htmlFor="oauth-auth-url" className="text-xs text-muted-foreground">Auth URL</Label>
                <Input
                  id="oauth-auth-url"
                  value={authUrl}
                  onChange={(e) => setAuthUrl(e.target.value)}
                  placeholder="https://accounts.google.com/o/oauth2/v2/auth"
                  className={cn(CREATE_SURFACE_INPUT, "font-mono")}
                  disabled={authorizing}
                />
              </div>
              <div className="space-y-1">
                <Label htmlFor="oauth-token-url" className="text-xs text-muted-foreground">Token URL</Label>
                <Input
                  id="oauth-token-url"
                  value={tokenUrl}
                  onChange={(e) => setTokenUrl(e.target.value)}
                  placeholder="https://oauth2.googleapis.com/token"
                  className={cn(CREATE_SURFACE_INPUT, "font-mono")}
                  disabled={authorizing}
                />
              </div>
            </>
          )}
          <div className="space-y-1">
            <Label htmlFor="oauth-scopes" className="text-xs text-muted-foreground">Scopes</Label>
            <Input
              id="oauth-scopes"
              value={scopes}
              onChange={(e) => setScopes(e.target.value)}
              placeholder="space-separated scopes"
              className={cn(CREATE_SURFACE_INPUT, "font-mono")}
              disabled={authorizing}
            />
            {scopes && selectedProvider !== "custom" && (
              <p className="text-[10px] text-muted-foreground">
                Pre-filled for {OAUTH_PROVIDER_SHORTCUTS.find(p => p.key === selectedProvider)?.label ?? selectedProvider}
              </p>
            )}
          </div>

          {!onActionChange && (
            <div className="flex items-center gap-2 pt-1">
              <Button
                type="button"
                size="sm"
                className="h-7 text-xs gap-1.5 flex-1 max-sm:h-12 max-sm:text-sm"
                disabled={!canAuthorize}
                onClick={handleAuthorize}
              >
                {polling ? (
                  <Spinner className="h-3 w-3" />
                ) : (
                  <ExternalLink className="h-3 w-3" />
                )}
                {primaryLabel}
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 text-xs max-sm:h-12 max-sm:text-sm"
                onClick={onCancel}
                disabled={authorizing}
              >
                Cancel
              </Button>
            </div>
          )}

          {/* Manual code fallback */}
          {(showCodeInput || polling) && (
            <div className="border-t pt-3 mt-3 space-y-2">
              <p className="text-xs text-muted-foreground">
                If the redirect didn&apos;t complete automatically, paste the URL or authorization code from your browser:
              </p>
              <div className="flex items-center gap-2">
                <Input
                  id="oauth-manual-code"
                  aria-label="Manual authorization code or redirect URL"
                  value={manualCode}
                  onChange={(e) => setManualCode(e.target.value)}
                  placeholder="Paste redirect URL or authorization code"
                  className={cn(CREATE_SURFACE_INPUT, "font-mono flex-1")}
                />
                <Button
                  type="button"
                  size="sm"
                  className="h-7 text-xs max-sm:h-12 max-sm:text-sm"
                  disabled={!manualCode.trim() || !pendingCredId}
                  onClick={handleManualCodeExchange}
                >
                  Submit
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      {/* The tiles say what they are; the pills do not, so only the pill
          layout needs telling the reader what the row above is for. */}
      {!selectedProvider && variant === "inline" && (
        <p className="text-xs text-muted-foreground">
          Select a provider above or choose Custom for any OAuth2 endpoint.
        </p>
      )}
    </div>
  )
}
