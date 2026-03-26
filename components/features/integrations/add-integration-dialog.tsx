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

interface AddIntegrationDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}

export function AddIntegrationDialog({
  workspaceId,
  open,
  onOpenChange,
  onSuccess,
}: AddIntegrationDialogProps) {
  const [name, setName] = React.useState("")
  const [displayName, setDisplayName] = React.useState("")
  const [transport, setTransport] = React.useState<"streamable-http" | "stdio">("streamable-http")
  const [endpoint, setEndpoint] = React.useState("")
  const [command, setCommand] = React.useState("")
  const [icon, setIcon] = React.useState("")
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState("")

  function resetForm() {
    setName("")
    setDisplayName("")
    setTransport("streamable-http")
    setEndpoint("")
    setCommand("")
    setIcon("")
    setError("")
  }

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) resetForm()
    onOpenChange(nextOpen)
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError("")

    if (!name.trim()) {
      setError("Name is required")
      return
    }
    if (!displayName.trim()) {
      setError("Display name is required")
      return
    }
    if (transport === "streamable-http" && !endpoint.trim()) {
      setError("Endpoint is required for streamable-http transport")
      return
    }
    if (transport === "stdio" && !command.trim()) {
      setError("Command is required for stdio transport")
      return
    }

    setSubmitting(true)

    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        display_name: displayName.trim(),
        transport,
        enabled: true,
      }
      if (transport === "streamable-http") body.endpoint = endpoint.trim()
      if (transport === "stdio") body.command = command.trim()
      if (icon.trim()) body.icon = icon.trim()

      const res = await fetch(`/api/v1/integrations?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })

      if (!res.ok) {
        const data = await res.json()
        setError(typeof data.error === "string" ? data.error : "Failed to create integration")
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
          <DialogTitle>Add Integration</DialogTitle>
          <DialogDescription>
            Connect an MCP server to make its tools available to your agents.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="integration-name">Name (slug)</Label>
            <Input
              id="integration-name"
              placeholder="e.g. github-tools"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="integration-display-name">Display Name</Label>
            <Input
              id="integration-display-name"
              placeholder="e.g. GitHub Tools"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              required
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="integration-transport">Transport</Label>
            <Select value={transport} onValueChange={(v) => setTransport(v as "streamable-http" | "stdio")}>
              <SelectTrigger id="integration-transport" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="streamable-http">Streamable HTTP</SelectItem>
                <SelectItem value="stdio">Stdio</SelectItem>
              </SelectContent>
            </Select>
          </div>

          {transport === "streamable-http" && (
            <div className="space-y-2">
              <Label htmlFor="integration-endpoint">Endpoint</Label>
              <Input
                id="integration-endpoint"
                placeholder="e.g. http://localhost:3000/mcp"
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
                required
              />
            </div>
          )}

          {transport === "stdio" && (
            <div className="space-y-2">
              <Label htmlFor="integration-command">Command</Label>
              <Input
                id="integration-command"
                placeholder="e.g. npx @modelcontextprotocol/server-github"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                required
              />
            </div>
          )}

          <div className="space-y-2">
            <Label htmlFor="integration-icon">Icon (optional)</Label>
            <Input
              id="integration-icon"
              placeholder="e.g. github, database, globe"
              value={icon}
              onChange={(e) => setIcon(e.target.value)}
            />
            <p className="text-label text-muted-foreground">Lucide icon name</p>
          </div>

          {error && (
            <p className="text-body text-destructive">{error}</p>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => handleOpenChange(false)} disabled={submitting}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              Add Integration
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
