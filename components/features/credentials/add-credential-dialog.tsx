"use client"

import * as React from "react"
import { Eye, EyeOff, Loader2, Bot, Key, Lock } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

type CredentialType = "AI_CLI_TOKEN" | "API_KEY" | "SECRET"
type CredentialProvider = "ANTHROPIC" | "OPENAI" | "GOOGLE" | "NONE"

interface Team {
  id: string
  name: string
}

interface AddCredentialDialogProps {
  orgId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}

const PROVIDER_ENV_NAMES: Record<string, string> = {
  ANTHROPIC: "ANTHROPIC_API_KEY",
  OPENAI: "OPENAI_API_KEY",
  GOOGLE: "GOOGLE_API_KEY",
}

const TYPE_CONFIG = {
  AI_CLI_TOKEN: {
    icon: Bot,
    label: "AI CLI Token",
    description: "Setup token from AI CLI (claude, codex)",
  },
  API_KEY: {
    icon: Key,
    label: "API Key",
    description: "API key from provider console",
  },
  SECRET: {
    icon: Lock,
    label: "Secret",
    description: "Internal secret or environment variable",
  },
} as const

export function AddCredentialDialog({
  orgId,
  open,
  onOpenChange,
  onSuccess,
}: AddCredentialDialogProps) {
  const [type, setType] = React.useState<CredentialType>("API_KEY")
  const [provider, setProvider] = React.useState<CredentialProvider>("ANTHROPIC")
  const [name, setName] = React.useState("")
  const [description, setDescription] = React.useState("")
  const [value, setValue] = React.useState("")
  const [accountLabel, setAccountLabel] = React.useState("")
  const [scope, setScope] = React.useState<"ORGANIZATION" | "TEAM">("ORGANIZATION")
  const [teamId, setTeamId] = React.useState<string>("")
  const [showValue, setShowValue] = React.useState(false)
  const [teams, setTeams] = React.useState<Team[]>([])
  const [teamsLoading, setTeamsLoading] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState("")

  React.useEffect(() => {
    if (type !== "SECRET" && provider !== "NONE") {
      setName(PROVIDER_ENV_NAMES[provider] || "")
    }
  }, [type, provider])

  React.useEffect(() => {
    if (scope === "TEAM" && teams.length === 0) {
      setTeamsLoading(true)
      fetch(`/api/v1/teams?org_id=${orgId}`)
        .then((res) => res.json())
        .then((data: Team[]) => setTeams(data))
        .catch(() => setTeams([]))
        .finally(() => setTeamsLoading(false))
    }
  }, [scope, orgId, teams.length])

  function resetForm() {
    setType("API_KEY")
    setProvider("ANTHROPIC")
    setName("")
    setDescription("")
    setValue("")
    setAccountLabel("")
    setScope("ORGANIZATION")
    setTeamId("")
    setShowValue(false)
    setError("")
  }

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) resetForm()
    onOpenChange(nextOpen)
  }

  function handleTypeChange(newType: CredentialType) {
    setType(newType)
    if (newType === "SECRET") {
      setProvider("NONE")
      setName("")
      setAccountLabel("")
    } else {
      setProvider("ANTHROPIC")
    }
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError("")

    if (!name.trim()) {
      setError("Name is required")
      return
    }
    if (!value.trim()) {
      setError("Value is required")
      return
    }
    if (type !== "SECRET" && provider === "NONE") {
      setError("Provider is required for AI CLI Token and API Key")
      return
    }
    if (scope === "TEAM" && !teamId) {
      setError("Team is required for team-scoped credentials")
      return
    }

    setSubmitting(true)

    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        value,
        type,
        provider,
        scope,
      }
      if (description.trim()) body.description = description.trim()
      if (accountLabel.trim()) body.account_label = accountLabel.trim()
      if (scope === "TEAM") body.team_id = teamId

      const res = await fetch(`/api/v1/credentials?org_id=${orgId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })

      if (!res.ok) {
        const data = await res.json()
        setError(typeof data.error === "string" ? data.error : "Failed to create credential")
        return
      }

      handleOpenChange(false)
      onSuccess()
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Add Credential</DialogTitle>
          <DialogDescription>
            Add an AI token, API key, or secret. Encrypted with AES-256-GCM.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label>Type</Label>
            <div className="grid grid-cols-3 gap-2">
              {(["AI_CLI_TOKEN", "API_KEY", "SECRET"] as const).map((t) => {
                const cfg = TYPE_CONFIG[t]
                const Icon = cfg.icon
                const isActive = type === t
                return (
                  <button
                    key={t}
                    type="button"
                    onClick={() => handleTypeChange(t)}
                    className={`flex flex-col items-center gap-1.5 rounded-md border p-3 text-xs transition-colors ${
                      isActive
                        ? "border-primary bg-primary/5 text-primary"
                        : "border-border hover:bg-muted"
                    }`}
                  >
                    <Icon className="h-5 w-5" />
                    <span className="font-medium">{cfg.label}</span>
                  </button>
                )
              })}
            </div>
          </div>

          {type !== "SECRET" && (
            <div className="space-y-2">
              <Label htmlFor="cred-provider">Provider</Label>
              <Select value={provider} onValueChange={(v) => setProvider(v as CredentialProvider)}>
                <SelectTrigger id="cred-provider" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ANTHROPIC">Anthropic (Claude)</SelectItem>
                  <SelectItem value="OPENAI">OpenAI (GPT / Codex)</SelectItem>
                  <SelectItem value="GOOGLE">Google (Gemini)</SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}

          <div className="space-y-2">
            <Label htmlFor="cred-name">Name (env variable)</Label>
            <Input
              id="cred-name"
              placeholder={type === "SECRET" ? "e.g. GITHUB_TOKEN" : "e.g. ANTHROPIC_API_KEY"}
              value={name}
              onChange={(e) => setName(e.target.value)}
              readOnly={type !== "SECRET"}
              className={type !== "SECRET" ? "bg-muted" : undefined}
              required
            />
          </div>

          {type !== "SECRET" && (
            <div className="space-y-2">
              <Label htmlFor="cred-label">Label (optional)</Label>
              <Input
                id="cred-label"
                placeholder={type === "AI_CLI_TOKEN" ? "e.g. My Claude Max" : "e.g. Production key"}
                value={accountLabel}
                onChange={(e) => setAccountLabel(e.target.value)}
              />
            </div>
          )}

          {type === "AI_CLI_TOKEN" && (
            <div className="rounded-md border border-blue-200 bg-blue-50 p-3 text-xs text-blue-800 dark:border-blue-900 dark:bg-blue-950 dark:text-blue-200 space-y-1">
              <p className="font-medium">How to get a setup token:</p>
              <ol className="list-decimal list-inside space-y-0.5">
                <li>Open terminal on your computer</li>
                <li>
                  Run: <code className="rounded bg-blue-100 px-1 font-mono dark:bg-blue-900">claude setup-token</code>
                </li>
                <li>Copy the entire output and paste below</li>
              </ol>
            </div>
          )}

          <div className="space-y-2">
            <Label htmlFor="cred-value">
              {type === "AI_CLI_TOKEN" ? "Setup Token" : type === "API_KEY" ? "API Key" : "Value"}
            </Label>
            <div className="relative">
              <Input
                id="cred-value"
                type={showValue ? "text" : "password"}
                placeholder={
                  type === "AI_CLI_TOKEN"
                    ? "Paste setup-token output here"
                    : type === "API_KEY"
                      ? "e.g. sk-ant-..."
                      : "Enter secret value"
                }
                value={value}
                onChange={(e) => setValue(e.target.value)}
                required
                className="pr-10 font-mono text-xs"
              />
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                className="absolute right-2 top-1/2 -translate-y-1/2"
                onClick={() => setShowValue(!showValue)}
              >
                {showValue ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                <span className="sr-only">{showValue ? "Hide" : "Show"} value</span>
              </Button>
            </div>
          </div>

          {type === "SECRET" && (
            <div className="space-y-2">
              <Label htmlFor="cred-description">Description</Label>
              <Textarea
                id="cred-description"
                placeholder="Optional description..."
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={2}
              />
            </div>
          )}

          <div className="space-y-2">
            <Label htmlFor="cred-scope">Scope</Label>
            <Select
              value={scope}
              onValueChange={(v) => {
                setScope(v as "ORGANIZATION" | "TEAM")
                if (v === "ORGANIZATION") setTeamId("")
              }}
            >
              <SelectTrigger id="cred-scope" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="ORGANIZATION">Organization</SelectItem>
                <SelectItem value="TEAM">Team</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {scope === "TEAM" && (
            <div className="space-y-2">
              <Label htmlFor="cred-team">Team</Label>
              {teamsLoading ? (
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  Loading teams...
                </div>
              ) : (
                <Select value={teamId} onValueChange={setTeamId}>
                  <SelectTrigger id="cred-team" className="w-full">
                    <SelectValue placeholder="Select a team" />
                  </SelectTrigger>
                  <SelectContent>
                    {teams.map((team) => (
                      <SelectItem key={team.id} value={team.id}>
                        {team.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </div>
          )}

          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => handleOpenChange(false)} disabled={submitting}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              Add Credential
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
