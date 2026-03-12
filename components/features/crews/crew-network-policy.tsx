"use client"

import { useEffect, useState } from "react"
import { Globe, Shield } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"

// Must match internal/sidecar/allowlist.go DefaultAllowedDomains
const DEFAULT_DOMAINS = ["api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com", "api.factory.ai"]

interface CrewNetworkPolicyProps {
  networkMode: string
  allowedDomains: string[]
  canEdit: boolean
  onSave: (mode: string, domains: string[]) => Promise<void>
}

export function CrewNetworkPolicy({ networkMode, allowedDomains, canEdit, onSave }: CrewNetworkPolicyProps) {
  const [mode, setMode] = useState(networkMode)
  const [domains, setDomains] = useState(allowedDomains.join(", "))
  const [saving, setSaving] = useState(false)

  // Resync editor state when props change (e.g. after server-side normalization)
  useEffect(() => { setMode(networkMode) }, [networkMode])
  useEffect(() => { setDomains(allowedDomains.join(", ")) }, [allowedDomains])

  const isFree = mode === "free"
  // Compare parsed domain arrays instead of raw strings to avoid false dirty state
  const parsedDomains = isFree ? [] : domains.split(/[,\n]+/).map((d) => d.trim().toLowerCase()).filter(Boolean)
  const hasChanges = mode !== networkMode || JSON.stringify(parsedDomains) !== JSON.stringify(allowedDomains)

  async function handleSave() {
    setSaving(true)
    try {
      const parsed = isFree ? [] : domains.split(/[,\n]+/).map((d) => d.trim()).filter(Boolean)
      await onSave(mode, parsed)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center gap-2">
          {isFree ? (
            <Globe className="h-4 w-4 text-emerald-600" />
          ) : (
            <Shield className="h-4 w-4 text-amber-600" />
          )}
          <CardTitle className="text-base">Network Policy</CardTitle>
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${
            isFree
              ? "bg-emerald-100 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400"
              : "bg-amber-100 text-amber-700 dark:bg-amber-950 dark:text-amber-400"
          }`}>
            {isFree ? "Unrestricted" : "Restricted"}
          </span>
        </div>
        <CardDescription>
          Control outbound network access for agents in this crew.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
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
              Agents can only access the domains listed below. All other traffic is blocked.
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
                <Label htmlFor="allowed-domains" className="text-xs">Extra Allowed Domains</Label>
                <Textarea
                  id="allowed-domains"
                  value={domains}
                  onChange={(e) => setDomains(e.target.value)}
                  rows={2}
                  placeholder="github.com, api.github.com, registry.npmjs.org"
                  className="font-mono text-xs"
                />
                <p className="text-[11px] text-muted-foreground">
                  Comma or newline-separated list of additional domains to allow.
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

        {canEdit && hasChanges && (
          <Button size="sm" onClick={handleSave} disabled={saving}>
            {saving ? "Saving..." : "Save Network Policy"}
          </Button>
        )}
      </CardContent>
    </Card>
  )
}
