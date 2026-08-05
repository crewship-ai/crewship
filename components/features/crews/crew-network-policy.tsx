"use client"

import { useEffect, useState } from "react"
import { Globe, Shield, ShieldOff, Package, Network } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { PACKAGE_REGISTRY_DOMAINS, mergeDomains } from "./registry-presets"

// Must match internal/sidecar/allowlist.go DefaultAllowedDomains
const DEFAULT_DOMAINS = [
  "api.anthropic.com",
  "console.anthropic.com",
  "api.openai.com",
  "auth.openai.com",
  "chatgpt.com",
  "generativelanguage.googleapis.com",
  "oauth2.googleapis.com",
  "accounts.google.com",
  "api.cursor.sh",
  "api2.cursor.sh",
  "api.factory.ai",
  "app.factory.ai",
  // OpenCode BYOK providers (#944)
  "openrouter.ai",
  "api.x.ai",
  "api.groq.com",
  "api.deepseek.com",
  "api.moonshot.ai",
  "api.z.ai",
  "api.minimax.io",
]

interface CrewNetworkPolicyProps {
  /** The CONFIGURED mode — what the operator asked for and what is stored. */
  networkMode: string
  /** #1648 — whether the server's container provider actually applies
   *  networkMode. This card used to render the configured value as though it
   *  were the effective one, so a crew on a provider with no egress proxy
   *  showed a green-lit "Restricted" fence that nothing was checking.
   *  Undefined means the backend did not report it (older server): the card
   *  renders as before rather than claiming either state. */
  enforced?: boolean
  /** The provider's own explanation, shown when enforced === false. */
  unenforcedReason?: string
  allowedDomains: string[]
  /** #1377 gap 3 — crews.allow_private_endpoints (migration v135). Undefined
   *  means the caller didn't load the field; the toggle stays hidden rather
   *  than rendering a control that would PATCH a value we never read. */
  allowPrivateEndpoints?: boolean
  canEdit: boolean
  /** Private-endpoint egress is an ADMIN ("manage") capability server-side
   *  (crews_create.go:303 / the crews_update manage gate). Off by default so a
   *  caller that forgets to pass it renders read-only, not falsely editable. */
  canEditPrivateEndpoints?: boolean
  onSave: (mode: string, domains: string[], allowPrivateEndpoints?: boolean) => Promise<void>
}

