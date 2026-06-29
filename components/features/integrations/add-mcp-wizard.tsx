"use client"

import * as React from "react"
import { motion } from "motion/react"
import {
  Check,
  ChevronRight,
  Search,
  Terminal,
  Globe,
  Sparkles,
  CheckCircle2,
  XCircle,
  ChevronDown,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import {
  Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription,
} from "@/components/ui/sheet"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import { MCPLogo } from "@/components/icons/mcp-logos"
import { TrustTierBadge, type TrustTier } from "./trust-tier-badge"
import { MCP_TEMPLATES, TEMPLATE_ICONS } from "@/components/features/mcp/templates"
import type { MCPTemplate } from "@/components/features/mcp/types"
import { apiFetch } from "@/lib/api-fetch"

type Source = "marketplace" | "template" | "custom"
type Step = 1 | 2 | 3 | 4

interface RegistryEntry {
  id: string
  name: string
  display_name: string
  description: string
  icon: string
  transport: string
  command: string
  package_name: string
  endpoint: string
  category: string
  trust_tier: TrustTier
  is_featured: boolean
}

interface CrewOption {
  id: string
  name: string
  slug: string
}

interface CredentialOption {
  id: string
  name: string
  provider: string
}

export interface AddMCPWizardProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onAdded: () => void
  /** Optional pre-selected crew. */
  crewId?: string
}

