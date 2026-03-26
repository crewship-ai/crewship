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
import type { WorkspaceMCPServer } from "@/lib/types/integration"

interface EditIntegrationDialogProps {
  workspaceId: string
  server: WorkspaceMCPServer
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}

export function EditIntegrationDialog({
  workspaceId,
  server,
  open,
  onOpenChange,
  onSuccess,
}: EditIntegrationDialogProps) {
  const [displayName, setDisplayName] = React.useState(server.display_name)
  const [transport, setTransport] = React.useState<"streamable-http" | "stdio">(server.transport)
  const [endpoint, setEndpoint] = React.useState(server.endpoint ?? "")
  const [command, setCommand] = React.useState(server.command ?? "")
  const [icon, setIcon] = React.useState(server.icon ?? "")
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState("")

  React.useEffect(() => {
    if (open) {
      setDisplayName(server.display_name)
      setTransport(server.transport)
      setEndpoint(server.endpoint ?? "")
      setCommand(server.command ?? "")
      setIcon(server.icon ?? "")
      setError("")
    }
  }, [open, server])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError("")

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
        display_name: displayName.trim(),
        transport,
      }
      if (transport === "streamable-http") body.endpoint = endpoint.trim()
      if (transport === "stdio") body.command = command.trim()
      body.icon = icon.trim() || null

      const res = await fetch(`/api/v1/integrations/${server.id}?workspace_id=${workspaceId}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })

      if (!res.ok) {
        const data = await res.json()
        setError(typeof data.error === "string" ? data.error : "Failed to update integration")
        return
      }

      onOpenChange(false)
      onSuccess()
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Edit Integration</DialogTitle>
          <DialogDescription>
            Update the MCP server configuration for {server.display_name}.
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="edit-integration-name">Name (slug)</Label>
            <Input
              id="edit-integration-name"
              value={server.name}
              readOnly
              className="bg-muted"
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="edit-integration-display-name">Display Name</Label>
            <Input
              id="edit-integration-display-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              required
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="edit-integration-transport">Transport</Label>
            <Select value={transport} onValueChange={(v) => setTransport(v as "streamable-http" | "stdio")}>
              <SelectTrigger id="edit-integration-transport" className="w-full">
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
              <Label htmlFor="edit-integration-endpoint">Endpoint</Label>
              <Input
                id="edit-integration-endpoint"
                placeholder="e.g. http://localhost:3000/mcp"
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
                required
              />
            </div>
          )}

          {transport === "stdio" && (
            <div className="space-y-2">
              <Label htmlFor="edit-integration-command">Command</Label>
              <Input
                id="edit-integration-command"
                placeholder="e.g. npx @modelcontextprotocol/server-github"
                value={command}
                onChange={(e) => setCommand(e.target.value)}
                required
              />
            </div>
          )}

          <div className="space-y-2">
            <Label htmlFor="edit-integration-icon">Icon (optional)</Label>
            <Input
              id="edit-integration-icon"
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
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={submitting}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting}>
              {submitting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
              Save Changes
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
