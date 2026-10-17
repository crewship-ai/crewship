"use client"

import * as React from "react"
import { Check, Copy, TerminalSquare } from "lucide-react"

import { CreateSurface, CreateSurfaceBody, CreateSurfaceFooter, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { Button } from "@/components/ui/button"

/** Commands that exist in the CLI today — nothing here is aspirational. */
export const VAULT_CLI_COMMANDS: { cmd: string; what: string }[] = [
  { cmd: "crewship credential list", what: "Every secret, never a value" },
  { cmd: 'crewship credential create --name "Stripe secret key" --type API_KEY --provider STRIPE', what: "Add a secret" },
  { cmd: "crewship credential login --provider OPENAI", what: "Sign in with ChatGPT (device code)" },
  { cmd: 'crewship credential rotate "Linear token" --grace-seconds 86400', what: "Replace the value; the old one works for 24 h" },
  { cmd: "crewship credential reveal-policy", what: "Who may reveal" },
  { cmd: "crewship credential pool list", what: "Account groups" },
]

export function CliLines({ commands }: { commands: { cmd: string; what: string }[] }) {
  const [copied, setCopied] = React.useState<string | null>(null)
  return (
    <ol className="flex flex-col gap-2">
      {commands.map((c) => (
        <li key={c.cmd} className="rounded-lg border border-border bg-muted/40 px-3 py-2">
          <div className="flex items-start gap-2">
            <code className="min-w-0 flex-1 break-all font-mono text-label text-foreground">{c.cmd}</code>
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={`Copy ${c.cmd}`}
              onClick={() => {
                navigator.clipboard?.writeText(c.cmd).then(() => setCopied(c.cmd), () => setCopied(null))
              }}
            >
              {copied === c.cmd ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
            </Button>
          </div>
          <small className="text-micro text-muted-foreground">{c.what}</small>
        </li>
      ))}
    </ol>
  )
}

export function VaultCliDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="md">
      <CreateSurfaceHeader icon={TerminalSquare} title="Vault from the command line" description="The same actions, from a terminal or a script." onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody>
        <CliLines commands={VAULT_CLI_COMMANDS} />
      </CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => onOpenChange(false)} cancelLabel="Close" hint={null} guardCancel={false} />
    </CreateSurface>
  )
}