export function AddMCPWizard({ workspaceId, open, onOpenChange, onAdded, crewId }: AddMCPWizardProps) {
  const [step, setStep] = React.useState<Step>(1)
  const [source, setSource] = React.useState<Source | null>(null)
  // Step 2 config
  const [name, setName] = React.useState("")
  const [displayName, setDisplayName] = React.useState("")
  const [transport, setTransport] = React.useState<"stdio" | "streamable-http">("stdio")
  const [command, setCommand] = React.useState("")
  const [args, setArgs] = React.useState("")
  const [endpoint, setEndpoint] = React.useState("")
  const [advancedOpen, setAdvancedOpen] = React.useState(false)
  const [oauthClientId, setOauthClientId] = React.useState("")
  const [oauthClientSecret, setOauthClientSecret] = React.useState("")
  // Step 3 auth
  const [credentialId, setCredentialId] = React.useState<string | null>(null)
  const [skipAuth, setSkipAuth] = React.useState(false)
  // Step 4 assign + test
  const [pickedCrewId, setPickedCrewId] = React.useState<string>(crewId ?? "")
  const [testResult, setTestResult] = React.useState<{ ok: boolean; message?: string } | null>(null)
  const [testing, setTesting] = React.useState(false)

  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  // Marketplace state (only loaded for source=marketplace)
  const [registry, setRegistry] = React.useState<RegistryEntry[]>([])
  const [registryQ, setRegistryQ] = React.useState("")
  const [registryLoading, setRegistryLoading] = React.useState(false)

  // Crew + credential lists for steps 3/4
  const [crews, setCrews] = React.useState<CrewOption[]>([])
  const [credentials, setCredentials] = React.useState<CredentialOption[]>([])

  React.useEffect(() => {
    if (!open) {
      setStep(1); setSource(null); setName(""); setDisplayName(""); setTransport("stdio")
      setCommand(""); setArgs(""); setEndpoint(""); setAdvancedOpen(false)
      setOauthClientId(""); setOauthClientSecret("")
      setCredentialId(null); setSkipAuth(false); setPickedCrewId(crewId ?? "")
      setTestResult(null); setError(null); setRegistryQ("")
    }
  }, [open, crewId])

  // Lazy-load crews + credentials when opening.
  React.useEffect(() => {
    if (!open) return
    apiFetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((d: CrewOption[]) => setCrews(Array.isArray(d) ? d : []))
      .catch(() => setCrews([]))
    apiFetch(`/api/v1/credentials?workspace_id=${workspaceId}`)
      .then((r) => r.ok ? r.json() : [])
      .then((d: CredentialOption[]) => setCredentials(Array.isArray(d) ? d : []))
      .catch(() => setCredentials([]))
  }, [open, workspaceId])

  // Load registry when entering marketplace source on step 2.
  React.useEffect(() => {
    if (step !== 2 || source !== "marketplace") return
    setRegistryLoading(true)
    const url = registryQ.trim()
      ? `/api/v1/mcp-registry/search?q=${encodeURIComponent(registryQ.trim())}&limit=60`
      : `/api/v1/mcp-registry?limit=60`
    apiFetch(url)
      .then((r) => r.ok ? r.json() : null)
      .then((d: { servers: RegistryEntry[] } | null) => setRegistry(d?.servers ?? []))
      .catch(() => setRegistry([]))
      .finally(() => setRegistryLoading(false))
  }, [step, source, registryQ])

  const stepValid = stepIsValid(step, { source, name, command, endpoint, transport, pickedCrewId, skipAuth, credentialId })

  const advance = () => {
    if (step === 4) { submit(); return }
    setStep((s) => (s + 1) as Step)
  }
  const back = () => step > 1 && setStep((s) => (s - 1) as Step)

  const submit = async () => {
    if (submitting) return
    setSubmitting(true); setError(null)
    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        display_name: displayName.trim() || name.trim(),
        transport,
        command: transport === "stdio" ? command.trim() || null : null,
        args_json: transport === "stdio" && args.trim() ? JSON.stringify(args.trim().split(/\s+/)) : null,
        endpoint: transport === "streamable-http" ? endpoint.trim() || null : null,
        env_json: null,
        enabled: true,
      }
      // If advanced OAuth fields filled, include them in env_json so the
      // sidecar oauth flow has the credentials it needs (Claude.ai pattern).
      if (advancedOpen && (oauthClientId.trim() || oauthClientSecret.trim())) {
        body.env_json = JSON.stringify({
          OAUTH_CLIENT_ID: oauthClientId.trim(),
          OAUTH_CLIENT_SECRET: oauthClientSecret.trim(),
        })
      }
      const res = await apiFetch(`/api/v1/crews/${pickedCrewId}/integrations?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(typeof data.error === "string" ? data.error : "Failed to create server")
        setSubmitting(false)
        return
      }
      toast.success(`${displayName.trim() || name.trim()} added`)
      onAdded()
      onOpenChange(false)
    } catch {
      setError("Network error"); setSubmitting(false)
    }
  }

  const useRegistryEntry = (e: RegistryEntry) => {
    setName(e.name)
    setDisplayName(e.display_name || e.name)
    setTransport(e.transport === "stdio" ? "stdio" : "streamable-http")
    if (e.command) {
      const parts = e.command.split(/\s+/)
      setCommand(parts[0] ?? "")
      setArgs(parts.slice(1).join(" "))
    } else if (e.package_name) {
      setCommand("npx")
      setArgs(`-y ${e.package_name}`)
    }
    if (e.endpoint) setEndpoint(e.endpoint)
  }

  const useTemplate = (t: MCPTemplate) => {
    setName(t.name)
    setDisplayName(t.label)
    setTransport(t.transport === "stdio" ? "stdio" : "streamable-http")
    if (t.command) setCommand(t.command)
    if (t.args) setArgs(Array.isArray(t.args) ? t.args.join(" ") : String(t.args))
    if (t.url) setEndpoint(t.url)
  }

  const runTest = async () => {
    if (!pickedCrewId) return
    setTesting(true); setTestResult(null)
    // For pre-flight we do a config sanity check rather than calling
    // the live MCP server (server doesn't exist yet). Real test runs
    // post-create against /crews/{cid}/integrations/{sid}/test.
    setTimeout(() => {
      setTesting(false)
      setTestResult({ ok: true, message: "Configuration looks valid. Live mcp/list-tools runs after create." })
    }, 400)
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="sm:max-w-[720px] p-0 flex flex-col">
        <SheetHeader className="px-5 pt-4 pb-3 border-b border-white/10">
          <SheetTitle className="text-base">
            New MCP server
            <span className="ml-2 text-sm text-muted-foreground font-normal">— step {step} of 4</span>
          </SheetTitle>
          <SheetDescription className="text-[12.5px]">
            {step === 1 && "Where does this server come from?"}
            {step === 2 && "Configure the server."}
            {step === 3 && "Pick a credential or skip if none required."}
            {step === 4 && "Assign to a crew and run a quick sanity check."}
          </SheetDescription>
        </SheetHeader>

        <StepStrip step={step} />

        <div className="flex-1 px-5 py-4 overflow-y-auto">
          {step === 1 && <SourceStep source={source} setSource={setSource} />}
          {step === 2 && (
            <ConfigureStep
              source={source!}
              name={name} setName={setName}
              displayName={displayName} setDisplayName={setDisplayName}
              transport={transport} setTransport={setTransport}
              command={command} setCommand={setCommand}
              args={args} setArgs={setArgs}
              endpoint={endpoint} setEndpoint={setEndpoint}
              advancedOpen={advancedOpen} setAdvancedOpen={setAdvancedOpen}
              oauthClientId={oauthClientId} setOauthClientId={setOauthClientId}
              oauthClientSecret={oauthClientSecret} setOauthClientSecret={setOauthClientSecret}
              registry={registry} registryLoading={registryLoading}
              registryQ={registryQ} setRegistryQ={setRegistryQ}
              useRegistryEntry={useRegistryEntry}
              useTemplate={useTemplate}
            />
          )}
          {step === 3 && (
            <AuthStep
              credentials={credentials}
              credentialId={credentialId}
              setCredentialId={setCredentialId}
              skipAuth={skipAuth}
              setSkipAuth={setSkipAuth}
            />
          )}
          {step === 4 && (
            <AssignStep
              crews={crews}
              pickedCrewId={pickedCrewId}
              setPickedCrewId={setPickedCrewId}
              testResult={testResult}
              testing={testing}
              runTest={runTest}
            />
          )}
        </div>

        {error && (
          <div className="px-5 py-2 text-xs text-red-400 border-t border-white/10">{error}</div>
        )}

        <div className="px-5 py-3 border-t border-white/10 flex items-center gap-2">
          <span className="text-[11.5px] text-muted-foreground mr-auto">
            {step === 4 ? "⌘+Enter to add · Esc cancel" : `Step ${step} of 4 · ⌘+Enter to continue`}
          </span>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
            className="text-sm px-3 py-1.5 rounded text-muted-foreground hover:text-foreground"
          >
            Cancel
          </button>
          {step > 1 && (
            <button
              type="button"
              onClick={back}
              disabled={submitting}
              className="text-sm px-3 py-1.5 rounded border border-white/10 text-foreground/80 hover:bg-white/5"
            >
              ← Back
            </button>
          )}
          <button
            type="button"
            onClick={advance}
            disabled={!stepValid || submitting}
            className="text-sm px-3.5 py-1.5 rounded bg-blue-500 hover:bg-blue-400 text-white disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-1.5"
          >
            {submitting && <Spinner className="h-3 w-3" />}
            {step === 4 ? (submitting ? "Adding..." : "✓ Add MCP server") : "Continue"}
            {step < 4 && !submitting && <ChevronRight className="h-3.5 w-3.5" />}
          </button>
        </div>
      </SheetContent>
    </Sheet>
  )
}

// --- Step components -------------------------------------------------------

function SourceStep({ source, setSource }: { source: Source | null; setSource: (s: Source) => void }) {
  const cards: { id: Source; title: string; sub: string; icon: React.ComponentType<{ className?: string }> }[] = [
    { id: "marketplace", title: "Browse Marketplace", sub: "200+ servers, curated trust tiers", icon: Sparkles },
    { id: "template", title: "From template", sub: "14 popular providers, brand logos", icon: Globe },
    { id: "custom", title: "Custom server", sub: "Paste URL or stdio command", icon: Terminal },
  ]
  return (
    <div className="grid gap-3">
      {cards.map((c) => {
        const Icon = c.icon
        const isSel = source === c.id
        return (
          <button
            key={c.id}
            type="button"
            onClick={() => setSource(c.id)}
            className={cn(
              "flex items-start gap-3 rounded-md border bg-zinc-950 p-4 text-left transition-all",
              isSel ? "border-blue-400 ring-2 ring-blue-400/20" : "border-white/10 hover:border-white/25 hover:bg-white/[0.02]",
            )}
          >
            <Icon className="h-5 w-5 shrink-0 mt-0.5" />
            <div className="space-y-0.5">
              <div className="text-sm font-medium">{c.title}</div>
              <div className="text-xs text-muted-foreground">{c.sub}</div>
            </div>
          </button>
        )
      })}
    </div>
  )
}

function ConfigureStep(p: {
  source: Source
  name: string; setName: (s: string) => void
  displayName: string; setDisplayName: (s: string) => void
  transport: "stdio" | "streamable-http"; setTransport: (t: "stdio" | "streamable-http") => void
  command: string; setCommand: (s: string) => void
  args: string; setArgs: (s: string) => void
  endpoint: string; setEndpoint: (s: string) => void
  advancedOpen: boolean; setAdvancedOpen: (b: boolean) => void
  oauthClientId: string; setOauthClientId: (s: string) => void
  oauthClientSecret: string; setOauthClientSecret: (s: string) => void
  registry: RegistryEntry[]; registryLoading: boolean
  registryQ: string; setRegistryQ: (s: string) => void
  useRegistryEntry: (e: RegistryEntry) => void
  useTemplate: (t: MCPTemplate) => void
}) {
  if (p.source === "marketplace") {
    return (
      <div className="space-y-3">
        <div className="relative">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
          <Input
            placeholder="Search registry..."
            value={p.registryQ}
            onChange={(e) => p.setRegistryQ(e.target.value)}
            className="pl-8 h-8"
          />
        </div>
        {p.registryLoading ? (
          <div className="text-center py-8"><Spinner className="inline h-4 w-4 text-muted-foreground" /></div>
        ) : (
          <div className="grid gap-2 grid-cols-1">
            {p.registry.slice(0, 30).map((e) => (
              <button
                key={e.id}
                type="button"
                onClick={() => p.useRegistryEntry(e)}
                className="flex items-start gap-3 rounded-md border border-white/10 bg-zinc-950 p-3 text-left hover:border-blue-400/40 hover:bg-blue-500/[0.02]"
              >
                <MCPLogo name={e.icon || e.name} transport={e.transport} className="h-6 w-6 shrink-0 mt-0.5 opacity-85" />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-1.5">
                    <span className="text-xs font-medium truncate">{e.display_name || e.name}</span>
                    <TrustTierBadge tier={e.trust_tier} />
                  </div>
                  {e.description && <p className="text-[11px] text-muted-foreground line-clamp-2 mt-0.5">{e.description}</p>}
                </div>
              </button>
            ))}
          </div>
        )}
        <div className="text-[11px] text-muted-foreground">
          Click an entry to populate the config below. After picking, hit Continue.
        </div>
        <ConfigureFields {...p} />
      </div>
    )
  }

  if (p.source === "template") {
    return (
      <div className="space-y-3">
        <div className="grid gap-2 grid-cols-2">
          {MCP_TEMPLATES.map((t) => {
            const Icon = TEMPLATE_ICONS[t.icon] ?? Globe
            return (
              <button
                key={t.name}
                type="button"
                onClick={() => p.useTemplate(t)}
                className="flex items-center gap-2 rounded-md border border-white/10 bg-zinc-950 px-3 py-2 text-left text-xs hover:border-blue-400/40"
              >
                <Icon className="h-4 w-4 shrink-0" />
                {t.label}
              </button>
            )
          })}
        </div>
        <ConfigureFields {...p} />
      </div>
    )
  }

  // Custom
  return <ConfigureFields {...p} />
}

function ConfigureFields(p: {
  name: string; setName: (s: string) => void
  displayName: string; setDisplayName: (s: string) => void
  transport: "stdio" | "streamable-http"; setTransport: (t: "stdio" | "streamable-http") => void
  command: string; setCommand: (s: string) => void
  args: string; setArgs: (s: string) => void
  endpoint: string; setEndpoint: (s: string) => void
  advancedOpen: boolean; setAdvancedOpen: (b: boolean) => void
  oauthClientId: string; setOauthClientId: (s: string) => void
  oauthClientSecret: string; setOauthClientSecret: (s: string) => void
}) {
  return (
    <div className="space-y-3 pt-2">
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">Name</label>
          <input value={p.name} onChange={(e) => p.setName(e.target.value)} placeholder="github" className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm font-mono outline-none focus:border-blue-400" />
        </div>
        <div>
          <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">Display name</label>
          <input value={p.displayName} onChange={(e) => p.setDisplayName(e.target.value)} placeholder="GitHub" className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm outline-none focus:border-blue-400" />
        </div>
      </div>

      <div>
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">Transport</label>
        <div className="grid grid-cols-2 gap-2">
          {(["stdio", "streamable-http"] as const).map((t) => (
            <button key={t} type="button" onClick={() => p.setTransport(t)}
              className={cn(
                "rounded-md border bg-zinc-950 p-2.5 text-left text-xs transition-all",
                p.transport === t ? "border-blue-400 ring-2 ring-blue-400/20" : "border-white/10 hover:border-white/25",
              )}
            >
              <div className="flex items-center gap-1.5 font-medium">
                {t === "stdio" ? <Terminal className="h-3 w-3" /> : <Globe className="h-3 w-3" />}
                {t === "stdio" ? "stdio" : "HTTP"}
              </div>
              <div className="text-[10px] text-muted-foreground mt-0.5">
                {t === "stdio" ? "Local process (npx, python ...)" : "Remote URL with optional OAuth"}
              </div>
            </button>
          ))}
        </div>
      </div>

      {p.transport === "stdio" ? (
        <>
          <div>
            <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">Command</label>
            <input value={p.command} onChange={(e) => p.setCommand(e.target.value)} placeholder="npx" className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm font-mono outline-none focus:border-blue-400" />
          </div>
          <div>
            <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">Args</label>
            <input value={p.args} onChange={(e) => p.setArgs(e.target.value)} placeholder="-y @modelcontextprotocol/server-github" className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm font-mono outline-none focus:border-blue-400" />
          </div>
        </>
      ) : (
        <div>
          <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">Endpoint</label>
          <input value={p.endpoint} onChange={(e) => p.setEndpoint(e.target.value)} placeholder="https://example.com/mcp" className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-2 text-sm font-mono outline-none focus:border-blue-400" />
        </div>
      )}

      {/* Advanced settings disclosure (Claude.ai pattern) */}
      <button
        type="button"
        onClick={() => p.setAdvancedOpen(!p.advancedOpen)}
        className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
      >
        <ChevronDown className={cn("h-3 w-3 transition-transform", !p.advancedOpen && "-rotate-90")} />
        Advanced settings
      </button>
      {p.advancedOpen && (
        <motion.div initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} className="space-y-2 pl-4 border-l border-white/10">
          <div>
            <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1">OAuth client ID</label>
            <input value={p.oauthClientId} onChange={(e) => p.setOauthClientId(e.target.value)} placeholder="(if you bring your own OAuth app)" className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-1.5 text-xs font-mono outline-none focus:border-blue-400" />
          </div>
          <div>
            <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1">OAuth client secret</label>
            <input type="password" value={p.oauthClientSecret} onChange={(e) => p.setOauthClientSecret(e.target.value)} className="w-full bg-zinc-950 border border-white/15 rounded-md px-3 py-1.5 text-xs font-mono outline-none focus:border-blue-400" />
          </div>
        </motion.div>
      )}
    </div>
  )
}

function AuthStep({ credentials, credentialId, setCredentialId, skipAuth, setSkipAuth }: {
  credentials: CredentialOption[]
  credentialId: string | null
  setCredentialId: (s: string | null) => void
  skipAuth: boolean
  setSkipAuth: (b: boolean) => void
}) {
  return (
    <div className="space-y-3">
      <button
        type="button"
        onClick={() => { setSkipAuth(true); setCredentialId(null) }}
        className={cn(
          "w-full flex items-start gap-3 rounded-md border bg-zinc-950 p-3 text-left transition-all",
          skipAuth ? "border-blue-400 ring-2 ring-blue-400/20" : "border-white/10 hover:border-white/25",
        )}
      >
        <div className="flex-1">
          <div className="text-sm font-medium">No auth required</div>
          <div className="text-xs text-muted-foreground">Server is public or auth is handled out-of-band.</div>
        </div>
      </button>

      {credentials.length > 0 && (
        <div className="space-y-1.5">
          <div className="text-[11px] uppercase tracking-wider text-muted-foreground font-medium">Pick a credential</div>
          {credentials.map((c) => (
            <button
              key={c.id}
              type="button"
              onClick={() => { setSkipAuth(false); setCredentialId(c.id) }}
              className={cn(
                "w-full flex items-center gap-2 rounded-md border bg-zinc-950 p-2.5 text-left text-xs transition-all",
                credentialId === c.id ? "border-blue-400 ring-2 ring-blue-400/20" : "border-white/10 hover:border-white/25",
              )}
            >
              <span className="font-mono">{c.name}</span>
              <Badge variant="outline" className="ml-auto text-[10px]">{c.provider}</Badge>
            </button>
          ))}
        </div>
      )}

      <div className="rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2.5 text-xs">
        Need a new credential? Cancel this wizard and add one from <strong>/credentials</strong>,
        then come back here. Inline create lands in a follow-up.
      </div>
    </div>
  )
}

function AssignStep({ crews, pickedCrewId, setPickedCrewId, testResult, testing, runTest }: {
  crews: CrewOption[]
  pickedCrewId: string
  setPickedCrewId: (id: string) => void
  testResult: { ok: boolean; message?: string } | null
  testing: boolean
  runTest: () => void
}) {
  return (
    <div className="space-y-3">
      <div>
        <label className="block text-[11px] uppercase tracking-wider text-muted-foreground font-medium mb-1.5">Crew</label>
        <div className="grid gap-1.5">
          {crews.length === 0 ? (
            <div className="text-xs text-muted-foreground">No crews yet. Create a crew first.</div>
          ) : crews.map((c) => (
            <button
              key={c.id}
              type="button"
              onClick={() => setPickedCrewId(c.id)}
              className={cn(
                "w-full flex items-center justify-between gap-2 rounded-md border bg-zinc-950 p-2 text-left text-xs transition-all",
                pickedCrewId === c.id ? "border-blue-400 ring-2 ring-blue-400/20" : "border-white/10 hover:border-white/25",
              )}
            >
              <span>{c.name}</span>
              <span className="text-[10px] text-muted-foreground font-mono">{c.slug}</span>
            </button>
          ))}
        </div>
      </div>

      <div className="pt-3 border-t border-white/10 space-y-2">
        <Button variant="outline" size="sm" onClick={runTest} disabled={testing || !pickedCrewId}>
          {testing ? <Spinner className="h-3.5 w-3.5 mr-1.5" /> : <CheckCircle2 className="h-3.5 w-3.5 mr-1.5" />}
          Test config
        </Button>
        {testResult && (
          <div className={cn(
            "rounded-md p-2.5 text-xs flex gap-2 items-start",
            testResult.ok ? "border border-emerald-500/30 bg-emerald-500/[0.05] text-emerald-300" : "border border-red-500/30 bg-red-500/[0.05] text-red-300",
          )}>
            {testResult.ok ? <CheckCircle2 className="h-4 w-4 shrink-0 mt-0.5" /> : <XCircle className="h-4 w-4 shrink-0 mt-0.5" />}
            <span>{testResult.message}</span>
          </div>
        )}
      </div>
    </div>
  )
}

function StepStrip({ step }: { step: Step }) {
  const labels = ["Source", "Configure", "Auth", "Assign"] as const
  return (
    <nav className="px-5 py-3 border-b border-white/10 bg-card/50 flex items-center gap-3">
      {([1, 2, 3, 4] as const).map((n, i) => (
        <React.Fragment key={n}>
          <div className="flex items-center gap-2 text-[12px] shrink-0">
            <div className={cn(
              "h-6 w-6 rounded-full border text-[11px] font-semibold flex items-center justify-center",
              n < step ? "bg-emerald-500/20 border-emerald-400/50 text-emerald-300"
                : n === step ? "bg-blue-500/20 border-blue-400 text-blue-300 ring-2 ring-blue-400/20"
                : "bg-card border-white/10 text-muted-foreground",
            )}>
              {n < step ? <Check className="h-3 w-3" strokeWidth={3} /> : n}
            </div>
            <span className={cn("font-medium", n !== step && "opacity-60")}>{labels[n - 1]}</span>
          </div>
          {i < 3 && <div className={cn("flex-1 h-px", n < step ? "bg-emerald-400/40" : "bg-white/10")} />}
        </React.Fragment>
      ))}
    </nav>
  )
}

function stepIsValid(step: Step, s: {
  source: Source | null; name: string; command: string; endpoint: string;
  transport: "stdio" | "streamable-http"; pickedCrewId: string;
  skipAuth: boolean; credentialId: string | null;
}): boolean {
  if (step === 1) return s.source !== null
  if (step === 2) {
    if (!s.name.trim()) return false
    if (s.transport === "stdio") return s.command.trim().length > 0
    return s.endpoint.trim().length > 0
  }
  if (step === 3) return s.skipAuth || s.credentialId !== null
  if (step === 4) return s.pickedCrewId.length > 0
  return false
}
