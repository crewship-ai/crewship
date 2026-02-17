"use client"

import * as React from "react"
import { Loader2 } from "lucide-react"
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

interface OrgCredential {
  id: string
  name: string
  description: string | null
  scope: string
}

interface AssignCredentialDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  agentId: string
  workspaceId: string
  onAssigned: () => void
}

const ENV_VAR_PRESETS = [
  "ANTHROPIC_API_KEY",
  "OPENAI_API_KEY",
  "GOOGLE_API_KEY",
  "GITHUB_TOKEN",
]

export function AssignCredentialDialog({
  open,
  onOpenChange,
  agentId,
  workspaceId,
  onAssigned,
}: AssignCredentialDialogProps) {
  const [credentials, setCredentials] = React.useState<OrgCredential[]>([])
  const [selectedCredentialId, setSelectedCredentialId] = React.useState("")
  const [envVarName, setEnvVarName] = React.useState("")
  const [priority, setPriority] = React.useState("0")
  const [loading, setLoading] = React.useState(false)
  const [loadingCreds, setLoadingCreds] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)

  React.useEffect(() => {
    if (!open || !workspaceId) return
    setLoadingCreds(true)
    fetch(`/api/v1/credentials?workspace_id=${workspaceId}`)
      .then((r) => r.json())
      .then((data: OrgCredential[]) => setCredentials(Array.isArray(data) ? data : []))
      .catch(() => setCredentials([]))
      .finally(() => setLoadingCreds(false))
  }, [open, workspaceId])

  function reset() {
    setSelectedCredentialId("")
    setEnvVarName("")
    setPriority("0")
    setError(null)
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!selectedCredentialId || !envVarName.trim()) return

    setLoading(true)
    setError(null)

    try {
      const res = await fetch(`/api/v1/agents/${agentId}/credentials?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          credential_id: selectedCredentialId,
          env_var_name: envVarName.trim(),
          priority: parseInt(priority, 10) || 0,
        }),
      })

      if (!res.ok) {
        const data = await res.json().catch(() => ({ error: "Failed to assign credential" }))
        setError(typeof data.error === "string" ? data.error : "Failed to assign credential")
        return
      }

      reset()
      onOpenChange(false)
      onAssigned()
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!v) reset(); onOpenChange(v) }}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Assign Credential</DialogTitle>
            <DialogDescription>
              Select an workspace credential and configure how it will be injected into the agent environment.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="credential">Credential</Label>
              {loadingCreds ? (
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" /> Loading...
                </div>
              ) : credentials.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No credentials available. Create one in the Credentials page first.
                </p>
              ) : (
                <Select value={selectedCredentialId} onValueChange={setSelectedCredentialId}>
                  <SelectTrigger>
                    <SelectValue placeholder="Select a credential" />
                  </SelectTrigger>
                  <SelectContent>
                    {credentials.map((c) => (
                      <SelectItem key={c.id} value={c.id}>
                        {c.name} ({c.scope})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="env_var">Environment Variable Name</Label>
              <div className="flex flex-wrap gap-1 mb-1.5">
                {ENV_VAR_PRESETS.map((preset) => (
                  <Button
                    key={preset}
                    type="button"
                    variant="outline"
                    size="sm"
                    className="text-xs h-6 px-2"
                    onClick={() => setEnvVarName(preset)}
                  >
                    {preset}
                  </Button>
                ))}
              </div>
              <Input
                id="env_var"
                value={envVarName}
                onChange={(e) => setEnvVarName(e.target.value)}
                placeholder="e.g. ANTHROPIC_API_KEY"
                pattern="^[A-Z_][A-Z0-9_]*$"
                required
              />
              <p className="text-xs text-muted-foreground">
                The credential value will be available as this env var inside the agent container.
              </p>
            </div>

            <div className="space-y-2">
              <Label htmlFor="priority">Priority</Label>
              <Input
                id="priority"
                type="number"
                min="0"
                max="99"
                value={priority}
                onChange={(e) => setPriority(e.target.value)}
              />
              <p className="text-xs text-muted-foreground">
                Lower number = higher priority. Used for credential failover (0 = primary).
              </p>
            </div>

            {error && (
              <p className="text-sm text-destructive">{error}</p>
            )}
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => { reset(); onOpenChange(false) }}>
              Cancel
            </Button>
            <Button type="submit" disabled={loading || !selectedCredentialId || !envVarName.trim()}>
              {loading && <Loader2 className="h-4 w-4 animate-spin mr-2" />}
              Assign
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
