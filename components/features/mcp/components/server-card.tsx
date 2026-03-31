"use client"

import { useState } from "react"
import {
  Plus, Trash2, Globe, Terminal, ChevronDown,
} from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Button } from "@/components/ui/button"
import { Badge } from "@/components/ui/badge"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"
import type { ServerEntry, Credential } from "../types"
import { CredentialPicker } from "./credential-picker"

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface ServerCardProps {
  entry: ServerEntry
  index: number
  readOnly: boolean
  credentials: Credential[]
  credLoading: boolean
  hasCredentialSupport: boolean
  workspaceId?: string
  onFetchCredentials: () => void
  onAddCredential: (cred: Credential) => void
  onUpdate: (index: number, patch: Partial<ServerEntry>) => void
  onRemove: (index: number) => void
  onAddEnvVar: (index: number) => void
  onUpdateEnvVar: (serverIdx: number, envIdx: number, field: "key" | "value", val: string) => void
  onRemoveEnvVar: (serverIdx: number, envIdx: number) => void
  onAddHeader: (index: number) => void
  onUpdateHeader: (serverIdx: number, hdrIdx: number, field: "key" | "value", val: string) => void
  onRemoveHeader: (serverIdx: number, hdrIdx: number) => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function ServerCard({
  entry,
  index,
  readOnly,
  credentials,
  credLoading,
  hasCredentialSupport,
  workspaceId,
  onFetchCredentials,
  onAddCredential,
  onUpdate,
  onRemove,
  onAddEnvVar,
  onUpdateEnvVar,
  onRemoveEnvVar,
  onAddHeader,
  onUpdateHeader,
  onRemoveHeader,
}: ServerCardProps) {
  const [advancedOpen, setAdvancedOpen] = useState(
    entry.env.length > 0 || entry.headers.length > 0,
  )

  return (
    <Card>
      <CardHeader className="pb-0">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 flex-1 min-w-0">
            {readOnly ? (
              <CardTitle className="text-sm font-mono truncate">
                {entry.name || "(unnamed)"}
              </CardTitle>
            ) : (
              <Input
                value={entry.name}
                onChange={(e) => onUpdate(index, { name: e.target.value })}
                placeholder="server-name"
                className="h-8 text-sm font-mono max-w-[200px]"
              />
            )}
            <Badge variant="secondary" className="gap-1 shrink-0">
              {entry.transport === "http" ? (
                <>
                  <Globe className="h-3 w-3" />
                  HTTP
                </>
              ) : (
                <>
                  <Terminal className="h-3 w-3" />
                  stdio
                </>
              )}
            </Badge>
          </div>
          {!readOnly && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => onRemove(index)}
              className="h-8 w-8 p-0 text-muted-foreground hover:text-destructive"
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          )}
        </div>
      </CardHeader>

      <CardContent className="space-y-3 pt-3">
        {/* Transport selector */}
        {!readOnly && (
          <div className="space-y-1.5">
            <Label className="text-xs">Transport</Label>
            <Select
              value={entry.transport}
              onValueChange={(val: "stdio" | "http") =>
                onUpdate(index, { transport: val })
              }
            >
              <SelectTrigger className="w-full h-8 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="stdio">stdio (local process)</SelectItem>
                <SelectItem value="http">HTTP (remote)</SelectItem>
              </SelectContent>
            </Select>
          </div>
        )}

        {/* Transport-specific fields */}
        {entry.transport === "stdio" ? (
          <div className="space-y-3">
            <div className="space-y-1.5">
              <Label className="text-xs">Command</Label>
              <Input
                value={entry.command}
                onChange={(e) => onUpdate(index, { command: e.target.value })}
                placeholder="npx"
                disabled={readOnly}
                className="h-8 text-xs font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">Arguments</Label>
              <Input
                value={entry.args}
                onChange={(e) => onUpdate(index, { args: e.target.value })}
                placeholder="-y @modelcontextprotocol/server-github"
                disabled={readOnly}
                className="h-8 text-xs font-mono"
              />
              <p className="text-xs text-muted-foreground">
                Space-separated arguments passed to the command.
              </p>
            </div>
          </div>
        ) : (
          <div className="space-y-1.5">
            <Label className="text-xs">URL</Label>
            <Input
              value={entry.url}
              onChange={(e) => onUpdate(index, { url: e.target.value })}
              placeholder="https://mcp.example.com/sse"
              disabled={readOnly}
              className="h-8 text-xs font-mono"
            />
          </div>
        )}

        {/* Collapsible advanced section for headers + env */}
        <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
          <CollapsibleTrigger asChild>
            <button
              type="button"
              className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              <ChevronDown
                className={cn(
                  "h-3.5 w-3.5 transition-transform",
                  advancedOpen && "rotate-180",
                )}
              />
              Advanced ({entry.env.length} env{entry.transport === "http" ? `, ${entry.headers.length} headers` : ""})
            </button>
          </CollapsibleTrigger>

          <CollapsibleContent className="space-y-3 pt-2">
            {/* Headers (HTTP only) */}
            {entry.transport === "http" && (
              <div className="space-y-2">
                <Label className="text-xs">Headers</Label>
                {entry.headers.map((h, hIdx) => (
                  <div key={hIdx} className="flex items-center gap-2">
                    <Input
                      value={h.key}
                      onChange={(e) => onUpdateHeader(index, hIdx, "key", e.target.value)}
                      placeholder="Header-Name"
                      disabled={readOnly}
                      className="h-7 text-xs font-mono flex-1"
                    />
                    <Input
                      value={h.value}
                      onChange={(e) => onUpdateHeader(index, hIdx, "value", e.target.value)}
                      placeholder="Bearer ${TOKEN}"
                      disabled={readOnly}
                      className="h-7 text-xs font-mono flex-1"
                    />
                    {!readOnly && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => onRemoveHeader(index, hIdx)}
                        className="h-7 w-7 p-0 shrink-0 text-muted-foreground hover:text-destructive"
                      >
                        <Trash2 className="h-3 w-3" />
                      </Button>
                    )}
                  </div>
                ))}
                {!readOnly && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => onAddHeader(index)}
                    className="h-7 text-xs gap-1"
                  >
                    <Plus className="h-3 w-3" />
                    Add Header
                  </Button>
                )}
              </div>
            )}

            {/* Environment variables */}
            <div className="space-y-2">
              <Label className="text-xs">Environment Variables</Label>
              {entry.env.map((e, eIdx) => (
                <div key={eIdx} className="flex items-center gap-2">
                  <Input
                    value={e.key}
                    onChange={(ev) => onUpdateEnvVar(index, eIdx, "key", ev.target.value)}
                    placeholder="VAR_NAME"
                    disabled={readOnly}
                    className="h-7 text-xs font-mono flex-1"
                  />
                  {hasCredentialSupport && workspaceId ? (
                    <div className="flex-1">
                      <CredentialPicker
                        envKey={e.key}
                        envValue={e.value}
                        credentials={credentials}
                        credLoading={credLoading}
                        workspaceId={workspaceId}
                        onFetchCredentials={onFetchCredentials}
                        onAddCredential={onAddCredential}
                        onChangeValue={(val) => onUpdateEnvVar(index, eIdx, "value", val)}
                      />
                    </div>
                  ) : (
                    <Input
                      value={e.value}
                      onChange={(ev) => onUpdateEnvVar(index, eIdx, "value", ev.target.value)}
                      placeholder="${CREDENTIAL_REF}"
                      disabled={readOnly}
                      className="h-7 text-xs font-mono flex-1"
                    />
                  )}
                  {!readOnly && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => onRemoveEnvVar(index, eIdx)}
                      className="h-7 w-7 p-0 shrink-0 text-muted-foreground hover:text-destructive"
                    >
                      <Trash2 className="h-3 w-3" />
                    </Button>
                  )}
                </div>
              ))}
              {!readOnly && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => onAddEnvVar(index)}
                  className="h-7 text-xs gap-1"
                >
                  <Plus className="h-3 w-3" />
                  Add Variable
                </Button>
              )}
              {!hasCredentialSupport && (
                <p className="text-xs text-muted-foreground">
                  Use {"${VAR_NAME}"} syntax to reference credentials. Claude Code expands environment variables automatically.
                </p>
              )}
            </div>
          </CollapsibleContent>
        </Collapsible>
      </CardContent>
    </Card>
  )
}