export function CrewNetworkPolicy({
  networkMode,
  enforced,
  unenforcedReason,
  allowedDomains,
  allowPrivateEndpoints,
  canEdit,
  canEditPrivateEndpoints = false,
  onSave,
}: CrewNetworkPolicyProps) {
  const [mode, setMode] = useState(networkMode)
  const [domains, setDomains] = useState(allowedDomains.join(", "))
  const [privateEndpoints, setPrivateEndpoints] = useState(Boolean(allowPrivateEndpoints))
  const [saving, setSaving] = useState(false)

  // Resync editor state when props change (e.g. after server-side normalization)
  useEffect(() => { setMode(networkMode) }, [networkMode])
  useEffect(() => { setDomains(allowedDomains.join(", ")) }, [allowedDomains])
  useEffect(() => { setPrivateEndpoints(Boolean(allowPrivateEndpoints)) }, [allowPrivateEndpoints])

  const isFree = mode === "free"
  // Keyed on the SAVED mode, not the editor's `mode`: the server reported
  // enforcement for what is stored, and predicting the answer for a mode the
  // operator has only clicked would be guessing. Flipping to restricted and
  // saving surfaces it on the next render, plus a warning on the PATCH.
  const isUnenforced = enforced === false && networkMode === "restricted"
  const showPrivateEndpoints = allowPrivateEndpoints !== undefined
  // Compare parsed domain arrays instead of raw strings to avoid false dirty state
  const parsedDomains = isFree ? [] : domains.split(/[,\n]+/).map((d) => d.trim().toLowerCase()).filter(Boolean)
  const hasChanges =
    mode !== networkMode ||
    JSON.stringify(parsedDomains) !== JSON.stringify(allowedDomains) ||
    (showPrivateEndpoints && privateEndpoints !== Boolean(allowPrivateEndpoints))

  function addRegistryPreset() {
    const current = domains.split(/[,\n]+/).map((d) => d.trim()).filter(Boolean)
    setDomains(mergeDomains(current, PACKAGE_REGISTRY_DOMAINS).join(", "))
  }

  async function handleSave() {
    setSaving(true)
    try {
      const parsed = isFree ? [] : domains.split(/[,\n]+/).map((d) => d.trim()).filter(Boolean)
      await onSave(mode, parsed, showPrivateEndpoints ? privateEndpoints : undefined)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center gap-2">
          {isFree ? (
            <Globe className="h-4 w-4 text-success" />
          ) : isUnenforced ? (
            <ShieldOff className="h-4 w-4 text-destructive" />
          ) : (
            <Shield className="h-4 w-4 text-warn" />
          )}
          <CardTitle className="text-base">Network Policy</CardTitle>
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${
            isFree
              ? "bg-success/15 text-success dark:bg-success/20 dark:text-success"
              : isUnenforced
                ? "bg-destructive/10 text-destructive dark:bg-destructive/20 dark:text-destructive"
                : "bg-warn/15 text-warn dark:bg-warn/20 dark:text-warn"
          }`}>
            {isFree ? "Unrestricted" : isUnenforced ? "Restricted — not enforced" : "Restricted"}
          </span>
        </div>
        <CardDescription>
          Control outbound network access for agents in this crew.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {isUnenforced && (
          <div
            role="alert"
            className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
          >
            <p className="font-medium">This crew&apos;s egress is not being restricted.</p>
            <p className="mt-1 text-destructive/90">{unenforcedReason}</p>
            <p className="mt-1 text-destructive/90">
              The setting below is kept as your intent and takes effect on a provider that can
              apply it — it is not being applied right now.
            </p>
          </div>
        )}

        {canEdit && (
          <div className="flex gap-2">
            <Button
              type="button"
              variant={isFree ? "default" : "outline"}
              size="sm"
              aria-pressed={isFree}
              onClick={() => { setMode("free"); setDomains("") }}
            >
              <Globe className="mr-1.5 h-3.5 w-3.5" />
              Free
            </Button>
            <Button
              type="button"
              variant={!isFree ? "default" : "outline"}
              size="sm"
              aria-pressed={!isFree}
              onClick={() => setMode("restricted")}
            >
              <Shield className="mr-1.5 h-3.5 w-3.5" />
              Restricted
            </Button>
          </div>
        )}

        {isFree && (
          <p className="text-sm text-muted-foreground">
            Agents can access any domain on the internet.
          </p>
        )}

        {!isFree && (
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">
              {isUnenforced
                ? "Configured to allow only the domains listed below — but nothing is blocking the rest on this provider."
                : "Agents can only access the domains listed below. All other traffic is blocked."}
            </p>
            <div className="space-y-1">
              <Label className="text-xs text-muted-foreground">Always Allowed (LLM APIs)</Label>
              <div className="flex flex-wrap gap-1.5">
                {DEFAULT_DOMAINS.map((d) => (
                  <span key={d} className="inline-flex items-center rounded-md bg-muted px-2 py-0.5 text-[11px] font-mono text-muted-foreground">
                    {d}
                  </span>
                ))}
              </div>
            </div>
            {canEdit ? (
              <div className="space-y-1">
                <div className="flex items-center justify-between gap-2">
                  <Label htmlFor="allowed-domains" className="text-xs">Extra Allowed Domains</Label>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-6 px-2 text-[11px]"
                    onClick={addRegistryPreset}
                  >
                    <Package className="mr-1 h-3 w-3" />
                    Allow package registries
                  </Button>
                </div>
                <Textarea
                  id="allowed-domains"
                  value={domains}
                  onChange={(e) => setDomains(e.target.value)}
                  rows={2}
                  placeholder="github.com, *.github.com, registry.npmjs.org"
                  className="font-mono text-xs"
                />
                <p className="text-[11px] text-muted-foreground">
                  Comma or newline-separated. Use a <code className="font-mono">*.github.com</code> wildcard
                  to allow every subdomain (the apex <code className="font-mono">github.com</code> stays separate).
                  “Allow package registries” adds npm, pip, cargo, go, apt &amp; Docker Hub hosts.
                </p>
              </div>
            ) : allowedDomains.length > 0 && (
              <div className="space-y-1">
                <Label className="text-xs text-muted-foreground">Extra Allowed Domains</Label>
                <div className="flex flex-wrap gap-1.5">
                  {allowedDomains.map((d) => (
                    <span key={d} className="inline-flex items-center rounded-md bg-muted px-2 py-0.5 text-[11px] font-mono text-muted-foreground">
                      {d}
                    </span>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}

        {/* Private endpoints — orthogonal to free/restricted. The SSRF fence
            blocks RFC1918 / loopback / link-local targets in BOTH modes, so an
            on-prem Ollama or LAN model needs this opt-in even on a `free` crew.
            Surfacing it here is what stops "why is my LAN model unreachable?"
            from being a CLI-only mystery (#1377 gap 3). */}
        {showPrivateEndpoints && (
          <div className="space-y-1.5 border-t pt-3">
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0">
                <Label htmlFor="allow-private-endpoints" className="text-xs font-medium flex items-center gap-1.5">
                  <Network className="h-3.5 w-3.5 text-muted-foreground" />
                  Private endpoints
                </Label>
                <p className="text-[11px] text-muted-foreground">
                  Let this crew reach private / LAN addresses (on-prem Ollama, a
                  self-hosted model endpoint). Off by default — the SSRF fence blocks
                  RFC1918, loopback and link-local targets in both network modes.
                </p>
              </div>
              <Switch
                id="allow-private-endpoints"
                aria-label="Private endpoints"
                checked={privateEndpoints}
                disabled={!canEdit || !canEditPrivateEndpoints}
                onCheckedChange={setPrivateEndpoints}
              />
            </div>
            {privateEndpoints && (
              <p className="text-[11px] text-warn dark:text-warn">
                Also requires the instance ceiling{" "}
                <code className="font-mono">CREWSHIP_ALLOW_PRIVATE_ENDPOINTS</code> on the
                server — without it the crew flag alone will not unblock a private target.
              </p>
            )}
            {!privateEndpoints && (
              <p className="text-[11px] text-muted-foreground">
                Blocked targets are reported as an SSRF-fence denial. The server ceiling{" "}
                <code className="font-mono">CREWSHIP_ALLOW_PRIVATE_ENDPOINTS</code> must also
                be set for this flag to take effect.
              </p>
            )}
            {!canEditPrivateEndpoints && (
              <p className="text-[11px] text-muted-foreground">
                Requires an admin to change.
              </p>
            )}
          </div>
        )}

        {canEdit && hasChanges && (
          <Button size="sm" onClick={handleSave} disabled={saving}>
            {saving ? "Saving..." : "Save Network Policy"}
          </Button>
        )}
      </CardContent>
    </Card>
  )
}
