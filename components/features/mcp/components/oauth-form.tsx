"use client"

import { useState, useEffect, useRef } from "react"
import { Loader2, ExternalLink } from "lucide-react"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import { toast } from "sonner"
import type { Credential, OAuthProvider } from "../types"
import { deriveCredentialName } from "../lib/credential-helpers"

// ---------------------------------------------------------------------------
// Provider shortcuts shown as pill buttons
// ---------------------------------------------------------------------------

const OAUTH_PROVIDER_SHORTCUTS: { key: string; label: string }[] = [
  { key: "google", label: "Google" },
  { key: "github", label: "GitHub" },
  { key: "slack", label: "Slack" },
  { key: "microsoft", label: "Microsoft" },
  { key: "linear", label: "Linear" },
  { key: "gitlab", label: "GitLab" },
  { key: "notion", label: "Notion" },
  { key: "stripe", label: "Stripe" },
  { key: "cloudflare", label: "Cloudflare" },
]

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface OAuthFormProps {
  envKey: string
  workspaceId: string
  onAddCredential: (cred: Credential) => void
  onSelectCredential: (credName: string) => void
  onCancel: () => void
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
        const res = await fetch(`/api/v1/oauth/providers?workspace_id=${workspaceId}`)
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

      const createRes = await fetch(`/api/v1/credentials?workspace_id=${workspaceId}`, {
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
        const res = await fetch(`/api/v1/oauth/loopback?workspace_id=${workspaceId}`, {
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
        const res = await fetch(`/api/v1/oauth/initiate?workspace_id=${workspaceId}`, {
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
        const res = await fetch(`/api/v1/oauth/loopback?workspace_id=${workspaceId}`, {
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
          const statusRes = await fetch(
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
      const res = await fetch(`/api/v1/oauth/exchange?workspace_id=${workspaceId}`, {
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
    <div className="p-3 space-y-3">
      <div className="text-xs font-medium">Connect with OAuth</div>

      {/* Provider shortcuts */}
      <div className="flex items-center gap-1.5 flex-wrap">
        {OAUTH_PROVIDER_SHORTCUTS.map((p) => (
          <Button
            key={p.key}
            type="button"
            variant={selectedProvider === p.key ? "default" : "outline"}
            size="sm"
            className="h-6 text-[10px] px-2"
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
          className="h-6 text-[10px] px-2"
          onClick={handleCustom}
          disabled={authorizing}
        >
          Custom
        </Button>
      </div>

      {selectedProvider && (
        <div className="space-y-2">
          <div className="space-y-1">
            <Label htmlFor="oauth-client-id" className="text-xs text-muted-foreground">Client ID</Label>
            <Input
              id="oauth-client-id"
              value={clientId}
              onChange={(e) => setClientId(e.target.value)}
              placeholder="your-client-id"
              className="h-7 text-xs"
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
              className="h-7 text-xs font-mono"
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
                  className="h-7 text-xs font-mono"
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
                  className="h-7 text-xs font-mono"
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
              className="h-7 text-xs font-mono"
              disabled={authorizing}
            />
            {scopes && selectedProvider !== "custom" && (
              <p className="text-[10px] text-muted-foreground">
                Pre-filled for {OAUTH_PROVIDER_SHORTCUTS.find(p => p.key === selectedProvider)?.label ?? selectedProvider}
              </p>
            )}
          </div>

          <div className="flex items-center gap-2 pt-1">
            <Button
              type="button"
              size="sm"
              className="h-7 text-xs gap-1.5 flex-1"
              disabled={authorizing || !clientId.trim() || !clientSecret.trim() || (selectedProvider === "custom" && (!authUrl.trim() || !tokenUrl.trim()))}
              onClick={handleAuthorize}
            >
              {polling ? (
                <Loader2 className="h-3 w-3 animate-spin" />
              ) : (
                <ExternalLink className="h-3 w-3" />
              )}
              {polling ? "Waiting for authorization..." : "Authorize"}
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 text-xs"
              onClick={onCancel}
              disabled={authorizing}
            >
              Cancel
            </Button>
          </div>

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
                  className="h-7 text-xs font-mono flex-1"
                />
                <Button
                  type="button"
                  size="sm"
                  className="h-7 text-xs"
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

      {!selectedProvider && (
        <p className="text-xs text-muted-foreground">
          Select a provider above or choose Custom for any OAuth2 endpoint.
        </p>
      )}
    </div>
  )
}
