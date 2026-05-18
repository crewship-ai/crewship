"use client"

import { Bot, Key, ExternalLink, Lock, Terminal, User, KeyRound, ShieldCheck } from "lucide-react"
import { GitHubIcon } from "@/components/icons/provider-icons"
import { cn } from "@/lib/utils"
import type { AuthMethod, WizardState } from "./types"
import { PROVIDER_AUTH_METHODS, defaultEnvVarName } from "./types"

interface Props {
  state: WizardState
  setState: (patch: Partial<WizardState>) => void
}

const METHOD_META: Record<AuthMethod, {
  label: string
  icon: React.ComponentType<{ className?: string }>
  description: string
  type: WizardState["type"]
}> = {
  "setup-token": {
    label: "Setup token (Claude Max)",
    icon: Bot,
    description: "Long-lived token from `claude setup-token`. Recommended for Claude Max subscribers.",
    type: "AI_CLI_TOKEN",
  },
  "api-key": {
    label: "API key",
    icon: Key,
    description: "Raw API key from the provider's console.",
    type: "API_KEY",
  },
  "oauth": {
    label: "OAuth (1-click)",
    icon: ExternalLink,
    description: "Sign in with the provider — token is managed automatically.",
    type: "OAUTH2",
  },
  "pat": {
    label: "Personal access token",
    icon: Key,
    description: "Long-lived PAT or service account token.",
    type: "CLI_TOKEN",
  },
  "github-app": {
    label: "GitHub App",
    icon: GitHubIcon,
    description: "Install a GitHub App with scoped permissions (org-level).",
    type: "OAUTH2",
  },
  "secret": {
    label: "Generic secret",
    icon: Lock,
    description: "Opaque value stored encrypted; injected into the agent as ENV.",
    type: "SECRET",
  },
  // Vault types — the StepProvider flow sets these defaults directly,
  // so the user normally never sees this picker. Entries exist here
  // because METHOD_META is a Record<AuthMethod, …> and TS rightly
  // refuses partial maps, and because the single-method-card branch
  // above renders for any provider whose PROVIDER_AUTH_METHODS list
  // is omitted (vault tiles fall through to that branch).
  "userpass": {
    label: "Username + Password",
    icon: User,
    description: "Cleartext username + encrypted password. Injected as <NAME>_USERNAME / <NAME>_PASSWORD env vars.",
    type: "USERPASS",
  },
  "ssh-key": {
    label: "SSH private key",
    icon: KeyRound,
    description: "PEM-encoded private key, mounted at ~/.ssh/keys/<name> with mode 0600.",
    type: "SSH_KEY",
  },
  "certificate": {
    label: "TLS certificate",
    icon: ShieldCheck,
    description: "PEM-encoded cert chain, mounted at /secrets/<agent>/certs/<name>.pem with mode 0400.",
    type: "CERTIFICATE",
  },
}

export function StepAuth({ state, setState }: Props) {
  if (!state.provider) return null
  const methods = PROVIDER_AUTH_METHODS[state.provider] ?? [state.authMethod ?? "api-key"]

  // Provider with only one method: skip the picker, just show description.
  if (methods.length === 1) {
    const m = methods[0]
    const meta = METHOD_META[m]
    const Icon = meta.icon
    return (
      <div className="space-y-3">
        <div className="rounded-md border border-blue-400 bg-blue-500/[0.05] p-4 flex gap-3">
          <Icon className="h-5 w-5 shrink-0 mt-0.5" />
          <div className="space-y-1">
            <p className="text-sm font-medium">{meta.label}</p>
            <p className="text-xs text-muted-foreground">{meta.description}</p>
          </div>
        </div>
        <div className="rounded-md border border-blue-500/25 bg-blue-500/[0.05] px-3 py-2.5 text-xs text-foreground/80 flex gap-2.5 items-start">
          <span className="shrink-0 text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded bg-blue-500/90 text-blue-950 mt-0.5">
            TIP
          </span>
          <span className="leading-relaxed">
            This provider only supports one auth method. Continue to paste your token.
          </span>
        </div>
      </div>
    )
  }

  return (
    <div className="grid gap-3">
      {methods.map((m, idx) => {
        const meta = METHOD_META[m]
        const Icon = meta.icon
        const isSelected = state.authMethod === m
        const isRecommended = idx === 0
        return (
          <button
            key={m}
            type="button"
            onClick={() => {
              setState({
                authMethod: m,
                type: meta.type,
                name: defaultEnvVarName(state.provider!, m),
                // Reset value+username+test if auth method changed
                // mid-flow — switching from "pat" to "userpass" must
                // not leak the half-typed token into the new fields.
                value: "",
                username: "",
                testResult: null,
              })
            }}
            className={cn(
              "flex items-start gap-3 rounded-md border bg-zinc-950 p-4 text-left transition-all",
              isSelected
                ? "border-blue-400 ring-2 ring-blue-400/20"
                : "border-white/10 hover:border-white/25 hover:bg-white/[0.02]",
            )}
          >
            <Icon className="h-5 w-5 shrink-0 mt-0.5" />
            <div className="flex-1 space-y-1">
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium">{meta.label}</span>
                {isRecommended && (
                  <span className="text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded bg-blue-500/90 text-blue-950">
                    Recommended
                  </span>
                )}
              </div>
              <p className="text-xs text-muted-foreground">{meta.description}</p>
            </div>
          </button>
        )
      })}
    </div>
  )
}

// Suppress unused import warning (Terminal kept for future custom-CLI tile)
const _ = Terminal
